//go:build windows

package update

// Platform guidance woven into self-update messages. The shared text used to
// hand out a POSIX pipeline and a systemd command, neither of which exists on
// Windows (see README: `install.ps1` is the Windows counterpart).
const (
	// installScriptHint is how to (re)install/upgrade by hand.
	installScriptHint = `powershell -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/gausszhou/gotty/master/scripts/install.ps1 | iex"`
	// restartHint says how the new binary takes effect.
	restartHint = "restart the service (stop and start gotty again) to take effect"
)
