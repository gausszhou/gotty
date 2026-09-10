//go:build windows

package capture

// shellSyntaxHint names the portable way to get shell syntax on this platform.
// The shared message used to suggest `sh -c "..."` unconditionally, which points
// at a program that does not exist on Windows: `cmd /c` is the real equivalent
// (see terminal's fallbackShell for the same reasoning about the default shell).
const shellSyntaxHint = `cmd /c "..."`
