package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// blockedReader is a reader that never returns; attach stays alive until
// the context is canceled.
type blockedReader struct{}

func (blockedReader) Read([]byte) (int, error) {
	<-make(chan struct{}) // block forever
	return 0, io.EOF
}

// waitForText polls until buf contains needle or the deadline expires.
func waitForText(t *testing.T, buf *bytes.Buffer, needle string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(buf.Bytes(), []byte(needle)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("buffer never contained %q, got: %q", needle, buf.String())
}

// TestAttachReplaysTail verifies that a fresh attach receives the recent
// output (the tail) so the client sees the pre-refresh screen instead of
// a blank terminal. Live output still flows after the replay.
func TestAttachReplaysTail(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))

	sess, err := m.Create("mock", nil)
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}

	// First attach: feed one prompt, wait for delivery, then detach.
	first := &bytes.Buffer{}
	ctx1, cancel1 := context.WithCancel(context.Background())
	errCh1 := make(chan error, 1)
	go func() {
		errCh1 <- sess.Attach(ctx1, &splitConn{reader: blockedReader{}, writer: first}, AttachOptions{})
	}()
	defer cancel1()

	stub.writer.Write([]byte("zsh: $ "))
	waitForText(t, first, "zsh: $ ")
	cancel1()
	if err := <-errCh1; err != context.Canceled {
		t.Fatalf("unexpected first attach error: %v", err)
	}

	// Second attach: the recent prompt is replayed (screen restored).
	second := &bytes.Buffer{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- sess.Attach(ctx2, &splitConn{reader: blockedReader{}, writer: second}, AttachOptions{})
	}()

	waitForText(t, second, "zsh: $ ")

	// Live output flows to the new attach.
	stub.writer.Write([]byte("hello world"))
	waitForText(t, second, "hello world")

	cancel2()
	<-errCh2
}

// TestAttachReplayTailIsBounded verifies that a huge history is replayed
// only up to attachReplayTailBytes — the recent tail, not the whole log —
// so a refresh never scrolls through megabytes of old output.
func TestAttachReplayTailIsBounded(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))

	sess, err := m.Create("mock", nil)
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}

	// First attach. Feed well over the tail budget so the ring fills.
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(ctx1,
			&splitConn{reader: blockedReader{}, writer: io.Discard}, AttachOptions{})
	}()

	big := bytes.Repeat([]byte("0123456789abcdef"), 64*1024) // 1 MiB
	if _, err := stub.writer.Write(big); err != nil {
		t.Fatalf("stub write: %s", err)
	}
	// wait until the ring has ingested all of it
	waitFor(t, "ring ingest", func() bool {
		sess.outMu.Lock()
		defer sess.outMu.Unlock()
		return sess.total >= int64(len(big))
	})
	cancel1()
	if err := <-attachDone; err != context.Canceled {
		t.Fatalf("unexpected first attach error: %v", err)
	}

	// Second attach: count the replayed Output bytes up to the marker.
	in2, inW2 := io.Pipe()
	out2R, out2W := io.Pipe()
	attach2Done := make(chan error, 1)
	go func() {
		attach2Done <- sess.Attach(context.Background(),
			&splitConn{reader: in2, writer: out2W}, AttachOptions{})
	}()

	replayed := 0
	// drain init frames, then collect Output until SetReplayDone
	for {
		frame := readFrame(t, out2R)
		switch {
		case len(frame) == 1 && frame[0] == terminal.SetReplayDone:
			goto collected
		case frame[0] == terminal.Output:
			replayed += len(frame) - 1
		}
	}
collected:

	if replayed > attachReplayTailBytes {
		t.Fatalf("replay exceeded tail budget: %d > %d", replayed, attachReplayTailBytes)
	}
	if replayed == 0 {
		t.Fatal("expected a bounded replay tail, got none")
	}

	inW2.Close()
	<-attach2Done
}

