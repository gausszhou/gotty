package session

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

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
