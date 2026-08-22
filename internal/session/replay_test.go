package session

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
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

// TestAttachReplaysOutput verifies that a second attach receives the
// output produced during (or before) the first attach, so a reconnecting
// client sees the prompt instead of a blank screen.
func TestAttachReplaysOutput(t *testing.T) {
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

	// Second attach: the same output must be replayed immediately.
	second := &bytes.Buffer{}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	errCh2 := make(chan error, 1)
	go func() {
		errCh2 <- sess.Attach(ctx2, &splitConn{reader: blockedReader{}, writer: second}, AttachOptions{})
	}()

	waitForText(t, second, "zsh: $ ")

	// Live output still flows after the replay.
	stub.writer.Write([]byte("hello world"))
	waitForText(t, second, "hello world")

	cancel2()
	<-errCh2
}

// TestRingKeepsTail verifies the ring buffer bounds and tail retention.
func TestRingKeepsTail(t *testing.T) {
	r := newRing(16)
	r.Write([]byte("abcdefghij"))
	r.Write([]byte("klmnopqrstuvwxyz"))
	got := string(r.Bytes())
	if got != "klmnopqrstuvwxyz" {
		t.Fatalf("expected tail kept, got %q", got)
	}

	r.Write([]byte("0123456789ABCDEF")) // exactly the capacity
	if got := string(r.Bytes()); got != "0123456789ABCDEF" {
		t.Fatalf("expected exact-capacity write, got %q", got)
	}

	r.Write([]byte("OVERFLOW")) // chunk bigger than the capacity
	if got := string(r.Bytes()); got != "89ABCDEFOVERFLOW" {
		t.Fatalf("expected the most recent 16 bytes, got %q", got)
	}
}
