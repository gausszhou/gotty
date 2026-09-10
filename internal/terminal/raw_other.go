//go:build !unix && !windows

package terminal

import (
	"fmt"
	"os"
	"syscall"
)

// This file covers the platforms that have neither termios (unix) nor
// ConPTY (Windows), i.e. everything creack/pty itself cannot drive. gotty
// does not support them: they build, and starting a session fails with a
// clear error instead of a puzzling one from the PTY library.
//
// Windows used to live here as well and wrongly assumed that creack/pty
// falls back to a ConPTY backend — it does not (its start_windows.go returns
// ErrUnsupported), so every session failed there. Windows now has a real
// implementation in process_windows.go.

// startProcess has no PTY to offer on these platforms.
func startProcess(
	_ string,
	_, _ []string,
	_ string,
	_, _ uint16,
	_ bool,
) (ptyProcess, error) {
	return nil, fmt.Errorf("PTYs are not supported on this platform")
}

// rawSlave is a no-op without POSIX termios.
func rawSlave(_ *os.File) error { return nil }

// signalProcessGroup degrades to a plain process signal, then a kill: there
// is no process group to address, and the graceful-close escalation loop in
// Close() covers the rest.
func signalProcessGroup(proc *os.Process, sig syscall.Signal) error {
	if err := proc.Signal(sig); err == nil {
		return nil
	}
	return proc.Kill()
}
