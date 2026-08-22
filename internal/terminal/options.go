package terminal

import (
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Default values used when constructing a Terminal.
const (
	DefaultCloseSignal  = syscall.SIGHUP
	DefaultCloseTimeout = -1 * time.Second // -1: wait forever, no kill escalation
)

// Options configures how Terminals are created.
type Options struct {
	// Env is a list of additional environment variables, e.g. ["FOO=bar"].
	Env []string `json:"env"`

	// CloseSignal is sent to the process when the session is closed.
	CloseSignal int `json:"close_signal" flagName:"close-signal" flagDescribe:"Signal sent to the command process when the session is closed" default:"1"`

	// CloseTimeout is the time in seconds to force kill the process
	// after the close signal has been sent. -1 disables the escalation.
	// Default 3: the close signal (SIGHUP) can be ignored by processes
	// started under nohup / non-interactive shells (SIG_IGN inherited),
	// so a bounded grace period with SIGKILL escalation is the safe default.
	CloseTimeout int `json:"close_timeout" flagName:"close-timeout" flagDescribe:"Time in seconds to force kill process after the session is closed" default:"3"`

	// Term is the value of the TERM environment variable.
	// Empty means "xterm-256color".
	Term string `json:"term"`
}

// Option is a functional option of Terminal construction.
type Option func(*Terminal)

// WithCloseSignal sets the signal sent to the process on Close.
func WithCloseSignal(signal syscall.Signal) Option {
	return func(t *Terminal) {
		t.closeSignal = signal
	}
}

// WithCloseTimeout sets the grace period before SIGKILL on Close.
// A negative duration disables the kill escalation.
func WithCloseTimeout(timeout time.Duration) Option {
	return func(t *Terminal) {
		t.closeTimeout = timeout
	}
}

// WithEnv adds extra environment variables to the process.
func WithEnv(env []string) Option {
	return func(t *Terminal) {
		t.env = append(t.env, env...)
	}
}

// WithTerm sets the TERM environment variable of the process.
// An empty value keeps the default ("xterm-256color").
func WithTerm(term string) Option {
	return func(t *Terminal) {
		if term != "" {
			t.env = append(t.env, "TERM="+term)
		}
	}
}

// WithInitialSize sets the initial PTY size. Non-positive values are ignored.
func WithInitialSize(cols, rows int) Option {
	return func(t *Terminal) {
		if cols > 0 && rows > 0 {
			t.size = pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}
		}
	}
}
