//go:build !unix

package terminal

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// rawSlave is a no-op on platforms without POSIX termios (Windows): the
// capture query-answer path is Linux/macOS territory anyway.
func rawSlave(_ *os.File) error { return nil }

// startRawPTY falls back to the plain pty.Start paths on non-unix
// platforms: Windows has no termios, and creack/pty's Windows backend
// (conpty) manages the console itself.
func startRawPTY(cmd *exec.Cmd, size pty.Winsize) (*os.File, error) {
	if size.Cols > 0 && size.Rows > 0 {
		return pty.StartWithSize(cmd, &size)
	}
	return pty.Start(cmd)
}

// signalProcessGroup on Windows degrades to a plain process signal, then
// a kill: there is no process-group signal, and the graceful-close
// escalation loop in Close() covers the rest.
func signalProcessGroup(proc *os.Process, sig syscall.Signal) error {
	if err := proc.Signal(sig); err == nil {
		return nil
	}
	return proc.Kill()
}
