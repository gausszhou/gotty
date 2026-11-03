package session

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// The attached capture engine (capture --engine attached) asks the browser
// that is already attached to composite its terminal view and send the PNG
// back. The server side is therefore a request/response waiter: send a
// CaptureRequest on the attached channel, park until the client answers with
// CaptureResult (or the timeout fires).
//
// A routed frame caps its payload at 64 KiB, so the client splits the base64
// PNG into chunks and the waiter assembles them here.

var (
	// ErrNoClientAttached is returned by Capture when nobody is attached: the
	// composite happens in the browser, so without a client there is nothing
	// to ask.
	ErrNoClientAttached = errors.New("no client attached to the session")

	// ErrCaptureTimeout is returned when the attached client did not answer in
	// time — closed tab, frozen renderer, or an old client that does not
	// understand CaptureRequest.
	ErrCaptureTimeout = errors.New("capture timed out waiting for the attached client")

	// ErrCaptureBusy is returned when another capture is already in flight on
	// the same session. The protocol has no request id, so overlapping
	// requests could not be told apart.
	ErrCaptureBusy = errors.New("another capture is already in flight for this session")
)

// DefaultCaptureTimeout bounds one capture request.
const DefaultCaptureTimeout = 10 * time.Second

// CaptureOptions tunes one capture request.
type CaptureOptions struct {
	// IncludeSelection asks the client to include the current text selection
	// in the composite. Reserved by the protocol; current clients ignore it.
	IncludeSelection bool
	// Timeout bounds the wait; zero means DefaultCaptureTimeout.
	Timeout time.Duration
}

// CaptureResult is one successful capture: the PNG plus the terminal geometry
// the client composited it from.
type CaptureResult struct {
	PNG  []byte
	Cols int
	Rows int
	At   time.Time
}

// captureReply is the assembled client answer handed to the waiter.
type captureReply struct {
	b64  string
	err  string
	cols int
	rows int
}

// Capture asks the currently attached client for a snapshot and blocks until
// it answers, the context is cancelled, or the timeout fires. The session's
// attachment is untouched: the request goes to whoever is attached, and no
// extra client is attached or preempted.
func (s *Session) Capture(ctx context.Context, opts CaptureOptions) (*CaptureResult, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCaptureTimeout
	}

	req, err := terminal.EncodeCaptureRequest(terminal.CaptureRequestFrame{
		IncludeSelection: opts.IncludeSelection,
	})
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	conn := s.conn
	state := s.state
	s.mu.Unlock()
	if conn == nil || state != StateRunning {
		return nil, ErrNoClientAttached
	}

	reply := make(chan captureReply, 1)
	s.captureMu.Lock()
	if s.captureWaiter != nil {
		s.captureMu.Unlock()
		return nil, ErrCaptureBusy
	}
	s.captureWaiter = reply
	s.captureChunks = s.captureChunks[:0]
	s.captureMu.Unlock()
	defer func() {
		s.captureMu.Lock()
		if s.captureWaiter == reply {
			s.captureWaiter = nil
		}
		s.captureChunks = s.captureChunks[:0]
		s.captureMu.Unlock()
	}()

	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("failed to send capture request: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrCaptureTimeout
	case frame := <-reply:
		if frame.err != "" {
			return nil, fmt.Errorf("client capture failed: %s", frame.err)
		}
		png, err := base64.StdEncoding.DecodeString(frame.b64)
		if err != nil {
			return nil, fmt.Errorf("invalid capture payload: %w", err)
		}
		if len(png) == 0 {
			return nil, errors.New("client returned an empty capture")
		}
		res := &CaptureResult{PNG: png, Cols: frame.cols, Rows: frame.rows, At: time.Now()}
		s.captureMu.Lock()
		s.captureLatest = res
		s.captureMu.Unlock()
		return res, nil
	}
}

// resolveCapture assembles a chunked client answer and hands the complete
// result to the pending waiter. Frames that arrive after a timeout (no
// waiter), out of order, or beyond MaxCaptureChunks are dropped.
func (s *Session) resolveCapture(frame terminal.CaptureResultFrame) {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()

	ch := s.captureWaiter
	if ch == nil {
		return
	}

	// A failed capture is a single frame with no chunks.
	if frame.Error != "" {
		s.captureChunks = s.captureChunks[:0]
		select {
		case ch <- captureReply{err: frame.Error}:
		default:
		}
		return
	}

	if frame.Total <= 0 || frame.Total > terminal.MaxCaptureChunks || frame.Seq != len(s.captureChunks) {
		// Out of order or malformed: drop the partial result and let the
		// waiter time out rather than returning a corrupt PNG.
		s.captureChunks = s.captureChunks[:0]
		return
	}

	s.captureChunks = append(s.captureChunks, frame.Data)
	if len(s.captureChunks) < frame.Total {
		return
	}

	full := captureReply{
		b64:  strings.Join(s.captureChunks, ""),
		cols: frame.Cols,
		rows: frame.Rows,
	}
	s.captureChunks = s.captureChunks[:0]
	select {
	case ch <- full:
	default:
	}
}

// LatestCapture returns the most recent successful capture, or nil when the
// session has never been captured.
func (s *Session) LatestCapture() *CaptureResult {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if s.captureLatest == nil {
		return nil
	}
	cp := *s.captureLatest
	return &cp
}
