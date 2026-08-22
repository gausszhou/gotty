package terminal

import "errors"

// Errors returned by the terminal package.
var (
	// ErrTerminalClosed indicates the terminal process has exited
	// and its PTY has been closed.
	ErrTerminalClosed = errors.New("terminal closed")

	// ErrInvalidMessage indicates a malformed protocol frame.
	ErrInvalidMessage = errors.New("invalid protocol message")
)
