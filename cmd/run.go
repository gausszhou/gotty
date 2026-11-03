package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

// runRequest is the POST /api/sessions body. Empty fields are omitted so the
// server keeps its defaults (e.g. no command = the serve default shell).
type runRequest struct {
	ID         string   `json:"id,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	Width      int      `json:"width,omitempty"`
	Height     int      `json:"height,omitempty"`
	Env        []string `json:"env,omitempty"`
	Cwd        string   `json:"cwd,omitempty"`
	Term       string   `json:"term,omitempty"`
	Scrollback *int     `json:"scrollback,omitempty"`
}

// buildAgentRunCmd creates `gotty run`.
//
// It is the missing first step of every agent workflow: all the other agent
// subcommands need a session id, but the server deliberately keeps no list
// (the client manifest is the source of truth), so the caller must create a
// session and remember its id. Like `tu run`, the id goes to stdout by itself
// so the common shape works:
//
//	ID=$(gotty run --width 120 --height 40 -- ./my-tui)
//
// `--json` prints the full session record (state/pid/created_at/…) instead.
func buildAgentRunCmd() *cobra.Command {
	var (
		server     string
		jsonOut    bool
		timeout    time.Duration
		id         string
		width      int
		height     int
		env        []string
		cwd        string
		term       string
		scrollback int
	)

	cmd := &cobra.Command{
		Use:   "run [flags] -- <command> [args...]",
		Short: "Create a session and print its id",
		Long: "Create a session through the REST API and print its id — the\n" +
			"starting point for every other agent command. With no command, the\n" +
			"server's default command (the shell started by `gotty serve`) is used.\n\n" +
			"Put `--` before the command so its own flags (e.g. `--help`) are not\n" +
			"parsed by gotty:\n\n" +
			"  ID=$(gotty run --width 120 --height 40 -- ./my-tui)\n" +
			"  gotty screen --session \"$ID\"\n\n" +
			"Output: the bare id on stdout (`--json` = the full session record).\n" +
			"An --id makes the call idempotent: alive → same session, recorded →\n" +
			"resurrected, unknown → created.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if width < 0 || height < 0 {
				return fmt.Errorf("--width and --height must be >= 0")
			}
			if (width > 0) != (height > 0) {
				return fmt.Errorf("--width and --height must be set together")
			}
			if scrollback < -1 {
				return fmt.Errorf("--scrollback must be >= 0")
			}

			req := runRequest{
				ID:     id,
				Width:  width,
				Height: height,
				Env:    env,
				Cwd:    cwd,
				Term:   term,
			}
			if scrollback >= 0 {
				req.Scrollback = &scrollback
			}
			if len(args) > 0 {
				req.Command = args[0]
				req.Args = args[1:]
			}

			client := &agentClient{
				base:      resolveServerURL(server),
				forceJSON: jsonOut,
				client:    &http.Client{Timeout: timeout},
				out:       cmd.OutOrStdout(),
			}
			data, err := client.post(cmd.Context(), "/api/sessions", req)
			if err != nil {
				return err
			}

			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(data, &created); err != nil {
				return fmt.Errorf("invalid create-session response: %w", err)
			}
			if created.ID == "" {
				return fmt.Errorf("server returned no session id")
			}

			out := cmd.OutOrStdout()
			if jsonOut {
				_, err = fmt.Fprintf(out, "%s\n", jsonCompact(data))
				return err
			}
			_, err = fmt.Fprintln(out, created.ID)
			return err
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&server, "server", "",
		"server base URL ($GOTTY_SERVER, else $GOTTY_ADDRESS:$GOTTY_PORT, else http://127.0.0.1:9049)")
	flags.BoolVar(&jsonOut, "json", false, "print the full session record as one line of JSON")
	flags.DurationVar(&timeout, "timeout", 30*time.Second, "HTTP request timeout")
	flags.StringVar(&id, "id", "", "client-chosen session id (16 base36 chars; makes the call idempotent)")
	flags.IntVar(&width, "width", 0, "initial columns (0 = server default; needs --height)")
	flags.IntVar(&height, "height", 0, "initial rows (0 = server default; needs --width)")
	flags.StringArrayVar(&env, "env", nil, "extra environment variable K=V for the session (repeatable)")
	flags.StringVar(&cwd, "cwd", "", "working directory for the session (default: the server's)")
	flags.StringVar(&term, "term", "", "TERM value for the session (empty = the server's --term)")
	flags.IntVar(&scrollback, "scrollback", -1, "history lines kept for this session (0 disables; -1 = server default)")
	// Everything after the command belongs to the command: `gotty run ./tui --help`
	// must not be parsed as gotty's own flag.
	flags.SetInterspersed(false)

	return cmd
}
