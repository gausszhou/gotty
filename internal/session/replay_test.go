package session

import (
	"bytes"
	"context"
	"io"
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
