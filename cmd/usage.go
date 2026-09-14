package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// buildUsageCmd creates `gotty usage`.
//
// This is deliberately NOT `--help`: `--help` is exhaustive and meant for a
// human, while `usage` is a <1000-token cheat sheet an LLM can read once and
// then act on — the same division `tu usage` makes. Keep it short, keep it
// copy-pasteable, and keep every line true.
func buildUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "usage",
		Short: "Print the agent cheat sheet (short, for LLMs)",
		Long:  "A condensed reference for driving a GoTTY session from an AI agent.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), agentUsageText)
			return err
		},
	}
}

// agentUsageText is the cheat sheet. Every command in it is a thin wrapper
// over the REST API, so anything here can also be done with curl.
const agentUsageText = `gotty — agent cheat sheet (see AGENTS.md for the full contract)

SETUP
  gotty serve --permit-write            # start the server (default :9049)
  Every command below needs a session id: --session $ID (or $GOTTY_SESSION).
  Point at another host with --server URL (or $GOTTY_SERVER).
  Output: human text on a TTY, ONE line of JSON otherwise or with --json.

READ
  gotty screen --session $ID                      # current screen (text)
  gotty screen --format json --session $ID        # styled cells + cursor
  gotty screen --format png --out s.png --session $ID
  gotty scrollback --lines 200 --session $ID      # history (0 = all)
  gotty wait --regex 'Total: [0-9]+' --session $ID
  gotty wait --quiet-ms 500 --session $ID         # until output stops
  gotty status --session $ID                      # state, exit code, size
  gotty monitor --fps 10 --session $ID            # read-only live diff stream

WRITE
  gotty type 'ls -la\n' --session $ID             # raw bytes (a \n runs it)
  gotty press Escape : w q Enter --session $ID    # named keys — leave vim
  gotty press Ctrl+C --session $ID
  gotty paste "$(cat patch.diff)" --session $ID   # ONE paste, not line by line
  gotty mouse click --on-text OK --session $ID    # click by LABEL, not coords
  gotty mouse click --col 12 --row 3 --session $ID
  gotty mouse scroll down --amount 5 --session $ID
  gotty mouse drag --col 1 --row 1 --to-col 9 --to-row 9 --session $ID
  gotty cursor --session $ID                      # mouse-mode state
  gotty resize --cols 120 --rows 40 --session $ID
  gotty signal SIGINT --session $ID

KEYS  (full table: gotty press --help, or internal/keys)
  enter tab escape backspace space  up down left right
  home end pgup pgdn ins del  f1..f12
  Ctrl+<char>  Alt+<key>  Shift+<key>   e.g. Ctrl+C  Alt+Enter  Shift+Tab
  Anything else is one printable character, e.g. press a b c
  An unknown name is an error listing the accepted keys.

GOTCHAS
  - Multi-line text: use "paste", never "type" — a newline in typed text
    makes the shell execute each line.
  - paste only wraps in bracketed-paste markers when the program enabled
    mode 2004 (check "gotty cursor"); --force wraps regardless.
  - Mouse events on a program that enabled no mouse mode return 409.
    --force sends the bytes anyway.
  - --permit-write=false makes type/press/paste/mouse return 403.
  - --mirror=false makes screen/wait/mouse/cursor return 503.
  - wait times out (5s by default) instead of hanging; check "timed_out".
  - monitor never attaches, so it cannot steal the session from a human.

HTTP (same API, if you prefer curl)
  GET  /api/sessions/{id}/screen?format=text|json|png[&part=scrollback&lines=N]
  POST /api/sessions/{id}/wait    {"regex":"…","timeout_ms":5000,"quiet_ms":0}
  POST /api/sessions/{id}/keys    {"input":"…"} | {"keys":["Escape"]} | {"paste":"…"}
  POST /api/sessions/{id}/mouse   {"action":"click","on_text":"OK"}
  GET  /api/sessions/{id}/mouse   mouse-mode state
  GET  /ws?session_id={id}&mode=mirror   read-only diff stream
`
