package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// splitConn separates read and write into independent pipes,
// mimicking a bidirectional connection.
type splitConn struct {
	reader io.Reader
	writer io.Writer
}

func (c *splitConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *splitConn) Write(p []byte) (int, error) { return c.writer.Write(p) }

// Close 实现 io.Closer:关闭两端 pipe,令阻塞中的 Read 返回错误,
// 供抢占测试(Session.Attach 踢除旧客户端)使用。
func (c *splitConn) Close() error {
	if r, ok := c.reader.(io.Closer); ok {
		r.Close()
	}
	if w, ok := c.writer.(io.Closer); ok {
		w.Close()
	}
	return nil
}

// discardRW swallows reads and writes; used when a side is irrelevant.
type discardRW struct{}

func (discardRW) Read(p []byte) (int, error)  { return 0, io.EOF }
func (discardRW) Write(p []byte) (int, error) { return len(p), nil }

// stubTerminal implements Terminal with scriptable pipes.
type stubTerminal struct {
	command string
	args    []string
	pid     int

	reader *io.PipeReader // session reads PTY output from here
	writer *io.PipeWriter // session writes PTY input to here

	mu       sync.Mutex
	resizes  [][2]int
	signals  []syscall.Signal
	written  []byte
	closedCh chan struct{}
}

func newStubTerminal(command string) *stubTerminal {
	r, w := io.Pipe()
	return &stubTerminal{
		command:  command,
		pid:      4242,
		reader:   r,
		writer:   w,
		closedCh: make(chan struct{}),
	}
}

func (s *stubTerminal) Read(p []byte) (int, error) { return s.reader.Read(p) }

func (s *stubTerminal) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, p...)
	return len(p), nil
}

func (s *stubTerminal) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resizes = append(s.resizes, [2]int{cols, rows})
	return nil
}

func (s *stubTerminal) Signal(sig syscall.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.signals = append(s.signals, sig)
	return nil
}

func (s *stubTerminal) Close() error {
	select {
	case <-s.closedCh:
	default:
		close(s.closedCh)
	}
	s.reader.Close()
	return nil
}

func (s *stubTerminal) Exited() bool {
	select {
	case <-s.closedCh:
		return true
	default:
		return false
	}
}

func (s *stubTerminal) Wait() error { <-s.closedCh; return nil }
func (s *stubTerminal) PID() int    { return s.pid }
func (s *stubTerminal) Command() string {
	return s.command
}
func (s *stubTerminal) Args() []string { return s.args }
func (s *stubTerminal) WindowTitleVariables() map[string]interface{} {
	return map[string]interface{}{"command": s.command}
}

func (s *stubTerminal) writtenString() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.written)
}

func stubFactory() (TerminalFactory, *stubTerminal) {
	stub := newStubTerminal("mock")
	return func(command string, args []string, opts ...terminal.Option) (Terminal, error) {
		// 记录真实入参,供命令/参数断言使用
		stub.command = command
		stub.args = args
		return stub, nil
	}, stub
}

// perCallFactory creates a fresh stub per factory call, so each session
// owns an independent terminal (unlike the shared stubFactory singleton).
func perCallFactory() TerminalFactory {
	return func(command string, args []string, opts ...terminal.Option) (Terminal, error) {
		return newStubTerminal(command), nil
	}
}

func TestManagerLifecycle(t *testing.T) {
	factory, _ := stubFactory()
	m := NewManager(WithTerminalFactory(factory))

	sess, err := m.Create("mock", []string{"-x"})
	if err != nil {
		t.Fatalf("unexpected error from Create(): %s", err)
	}
	if sess.ID() == "" {
		t.Fatal("session id must not be empty")
	}
	if sess.State() != StateIdle {
		t.Fatalf("unexpected state: %s", sess.State())
	}

	if m.Count() != 1 || len(m.List()) != 1 {
		t.Fatalf("unexpected session count: %d", m.Count())
	}

	got, err := m.Get(sess.ID())
	if err != nil || got != sess {
		t.Fatalf("Get() mismatch: %v, %v", got, err)
	}

	if err := m.Destroy(sess.ID()); err != nil {
		t.Fatalf("unexpected error from Destroy(): %s", err)
	}
	if sess.State() != StateDestroyed {
		t.Fatalf("unexpected state after destroy: %s", sess.State())
	}
	if _, err := m.Get(sess.ID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
	if m.Count() != 0 {
		t.Fatalf("unexpected session count after destroy: %d", m.Count())
	}

	if err := m.Destroy(sess.ID()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on double destroy, got: %v", err)
	}
}

func TestManagerMaxSession(t *testing.T) {
	factory, _ := stubFactory()
	m := NewManager(WithTerminalFactory(factory), WithMaxSession(1))

	if _, err := m.Create("mock", nil); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if _, err := m.Create("mock", nil); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("expected ErrTooManySessions, got: %v", err)
	}
}

