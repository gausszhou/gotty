//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package terminal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// rawSlave puts the PTY slave (the process's terminal) into raw mode:
// no echo, no canonical line buffering, no ISIG, no output post-processing.
// See WithRawMode for why the capture engine needs this. tty is the slave
// *os.File returned by pty.Open. BSD-family termios API (TIOCGETA/TIOCSETA,
// int req; Iflag/Oflag/Cflag/Lflag are uint64 here, unlike Linux).
func rawSlave(tty *os.File) error {
	t, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TIOCGETA)
	if err != nil {
		return err
	}
	// 经典 raw(对齐 cfmakeraw 的位面):
	// 关闭输入转换、回显与规范模式、信号键、输出后处理;8 位无校验。
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(int(tty.Fd()), unix.TIOCSETA, t)
}

// startRawPTY opens a PTY, sets the slave to raw mode, then starts cmd
// against it as the controlling terminal (Setsid + Setctty). It cannot use
// pty.StartWithSize's name resolution: master.Name() only reports the
// generic ptmx device, and reopening it every time allocates useless new
// PTY pairs.
func startRawPTY(cmd *exec.Cmd, size pty.Winsize) (*os.File, error) {
	rawPTMX, rawTTY, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to open raw pty: %w", err)
	}
	if size.Cols > 0 && size.Rows > 0 {
		_ = pty.Setsize(rawPTMX, &size)
	}
	if err := rawSlave(rawTTY); err != nil {
		rawTTY.Close()
		rawPTMX.Close()
		return nil, fmt.Errorf("failed to set raw mode on terminal: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = rawTTY, rawTTY, rawTTY
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		rawTTY.Close()
		rawPTMX.Close()
		return nil, err
	}
	// 子进程已继承 slave fd;父进程侧关闭,避免干扰 termios 引用计数。
	rawTTY.Close()
	return rawPTMX, nil
}

// signalProcessGroup sends sig to -pid; with Setsid the command is a
// session leader so this addresses the whole group (sh -c children).
// A dead group (ESRCH) falls back to signaling the process itself.
func signalProcessGroup(proc *os.Process, sig syscall.Signal) error {
	if err := syscall.Kill(-proc.Pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return proc.Signal(sig)
		}
		return err
	}
	return nil
}
