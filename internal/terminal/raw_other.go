//go:build !unix

package terminal

import "os"

// rawSlave is a no-op on platforms without POSIX termios (Windows): the
// capture query-answer path is Linux/macOS territory anyway.
func rawSlave(_ *os.File) error { return nil }
