//go:build unix

package terminal

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
)

// unixProcess is a creack/pty master plus the exec.Cmd started on it.
type unixProcess struct {
	cmd    *exec.Cmd
	master *os.File
}

// startProcess starts command on a new PTY (see ptyProcess).
//
// The raw-mode path keeps its own PTY handling (startRawPTY): it has to set
// the slave to raw mode before the process starts, which pty.Start cannot do.
func startProcess(
	command string,
	args, env []string,
	dir string,
	cols, rows uint16,
	rawMode bool,
) (ptyProcess, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = env
	cmd.Dir = dir

	size := pty.Winsize{Rows: rows, Cols: cols}
	var master *os.File
	var err error
	switch {
	case rawMode:
		// 原始模式:自管 PTY(见 startRawPTY)。
		master, err = startRawPTY(cmd, size)
	case size.Cols > 0 && size.Rows > 0:
		master, err = pty.StartWithSize(cmd, &size)
	default:
		// 未指定尺寸:交回平台默认(继承启动终端的尺寸)。
		master, err = pty.Start(cmd)
	}
	if err != nil {
		return nil, err
	}

	return &unixProcess{cmd: cmd, master: master}, nil
}

func (p *unixProcess) Read(b []byte) (int, error)  { return p.master.Read(b) }
func (p *unixProcess) Write(b []byte) (int, error) { return p.master.Write(b) }
func (p *unixProcess) Close() error                { return p.master.Close() }

func (p *unixProcess) Resize(cols, rows uint16) error {
	size := pty.Winsize{Rows: rows, Cols: cols}
	return pty.Setsize(p.master, &size)
}

func (p *unixProcess) PID() int { return p.cmd.Process.Pid }

func (p *unixProcess) Signal(sig syscall.Signal) error {
	return signalProcessGroup(p.cmd.Process, sig)
}

func (p *unixProcess) Kill() error {
	return signalProcessGroup(p.cmd.Process, syscall.SIGKILL)
}

func (p *unixProcess) Wait() error { return p.cmd.Wait() }

// releaseAfterExit is a no-op on unix: the output pump ends by itself when
// the master read returns EIO. The master is deliberately left open so the
// last buffered bytes are still readable (see the comment in New).
func (p *unixProcess) releaseAfterExit() {}