func TestManagerIdleTimeout(t *testing.T) {
	factory, _ := stubFactory()
	m := NewManager(WithTerminalFactory(factory), WithIdleTimeout(10*time.Second))
	sess, _ := m.Create("mock", nil)

	// freshly created sessions are not expired
	m.DestroyExpired()
	if m.Count() != 1 {
		t.Fatal("fresh session must not be expired")
	}

	sess.mu.Lock()
	sess.lastTouched = time.Now().Add(-time.Minute)
	sess.mu.Unlock()

	m.DestroyExpired()
	if m.Count() != 0 {
		t.Fatal("idle session must be expired")
	}
	if sess.State() != StateDestroyed {
		t.Fatalf("unexpected state: %s", sess.State())
	}
}

func TestManagerRemovesExitedSessions(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, _ := m.Create("mock", nil)

	stub.Close()
	m.DestroyExpired()
	if m.Count() != 0 {
		t.Fatal("exited session must be removed")
	}
	if sess.State() != StateIdle {
		t.Fatalf("exited session should not be marked destroyed: %s", sess.State())
	}
}

// readFrame reads one frame (with deadline) from the client-output pipe.
func readFrame(t *testing.T, reader *io.PipeReader) []byte {
	t.Helper()
	buf := make([]byte, 64*1024)
	done := make(chan struct{})
	var n int
	var err error
	go func() {
		n, err = reader.Read(buf)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a frame")
	}
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected read error: %s", err)
	}
	if n == 0 {
		t.Fatal("empty frame")
	}
	return buf[:n]
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAttachProtocol(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, err := m.Create("mock", nil)
	if err != nil {
		t.Fatalf("unexpected error from Create(): %s", err)
	}

	// client connection
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	conn := &splitConn{reader: inReader, writer: outWriter}

	opts := AttachOptions{
		PermitWrite:      true,
		WindowTitle:      []byte("my title"),
		ReconnectSeconds: 10,
	}

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(context.Background(), conn, opts)
	}()

	// 1. init frames: SetWindowTitle, then SetReconnect
	frame := readFrame(t, outReader)
	if frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected first frame type `%c`", frame[0])
	}
	if string(frame[1:]) != "my title" {
		t.Fatalf("unexpected title: %q", frame[1:])
	}
	frame = readFrame(t, outReader)
	if frame[0] != terminal.SetReconnect {
		t.Fatalf("unexpected second frame type `%c`", frame[0])
	}
	if string(frame[1:]) != "10" {
		t.Fatalf("unexpected reconnect payload: %q", frame[1:])
	}

	// 2. terminal output -> Output frame
	if _, err := stub.writer.Write([]byte("hello")); err != nil {
		t.Fatalf("unexpected error from stub write: %s", err)
	}
	frame = readFrame(t, outReader)
	if frame[0] != terminal.Output {
		t.Fatalf("unexpected frame type `%c`", frame[0])
	}
	if !bytes.Equal(frame[1:], []byte("hello")) {
		t.Fatalf("unexpected output: %q", frame[1:])
	}

	if sess.State() != StateRunning {
		t.Fatalf("unexpected state: %s", sess.State())
	}

	// 3. client input -> terminal
	if _, err := inWriter.Write(terminal.EncodeFrame(terminal.Input, []byte("abc"))); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	waitFor(t, "terminal input", func() bool { return stub.writtenString() == "abc" })

	// 4. ping -> pong
	if _, err := inWriter.Write([]byte{terminal.Ping}); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	frame = readFrame(t, outReader)
	if len(frame) != 1 || frame[0] != terminal.Pong {
		t.Fatalf("expected Pong, got: %v", frame)
	}

	// 5. resize -> terminal
	resize := terminal.EncodeFrame(terminal.ResizeTerminal, []byte(`{"columns":120,"rows":40}`))
	if _, err := inWriter.Write(resize); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	waitFor(t, "terminal resize", func() bool {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		return len(stub.resizes) == 1 && stub.resizes[0] == [2]int{120, 40}
	})

	// 6. 同 id 的新 attach 抢占当前客户端
	inReader3, inWriter3 := io.Pipe()
	outReader3, outWriter3 := io.Pipe()
	conn3 := &splitConn{reader: inReader3, writer: outWriter3}
	preemptDone := make(chan error, 1)
	go func() {
		preemptDone <- sess.Attach(context.Background(), conn3, opts)
	}()

	// 旧客户端被踢出,返回 ErrSessionPreempted
	if err := <-attachDone; !errors.Is(err, ErrSessionPreempted) {
		t.Fatalf("expected ErrSessionPreempted, got: %v", err)
	}
	// 会话仍被新客户端附着(Running)
	if sess.State() != StateRunning {
		t.Fatalf("unexpected state after preempt: %s", sess.State())
	}
	// 新客户端收到 init 帧 + 重放的历史输出
	if frame := readFrame(t, outReader3); frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected frame after preempt: `%c`", frame[0])
	}
	if frame := readFrame(t, outReader3); frame[0] != terminal.SetReconnect {
		t.Fatalf("unexpected second frame after preempt: `%c`", frame[0])
	}
	if frame := readFrame(t, outReader3); frame[0] != terminal.Output {
		t.Fatalf("expected replayed Output after preempt, got type `%c`", frame[0])
	} else if string(frame[1:]) != "hello" {
		t.Fatalf("unexpected replayed output after preempt: %q", frame[1:])
	}

	// 7. 新客户端断开 -> attach 返回,状态回 idle
	inWriter3.Close()
	if err := <-preemptDone; !errors.Is(err, ErrClientClosed) {
		t.Fatalf("expected ErrClientClosed, got: %v", err)
	}
	if sess.State() != StateIdle {
		t.Fatalf("unexpected state after detach: %s", sess.State())
	}

	// 8. reattach works and the same terminal lives on
	inReader2, inWriter2 := io.Pipe()
	outReader2, outWriter2 := io.Pipe()
	conn2 := &splitConn{reader: inReader2, writer: outWriter2}
	go func() {
		preemptDone <- sess.Attach(context.Background(), conn2, opts)
	}()
	if frame := readFrame(t, outReader2); frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected reattach frame type `%c`", frame[0])
	}
	if frame := readFrame(t, outReader2); frame[0] != terminal.SetReconnect {
		t.Fatalf("unexpected reattach second frame type `%c`", frame[0])
	}
	// 9. the buffered output from earlier ("hello" written during the first
	// attach) is replayed so the reattached client sees it immediately.
	if frame := readFrame(t, outReader2); frame[0] != terminal.Output {
		t.Fatalf("expected replayed Output frame, got type `%c`", frame[0])
	} else if string(frame[1:]) != "hello" {
		t.Fatalf("unexpected replayed output: %q", frame[1:])
	}
	inWriter2.Close()
	<-preemptDone
}

