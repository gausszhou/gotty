//go:build !windows

package capture

// shellSyntaxHint names the portable way to get shell syntax on this platform.
// It is interpolated into the "command not found" error so the suggestion is
// actually runnable by the user who just hit it.
const shellSyntaxHint = `sh -c "..."`
