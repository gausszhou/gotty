package cmd

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

// buildAgentKillCmd creates `gotty kill` — the DELETE counterpart of
// `gotty run`, aligned with `tu kill`.
//
// Destroying is not the same as signalling: DELETE removes the session from
// the registry and closes its PTY, so a test suite can clean up in one step
// instead of remembering which signal to send and then waiting for the idle
// sweeper. `gotty run` + `gotty kill` bracket a test case.
func buildAgentKillCmd() *cobra.Command {
	var f agentFlags
	cmd := &cobra.Command{
		Use:   "kill",
		Short: "Destroy a session and stop its process",
		Long: "Destroy a session: stop its process and remove it from the server's\n" +
			"registry (DELETE /api/sessions/{id}). This is the counterpart of\n" +
			"`gotty run` — use it to clean up a test session in one step.\n\n" +
			"Already-exited sessions are swept about a second after they exit, so\n" +
			"a late kill may answer 404; treat that as already-clean.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			c.out = cmd.OutOrStdout()
			if _, err := c.do(cmd.Context(), http.MethodDelete, c.sessionPath(""), nil, nil); err != nil {
				return err
			}
			return c.emit(
				map[string]interface{}{"session_id": c.session, "destroyed": true},
				fmt.Sprintf("destroyed %s\n", c.session),
			)
		},
	}
	addAgentFlags(cmd, &f)
	return cmd
}