func TestAttachPermitWriteDisabled(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, _ := m.Create("mock", nil)

	inReader, inWriter := io.Pipe()
	conn := &splitConn{reader: inReader, writer: discardRW{}}

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(context.Background(), conn, AttachOptions{PermitWrite: false})
	}()

	if _, err := inWriter.Write(terminal.EncodeFrame(terminal.Input, []byte("ignored"))); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := stub.writtenString(); got != "" {
		t.Fatalf("input must be ignored without permit-write, got: %q", got)
	}

	inWriter.Close()
	if err := <-attachDone; !errors.Is(err, ErrClientClosed) {
		t.Fatalf("expected ErrClientClosed, got: %v", err)
	}
}

func TestAttachFixedSizeIgnoresResize(t *testing.T) {
	factory, stub := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, _ := m.Create("mock", nil)

	inReader, inWriter := io.Pipe()
	defer inReader.Close()
	conn := &splitConn{reader: inReader, writer: discardRW{}}

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(context.Background(), conn, AttachOptions{FixedCols: 80, FixedRows: 24})
	}()

	if _, err := inWriter.Write(terminal.EncodeFrame(terminal.ResizeTerminal, []byte(`{"columns":120,"rows":40}`))); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	time.Sleep(50 * time.Millisecond)

	stub.mu.Lock()
	resizes := append([][2]int{}, stub.resizes...)
	stub.mu.Unlock()
	if len(resizes) != 0 {
		t.Fatalf("resize must be ignored with fixed size, got: %v", resizes)
	}

	inWriter.Close()
	<-attachDone
}

