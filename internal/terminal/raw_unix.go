//go:build unix

package terminal

import (
	"os"

	"golang.org/x/sys/unix"
)

// rawSlave puts the PTY slave (the process's terminal) into raw mode:
// no echo, no canonical line buffering, no ISIG, no output post-processing.
// See WithRawMode for why the capture engine needs this. tty is the slave
// *os.File returned by pty.Open.
func rawSlave(tty *os.File) error {
	t, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
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
	return unix.IoctlSetTermios(int(tty.Fd()), unix.TCSETS, t)
}
