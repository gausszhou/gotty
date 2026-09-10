//go:build !windows

package update

// Platform guidance woven into self-update messages. Keeping it behind a build
// tag means the Windows build never tells users to run `sh` or `systemctl`.
const (
	// installScriptHint is how to (re)install/upgrade by hand.
	installScriptHint = "curl -fsSL https://raw.githubusercontent.com/gausszhou/gotty/master/scripts/install.sh | sh"
	// restartHint says how the new binary takes effect.
	restartHint = "restart the service (systemctl --user restart gotty) to take effect"
)