func TestAttachDestroyedSession(t *testing.T) {
	factory, _ := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, _ := m.Create("mock", nil)
	sess.Destroy()

	err := sess.Attach(context.Background(), nil, AttachOptions{})
	if !errors.Is(err, ErrSessionDestroyed) {
		t.Fatalf("expected ErrSessionDestroyed, got: %v", err)
	}
}

func TestDestroyWhileAttached(t *testing.T) {
	factory, _ := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	sess, _ := m.Create("mock", nil)

	inReader, _ := io.Pipe()
	conn := &splitConn{reader: inReader, writer: discardRW{}}

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- sess.Attach(context.Background(), conn, AttachOptions{})
	}()

	// destroy while attached: closes the conn and the terminal
	if err := m.Destroy(sess.ID()); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	select {
	case err := <-attachDone:
		if err == nil {
			// attach may return nil if the bridge saw a clean disconnect
			t.Logf("attach returned: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attach did not return after destroy")
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("failed to create store: %s", err)
	}
	if len(store.All()) != 0 {
		t.Fatal("new store must be empty")
	}

	_ = store.Record(Metadata{ID: "a1", Command: "bash", State: "idle", CreatedAt: 111})
	_ = store.Record(Metadata{ID: "a2", Command: "top", Args: []string{"-d", "2"}, State: "running", CreatedAt: 222})

	// 模拟重启:重新加载同一文件
	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("failed to reload store: %s", err)
	}
	all := reloaded.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 records after reload, got %d", len(all))
	}

	byID := map[string]Metadata{}
	for _, m := range all {
		byID[m.ID] = m
	}
	if byID["a1"].Command != "bash" || byID["a1"].State != "idle" {
		t.Fatalf("unexpected a1 record: %+v", byID["a1"])
	}
	if byID["a2"].Args[0] != "-d" || byID["a2"].State != "running" {
		t.Fatalf("unexpected a2 record: %+v", byID["a2"])
	}

	// Forget 后重载为空
	if err := reloaded.Forget("a1"); err != nil {
		t.Fatalf("failed to forget: %s", err)
	}
	reloaded2, _ := NewFileStore(path)
	if len(reloaded2.All()) != 1 {
		t.Fatal("expected 1 record after forget")
	}
}

func TestManagerKeepsHistoryAfterDestroy(t *testing.T) {
	factory, _ := stubFactory()
	m := NewManager(WithTerminalFactory(factory))
	s1, _ := m.Create("mock", nil)
	_, _ = m.Create("mock", nil)

	if err := m.Destroy(s1.ID()); err != nil {
		t.Fatalf("failed to destroy: %s", err)
	}

	history := m.History()
	if len(history) != 1 || history[0].ID != s1.ID() {
		t.Fatalf("unexpected history: %+v", history)
	}
	if m.Count() != 1 {
		t.Fatalf("s2 should still be alive, count=%d", m.Count())
	}
}

func TestManagerHistorySurvivesRestart(t *testing.T) {
	factory, _ := stubFactory()
	path := filepath.Join(t.TempDir(), "sessions.json")
	store, _ := NewFileStore(path)

	m1 := NewManager(WithTerminalFactory(factory), WithStore(store))
	s, _ := m1.Create("mock", []string{"-x"})
	_ = m1.Destroy(s.ID())

	// 重启:同一 store 文件,新的 manager
	store2, _ := NewFileStore(path)
	m2 := NewManager(WithTerminalFactory(factory), WithStore(store2))
	history := m2.History()
	if len(history) != 1 || history[0].ID != s.ID() || history[0].Args[0] != "-x" {
		t.Fatalf("history lost after restart: %+v", history)
	}
}

