//go:build windows

package cmd

import "os"

// fallbackShell is the shell a session runs when `gotty serve` was started
// without a command. Windows has no login-shell convention, so COMSPEC
// (cmd.exe) is the equivalent of $SHELL, and /bin/sh — what the unix path
// falls back to — does not exist on Windows at all: that fallback made every
// session fail to start even once PTY support existed.
func fallbackShell() string {
	// Git Bash / MSYS 用户会把 SHELL 指向 bash.exe:优先尊重显式配置。
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		return comspec
	}
	return `C:\Windows\System32\cmd.exe`
}
