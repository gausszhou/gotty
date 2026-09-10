package terminal

import (
	"io"
	"os"
	"syscall"
)

// ptyProcess is a PTY master plus the child process attached to it, taken as
// one unit because the two halves never come from different implementations:
// unix pairs a creack/pty master with an exec.Cmd (termios, process groups),
// Windows pairs a ConPTY pseudoconsole with a raw CreateProcess handle
// (no signals, handle-based I/O). The Terminal type owns the locking, the
// state machine and the close escalation; this interface owns the platform
// differences.
//
// Implementations live in process_unix.go and process_windows.go.
type ptyProcess interface {
	io.ReadWriteCloser

	// Resize sets the console size in character cells.
	Resize(cols, rows uint16) error

	// PID is the pid of the process attached to the PTY.
	PID() int

	// Signal asks the process to stop. unix delivers the signal to the
	// whole process group; Windows has no equivalent and degrades to
	// terminating the session (see process_windows.go).
	Signal(sig syscall.Signal) error

	// Kill terminates the process unconditionally.
	Kill() error

	// Wait blocks until the process exits and reports a non-zero exit as
	// *exec.ExitError (capture/driver.go reads the code through
	// errors.As). It is called exactly once, by Terminal's wait goroutine.
	Wait() error

	// releaseAfterExit releases what is only valid while the process runs.
	// It is called once, right after the process has exited and before the
	// exit is published, and it must not truncate output that the reader
	// has not drained yet:
	//
	//   - unix: nothing to do. The master read returns EIO on its own once
	//     the last slave handle is gone, which is what ends the session's
	//     output pump.
	//   - Windows: the pseudoconsole has to be closed, because the console
	//     host holds its own copy of the output pipe's write end — without
	//     closing it the reader never sees the channel break and the pump
	//     would hang forever after the shell exits. Closing the console may
	//     emit a final frame, so the read end stays open for the reader to
	//     drain (and the reader closes it when it sees the break).
	releaseAfterExit()
}

// childWorkDir is the working directory given to session processes: the
// user's home directory rather than the gotty process cwd (a long-running
// service is usually started from a directory the user never meant as a
// shell start point). An empty return value keeps the inherited cwd, which
// is what happens when the home directory cannot be resolved ($HOME unset).
func childWorkDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