// TestAttachRestoreJittersSize: 恢复历史会话(重放了非空输出尾部)时,
// 服务端在握手标记后自动补一次尺寸抖动(r-1 → r 两跳 SIGWINCH),让
// 前台程序整帧重绘,画面与真实 PTY 状态收敛。
func TestAttachRestoreJittersSize(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, err := m.Create("mock", nil)
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}

	// attach1: 发 resize(记录 lastSize)并产生输出,然后断开。
	in1, inW1 := io.Pipe()
	out1R, out1W := io.Pipe()
	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(context.Background(), &splitConn{reader: in1, writer: out1W}, AttachOptions{})
	}()
	for {
		f := readFrame(t, out1R)
		if len(f) == 1 && f[0] == terminal.SetReplayDone {
			break
		}
	}
	inW1.Write(terminal.EncodeFrame(terminal.ResizeTerminal, []byte(`{"columns":120,"rows":30}`)))
	// 首个 resize 触发 firstResize 抖动:30 → 29 → 30 共三次 Setsize
	waitFor(t, "resize recorded", func() bool {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return len(stub.resizes) >= 3
	})
	stub.writer.Write([]byte("zsh: $ "))
	time.Sleep(100 * time.Millisecond) // let the ring ingest
	inW1.Close()
	<-attachDone

	// attach2: 恢复会话 → marker 后服务端自动抖动。
	in2, inW2 := io.Pipe()
	out2R, out2W := io.Pipe()
	go func() {
		attachDone <- sess.Attach(context.Background(), &splitConn{reader: in2, writer: out2W}, AttachOptions{})
	}()
	for {
		f := readFrame(t, out2R)
		if len(f) == 1 && f[0] == terminal.SetReplayDone {
			break
		}
	}
	waitFor(t, "restore jitter", func() bool {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		n := len(stub.resizes)
		return n >= 2 &&
			stub.resizes[n-2] == [2]int{120, 29} &&
			stub.resizes[n-1] == [2]int{120, 30}
	})
	inW2.Close()
	<-attachDone
}

// TestAttachIgnoresTinyResize verifies the server ignores probe-sized
// "1 row" resizes (FitAddon clamps to rows=1 on a hidden container) so a
// session's PTY can never be shrunk to a single line.
func TestAttachIgnoresTinyResize(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, err := m.Create("mock", nil)
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	conn := &splitConn{reader: inReader, writer: outWriter}

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(context.Background(), conn, AttachOptions{})
	}()
	for {
		f := readFrame(t, outReader)
		if len(f) == 1 && f[0] == terminal.SetReplayDone {
			break
		}
	}

	// 1-row resize must be dropped entirely
	inWriter.Write(terminal.EncodeFrame(terminal.ResizeTerminal, []byte(`{"columns":120,"rows":1}`)))
	time.Sleep(150 * time.Millisecond)
	// a real resize still works
	inWriter.Write(terminal.EncodeFrame(terminal.ResizeTerminal, []byte(`{"columns":120,"rows":30}`)))
	waitFor(t, "real resize", func() bool {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return len(stub.resizes) >= 1
	})

	stub.mu.Lock()
	for _, r := range stub.resizes {
		if r[1] < 2 {
			stub.mu.Unlock()
			t.Fatalf("tiny resize reached the PTY: %v", r)
		}
	}
	stub.mu.Unlock()

	inWriter.Close()
	<-attachDone
}

// TestAttachHandshakeEmitsReplayDone verifies the handshake marker is sent
// right after the init frames (there is nothing to replay on a fresh
// session): the client keys input forwarding off it.
// TestAlignTailStartCutsEscapeSequences: 尾部重放的起点若落在转义序列
// 中间,对齐后会回退到最近的 ESC —— 否则 OSC/CSI 的载荷(颜色、应答)
// 会被 xterm 当普通文本显示(退出 TUI 后刷新看到的乱码)。
func TestAlignTailStartCutsEscapeSequences(t *testing.T) {
	// opencode 退出时的典型输出:OSC 10/11 颜色 + OSC 4 调色板 + CSI 应答
	data := []byte("gauss@host:~$ opencode\r\n" +
		"\x1b]10;rgb:cccc/cccc/cccc\x1b\\" +
		"\x1b]11;rgb:0000/0000/0000\x1b\\" +
		"\x1b[1;1R" +
		"tail body\r\n")

	// 起点切在第一个 OSC 的载荷中间("10;rgb:...")
	mid := bytes.Index(data, []byte("]10;rgb:"))
	start := alignTailStart(data, mid+1) // '10;rgb:...' 开头
	if start > mid {
		t.Fatalf("start %d did not rewind to the escape (payload at %d)", start, mid)
	}
	if data[start] != 0x1b {
		t.Fatalf("aligned start must be ESC, got 0x%02x", data[start])
	}

	// 起点切在不含转义的纯文本行中间(首行,前面无 ESC):保持不动
	hPos := bytes.IndexByte(data, 'h') // "host" 处,前面是普通文本
	start = alignTailStart(data, hPos)
	if start != hPos {
		t.Fatalf("plain-text start must be kept, got %d want %d", start, hPos)
	}
}