func TestCreateWithIDIdempotent(t *testing.T) {
	factory, _ := stubFactory()
	m := NewManager(WithTerminalFactory(factory))

	const id = "aaaaaaaaaaaaaaaa"
	s1, created, err := m.CreateWithID(id, "mock", nil)
	if err != nil || !created {
		t.Fatalf("expected fresh create, got created=%v err=%v", created, err)
	}
	s2, created, err := m.CreateWithID(id, "mock", nil)
	if err != nil || created {
		t.Fatalf("expected idempotent hit, got created=%v err=%v", created, err)
	}
	if s1 != s2 {
		t.Fatal("idempotent create must return the same session")
	}
	// 幂等命中不占 max-session 名额
	if m.Count() != 1 {
		t.Fatalf("expected 1 session, got %d", m.Count())
	}
}

func TestCreateWithIDResurrectsRecordedSession(t *testing.T) {
	factory, _ := stubFactory()
	store := NewMemoryStore()
	m := NewManager(WithTerminalFactory(factory), WithStore(store))

	const id = "bbbbbbbbbbbbbbbb"
	s1, _, err := m.CreateWithID(id, "mock", []string{"-x"})
	if err != nil {
		t.Fatalf("failed to create: %s", err)
	}
	if err := m.Destroy(s1.ID()); err != nil {
		t.Fatalf("failed to destroy: %s", err)
	}

	// 复活:即使请求带了不同命令,也用记录中的命令重建
	s2, created, err := m.CreateWithID(id, "totally-different", nil)
	if err != nil {
		t.Fatalf("failed to resurrect: %s", err)
	}
	if !created {
		t.Fatal("expected a new session from resurrection")
	}
	if s2.ID() != id {
		t.Fatalf("resurrected id mismatch: %s", s2.ID())
	}
	if s2.Command() != "mock" || len(s2.Args()) != 1 || s2.Args()[0] != "-x" {
		t.Fatalf("resurrected session must use recorded command, got %s %v", s2.Command(), s2.Args())
	}
	meta, ok := store.Get(id)
	if !ok {
		t.Fatal("record must exist after resurrection")
	}
	if meta.RunCount != 1 {
		t.Fatalf("expected run_count=1 after resurrect, got %d", meta.RunCount)
	}
}

func TestManagerStatus(t *testing.T) {
	var lastStub *stubTerminal
	factory := func(command string, args []string, opts ...terminal.Option) (Terminal, error) {
		lastStub = newStubTerminal(command)
		return lastStub, nil
	}
	m := NewManager(WithTerminalFactory(factory))

	s1, _ := m.Create("mock", nil)
	s2, _ := m.Create("mock", nil)

	got := m.Status([]string{s1.ID(), s2.ID(), "nonexistent"})
	if len(got) != 2 {
		t.Fatalf("expected 2 alive, got %d", len(got))
	}
	if got[0] != s1 || got[1] != s2 {
		t.Fatal("status must preserve the requested order")
	}

	// destroyed 不算存活
	if err := m.Destroy(s1.ID()); err != nil {
		t.Fatalf("failed to destroy: %s", err)
	}
	got = m.Status([]string{s1.ID(), s2.ID()})
	if len(got) != 1 || got[0] != s2 {
		t.Fatalf("destroyed session must not be reported alive: %+v", got)
	}

	// exited 不算存活(清扫窗口期)
	lastStub.Close()
	got = m.Status([]string{s2.ID()})
	if len(got) != 0 {
		t.Fatalf("exited session must not be reported alive: %+v", got)
	}
}

// TestFileStoreMigratesLegacyArray: 旧格式(数组)在加载时被迁移为 map 并原子重写。
func TestFileStoreMigratesLegacyArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	legacy := `{"sessions": [
		{"id":"a1","command":"bash","state":"idle","created_at":111},
		{"id":"a2","command":"top","args":["-d","2"],"state":"running","created_at":222}
	]}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("failed to write legacy file: %s", err)
	}

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("failed to load legacy file: %s", err)
	}
	if len(store.All()) != 2 {
		t.Fatalf("expected 2 records after migration, got %d", len(store.All()))
	}

	// 文件已被重写为 map 格式,再次加载不再走迁移
	reloaded, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("failed to reload migrated file: %s", err)
	}
	if len(reloaded.All()) != 2 {
		t.Fatalf("expected 2 records after reload, got %d", len(reloaded.All()))
	}
	if meta, ok := reloaded.Get("a1"); !ok || meta.Command != "bash" {
		t.Fatalf("migrated record a1 mismatch: %+v", meta)
	}
}
