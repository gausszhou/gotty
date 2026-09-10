//go:build !windows

package cmd

import "os"

// fallbackShell is the shell a session runs when `gotty serve` was started
// without a command: the login shell from $SHELL, falling back to /bin/sh.
func fallbackShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}