// TestAlignTailStartSkipsUtf8Continuation: 多字节字符被切开时,丢弃
// 起始的续字节,不显示半个字。
func TestAlignTailStartSkipsUtf8Continuation(t *testing.T) {
	data := []byte("ok \xf0\x9f\x98\x80 end") // 😀 U+1F600 的多字节
	// 起点从 '9f' 开始(续字节) → 跳到字符结束后的 ' end'
	start := alignTailStart(data, bytes.IndexByte(data, 0x9f))
	if data[start] != ' ' {
		t.Fatalf("expected skip to the byte after the cut character, got 0x%02x at %d", data[start], start)
	}
}

func TestAttachHandshakeEmitsReplayDone(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, err := m.Create("mock", nil)
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	conn := &splitConn{reader: inReader, writer: outWriter}

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(context.Background(), conn, AttachOptions{})
	}()

	// title frame ...
	if frame := readFrame(t, outReader); frame[0] != terminal.SetWindowTitle {
		t.Fatalf("expected SetWindowTitle first, got `%c`", frame[0])
	}
	// ... then the handshake marker, with no output frames in between.
	if frame := readFrame(t, outReader); len(frame) != 1 || frame[0] != terminal.SetReplayDone {
		t.Fatalf("expected SetReplayDone right after init, got: %v", frame)
	}
	// 新会话(无历史可重放):不做恢复抖动。
	stub.mu.Lock()
	got := len(stub.resizes)
	stub.mu.Unlock()
	if got != 0 {
		t.Fatalf("fresh session must not jitter size, got %d resizes", got)
	}

	inWriter.Close()
	<-attachDone
}

// fakeMirror is a scriptable ScreenMirror test double: it accumulates the
// PTY output it was fed and answers queries from a canned buffer.
type fakeMirror struct {
	mu      sync.Mutex
	buf     strings.Builder
	answers []byte
}

func (m *fakeMirror) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf.Write(p)
	return len(p), nil
}

func (m *fakeMirror) Resize(cols, rows int) {}

func (m *fakeMirror) DrainAnswers() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.answers
	m.answers = nil
	return a
}

func (m *fakeMirror) Snapshot() ScreenSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return ScreenSnapshot{Text: m.buf.String(), Raw: "fake"}
}

// eventually polls cond until it holds or 2s elapse.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// newMirrorSession wires a stub terminal with a fake mirror.
func newMirrorSession(t *testing.T, mirror ScreenMirror) (*Session, *stubTerminal) {
	t.Helper()
	stub := newStubTerminal("mock")
	sess := New("mirror-test", stub, WithScreenMirror(mirror))
	t.Cleanup(func() { stub.Close() })
	return sess, stub
}

func TestSessionMirrorFeedsAndScreens(t *testing.T) {
	mirror := &fakeMirror{}
	sess, stub := newMirrorSession(t, mirror)

	stub.writer.Write([]byte("hello agent")) // 经 outputPump 进入镜像
	eventually(t, func() bool {
		snap, err := sess.Screen()
		return err == nil && strings.Contains(snap.Text, "hello agent")
	})

	snap, err := sess.Screen()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap.Text, "hello agent") {
		t.Errorf("screen text = %q, want to contain hello agent", snap.Text)
	}
	if snap.Raw != "fake" {
		t.Errorf("snapshot Raw not carried through: %v", snap.Raw)
	}
}

func TestSessionMirrorDisabled(t *testing.T) {
	sess, _ := newMirrorSession(t, nil)
	if _, err := sess.Screen(); !errors.Is(err, ErrMirrorDisabled) {
		t.Errorf("Screen error = %v, want ErrMirrorDisabled", err)
	}
	_, _, err := sess.Wait(context.Background(), nil, 0, 0)
	if !errors.Is(err, ErrMirrorDisabled) {
		t.Errorf("Wait error = %v, want ErrMirrorDisabled", err)
	}
}

func TestSessionWaitRegex(t *testing.T) {
	mirror := &fakeMirror{}
	sess, stub := newMirrorSession(t, mirror)

	stub.writer.Write([]byte("hello world"))
	snap, result, err := sess.Wait(context.Background(), regexp.MustCompile("world"), 2*time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Errorf("wait result = %+v, want matched", result)
	}
	if !strings.Contains(snap.Text, "world") {
		t.Errorf("wait screen = %q, want to contain world", snap.Text)
	}
}

