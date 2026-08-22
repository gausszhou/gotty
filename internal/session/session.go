package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// State is the lifecycle state of a session.
type State int

const (
	// StateIdle means the PTY is running and no client is attached.
	StateIdle State = iota
	// StateRunning means the PTY is running and a client is attached.
	StateRunning
	// StateDestroyed means the session has been destroyed.
	StateDestroyed
)

// String returns the wire representation of the state.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateDestroyed:
		return "destroyed"
	default:
		return "unknown"
	}
}

// Errors returned by session operations.
var (
	// ErrSessionBusy is returned when a second client tries to attach to
	// a session that already has a client.
	ErrSessionBusy = errors.New("session is already attached")
	// ErrSessionDestroyed is returned when operating on a destroyed session.
	ErrSessionDestroyed = errors.New("session is destroyed")
	// ErrClientClosed indicates the client connection was closed.
	ErrClientClosed = errors.New("client closed")
)

// AttachOptions parameterizes a single attach operation.
type AttachOptions struct {
	// PermitWrite allows the client to write input to the PTY.
	PermitWrite bool
	// FixedCols/FixedRows pin the terminal size; client resize requests
	// are ignored while they are non-zero.
	FixedCols, FixedRows int
	// WindowTitle is sent as a SetWindowTitle frame on attach.
	WindowTitle []byte
	// ReconnectSeconds enables client reconnection when > 0.
	ReconnectSeconds int
	// Preferences is sent as a SetPreferences frame when non-empty.
	Preferences []byte
}

// Terminal is the capability a session needs from the terminal layer.
// *terminal.Terminal implements it; tests may plug in stubs.
type Terminal interface {
	io.ReadWriter

	Resize(cols, rows int) error
	Signal(sig syscall.Signal) error
	Close() error
	Exited() bool
	Wait() error

	PID() int
	Command() string
	Args() []string
	WindowTitleVariables() map[string]interface{}
}

// Session is one terminal process with its lifecycle state.
// A Session is safe for concurrent use.
type Session struct {
	id        string
	term      Terminal
	createdAt time.Time

	mu          sync.Mutex
	state       State
	conn        io.ReadWriter
	lastTouched time.Time // last attach or detach; idle-timeout reference
}

// New creates a Session wrapping an already-started terminal.
func New(id string, term Terminal) *Session {
	return &Session{
		id:          id,
		term:        term,
		createdAt:   time.Now(),
		state:       StateIdle,
		lastTouched: time.Now(),
	}
}

// ID returns the session id.
func (s *Session) ID() string { return s.id }

// CreatedAt returns the creation time.
func (s *Session) CreatedAt() time.Time { return s.createdAt }

// Command returns the command running in the session.
func (s *Session) Command() string { return s.term.Command() }

// Args returns the arguments of the command.
func (s *Session) Args() []string { return s.term.Args() }

// PID returns the process id of the session.
func (s *Session) PID() int { return s.term.PID() }

// State returns the current lifecycle state.
func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Exited reports whether the underlying terminal process has exited.
func (s *Session) Exited() bool { return s.term.Exited() }

// Resize resizes the underlying PTY, unless a fixed size is enforced.
func (s *Session) Resize(cols, rows int) error {
	return s.term.Resize(cols, rows)
}

// Signal sends a signal to the underlying process.
func (s *Session) Signal(sig syscall.Signal) error {
	if s.State() == StateDestroyed {
		return ErrSessionDestroyed
	}
	return s.term.Signal(sig)
}

// StateDescription returns the view of the session used by the REST API.
func (s *Session) StateDescription() StateDescription {
	return StateDescription{
		ID:        s.id,
		State:     s.State().String(),
		Command:   s.Command(),
		Args:      s.Args(),
		PID:       s.PID(),
		Exited:    s.Exited(),
		CreatedAt: s.createdAt.Format(time.RFC3339),
	}
}

