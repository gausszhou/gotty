package session

import (
	"bytes"
	"context"
	"errors"
	"io"
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
		return stub, nil
	}, stub
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

	// 6. second attach while running -> busy
	if err := sess.Attach(context.Background(), discardRW{}, opts); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("expected ErrSessionBusy, got: %v", err)
	}

	// 7. client disconnects -> attach returns, state back to idle
	inWriter.Close()
	if err := <-attachDone; !errors.Is(err, ErrClientClosed) {
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
		attachDone <- sess.Attach(context.Background(), conn2, opts)
	}()
	if frame := readFrame(t, outReader2); frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected reattach frame type `%c`", frame[0])
	}
	if frame := readFrame(t, outReader2); frame[0] != terminal.SetReconnect {
		t.Fatalf("unexpected reattach second frame type `%c`", frame[0])
	}
	inWriter2.Close()
	<-attachDone
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