func TestSessionWaitQuiet(t *testing.T) {
	mirror := &fakeMirror{}
	sess, stub := newMirrorSession(t, mirror)

	stub.writer.Write([]byte("x"))
	_, result, err := sess.Wait(context.Background(), nil, 2*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Quiet {
		t.Errorf("wait result = %+v, want quiet", result)
	}
}

func TestSessionWaitTimeout(t *testing.T) {
	mirror := &fakeMirror{}
	sess, _ := newMirrorSession(t, mirror)

	snap, result, err := sess.Wait(context.Background(), regexp.MustCompile("never-appears"), 100*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut {
		t.Errorf("wait result = %+v, want timed_out", result)
	}
	if snap == nil {
		t.Error("timed-out wait must still return the current screen")
	}
}

func TestSessionWaitContextCancel(t *testing.T) {
	mirror := &fakeMirror{}
	sess, _ := newMirrorSession(t, mirror)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, _, err := sess.Wait(ctx, nil, 0, 0) // 无限等待,靠 ctx 打断
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Wait error = %v, want context.Canceled", err)
	}
}

func TestSessionInputWritesTerminal(t *testing.T) {
	stub := newStubTerminal("mock")
	sess := New("input-test", stub)
	t.Cleanup(func() { stub.Close() })

	if err := sess.Input([]byte("ls -la\r")); err != nil {
		t.Fatal(err)
	}
	if got := stub.writtenString(); got != "ls -la\r" {
		t.Errorf("written = %q, want %q", got, "ls -la\r")
	}
}

func TestSessionResizeSyncsMirror(t *testing.T) {
	mirror := &fakeMirror{}
	sess, stub := newMirrorSession(t, mirror)

	if err := sess.Resize(100, 40); err != nil {
		t.Fatal(err)
	}
	if got := stub.resizes; len(got) != 1 || got[0] != [2]int{100, 40} {
		t.Errorf("resizes = %v, want [[100 40]]", got)
	}
}

// 查询应答回写:无客户端附着且 answerQueries 开启时,镜像的应答写回 PTY;
// 附着客户端(浏览器自行应答)或 --answer-queries=false 时不写。
func TestOutputPumpAnswersWhenUnattached(t *testing.T) {
	mirror := &fakeMirror{answers: []byte("\x1b[0n")}
	sess, stub := newMirrorSession(t, mirror)

	stub.writer.Write([]byte("some output")) // 触发 pump
	eventually(t, func() bool {
		return strings.Contains(stub.writtenString(), "\x1b[0n")
	})
	// 应答也进了镜像(数据不因应答而丢)
	eventually(t, func() bool {
		snap, _ := sess.Screen()
		return strings.Contains(snap.Text, "some output")
	})
}

func TestOutputPumpDropsAnswersWhenDisabled(t *testing.T) {
	mirror := &fakeMirror{answers: []byte("\x1b[0n")}
	stub := newStubTerminal("mock")
	sess := New("no-answer", stub, WithScreenMirror(mirror), withAnswerQueries(false))
	t.Cleanup(func() { stub.Close() })
	_ = sess

	stub.writer.Write([]byte("some output"))
	time.Sleep(100 * time.Millisecond)
	if got := stub.writtenString(); strings.Contains(got, "\x1b[0n") {
		t.Errorf("answers written back despite --answer-queries=false: %q", got)
	}
}

func TestOutputPumpDropsAnswersWhenClientAttached(t *testing.T) {
	mirror := &fakeMirror{answers: []byte("\x1b[0n")}
	sess, stub := newMirrorSession(t, mirror)

	// 附着客户端:浏览器端 xterm 会自己应答,双答会污染
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(ctx, &splitConn{reader: blockedReader{}, writer: io.Discard}, AttachOptions{})
	}()
	eventually(t, func() bool { return sess.State() == StateRunning })

	stub.writer.Write([]byte("some output"))
	time.Sleep(100 * time.Millisecond)
	if got := stub.writtenString(); strings.Contains(got, "\x1b[0n") {
		t.Errorf("answers written back while a client is attached: %q", got)
	}
	// 镜像仍然收数据
	eventually(t, func() bool {
		snap, _ := sess.Screen()
		return strings.Contains(snap.Text, "some output")
	})
	cancel()
}