// StateDescription is the wire representation of a session.
type StateDescription struct {
	ID        string   `json:"id"`
	State     string   `json:"state"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	PID       int      `json:"pid"`
	Exited    bool     `json:"exited"`
	CreatedAt string   `json:"created_at"`
}

// Attach bridges conn with the terminal using the binary protocol and
// blocks until the client disconnects, the context is canceled, or the
// terminal process exits.
//
// While attached, the session is in StateRunning; afterwards it returns
// to StateIdle and the PTY keeps running (client disconnect only detaches).
func (s *Session) Attach(ctx context.Context, conn io.ReadWriter, opts AttachOptions) error {
	s.mu.Lock()
	switch s.state {
	case StateDestroyed:
		s.mu.Unlock()
		return ErrSessionDestroyed
	case StateRunning:
		s.mu.Unlock()
		return ErrSessionBusy
	}
	s.state = StateRunning
	s.conn = conn
	s.lastTouched = time.Now()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.state == StateRunning {
			s.state = StateIdle
		}
		s.conn = nil
		s.lastTouched = time.Now()
		s.mu.Unlock()
	}()

	return s.bridge(ctx, conn, opts)
}

// Detach force-closes the current client connection, if any.
// The PTY keeps running.
func (s *Session) Detach() {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()

	if closer, ok := conn.(io.Closer); ok && conn != nil {
		closer.Close()
	}
}

// Destroy tears the session down: it closes the client connection when
// attached and kills the terminal process, then marks the session destroyed.
func (s *Session) Destroy() error {
	s.mu.Lock()
	if s.state == StateDestroyed {
		s.mu.Unlock()
		return nil
	}
	s.state = StateDestroyed
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()

	if closer, ok := conn.(io.Closer); ok && conn != nil {
		closer.Close()
	}

	return s.term.Close()
}

// LastTouched returns the time of the last attach or detach.
func (s *Session) LastTouched() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTouched
}

// bridge pumps bytes in both directions.
func (s *Session) bridge(ctx context.Context, conn io.ReadWriter, opts AttachOptions) error {
	if err := s.sendInitializeMessage(conn, opts); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, 2)
	go func() {
		errs <- s.slaveToMaster(ctx, conn)
	}()
	go func() {
		errs <- s.masterToSlave(ctx, conn, opts)
	}()

	select {
	case err := <-errs:
		cancel()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) sendInitializeMessage(conn io.ReadWriter, opts AttachOptions) error {
	messages := [][]byte{
		terminal.EncodeWindowTitle(opts.WindowTitle),
	}
	if opts.ReconnectSeconds > 0 {
		messages = append(messages, terminal.EncodeReconnect(opts.ReconnectSeconds))
	}
	if len(opts.Preferences) > 0 {
		messages = append(messages, terminal.EncodePreferences(opts.Preferences))
	}

	for _, message := range messages {
		if _, err := conn.Write(message); err != nil {
			return fmt.Errorf("failed to send initializing message: %w", err)
		}
	}
	return nil
}

// slaveToMaster relays terminal output to the client.
func (s *Session) slaveToMaster(ctx context.Context, conn io.ReadWriter) error {
	buffer := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := s.term.Read(buffer)
		if err != nil {
			return terminal.ErrTerminalClosed
		}
		if n == 0 {
			continue
		}

		if _, err := conn.Write(terminal.EncodeOutput(buffer[:n])); err != nil {
			return ErrClientClosed
		}
	}
}

// masterToSlave relays client frames to the terminal.
func (s *Session) masterToSlave(ctx context.Context, conn io.ReadWriter, opts AttachOptions) error {
	buffer := make([]byte, 32*1024)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := conn.Read(buffer)
		if err != nil {
			return ErrClientClosed
		}

		message, err := terminal.DecodeClientFrame(buffer[:n])
		if err != nil {
			return err
		}

		switch message.Type {
		case terminal.Input:
			if !opts.PermitWrite || len(message.Payload) == 0 {
				continue
			}
			if _, err := s.term.Write(message.Payload); err != nil {
				return fmt.Errorf("failed to write received data to terminal: %w", err)
			}

		case terminal.Ping:
			if _, err := conn.Write(terminal.EncodePong()); err != nil {
				return ErrClientClosed
			}

		case terminal.ResizeTerminal:
			if opts.FixedCols > 0 && opts.FixedRows > 0 {
				continue
			}
			args, err := terminal.ParseResizeArgs(message.Payload)
			if err != nil {
				return err
			}
			if err := s.term.Resize(args.Columns, args.Rows); err != nil {
				return fmt.Errorf("failed to resize terminal: %w", err)
			}
		}
	}
}
