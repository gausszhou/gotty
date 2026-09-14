package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/gausszhou/gotty/internal/terminal"
)

// Agent-facing CLI (0004 §2.6, 0005 §2.1/§2.5).
//
// Every subcommand here is a thin wrapper over the agent REST API — no
// logic of its own, no local terminal: the point is that an LLM agent can
// drive a running session with one process invocation and parse the result.
//
// Output contract (aligned with `tu`):
//   - stdout is a TTY  → human-readable text;
//   - stdout is not a TTY, or --json is set → ONE line of JSON per result;
//   - errors are {"error": "..."} plus a non-zero exit code.
//
// `screen --format text` is the documented exception: it always prints the
// raw screen text, because piping it into grep/jq is the whole point.

// buildAgentCommands returns the agent subcommands. It is called twice — once
// for the root command (`gotty screen …`) and once for the `gotty agent …`
// group — so both spellings work.
func buildAgentCommands() []*cobra.Command {
	return []*cobra.Command{
		buildAgentScreenCmd(),
		buildAgentScrollbackCmd(),
		buildAgentWaitCmd(),
		buildAgentTypeCmd(),
		buildAgentPressCmd(),
		buildAgentPasteCmd(),
		buildAgentMouseCmd(),
		buildAgentCursorCmd(),
		buildAgentResizeCmd(),
		buildAgentSignalCmd(),
		buildAgentStatusCmd(),
		buildAgentMonitorCmd(),
	}
}

// buildAgentGroupCmd creates `gotty agent`, the explicit grouping of the
// same commands (useful when a name collides, and self-documenting).
func buildAgentGroupCmd() *cobra.Command {
	group := &cobra.Command{
		Use:   "agent",
		Short: "Drive a running session from scripts or an AI agent",
		Long: "Thin wrappers over the agent REST API. Every subcommand is also\n" +
			"available at the top level (`gotty screen …` == `gotty agent screen …`).\n\n" +
			"Point them at a server with --server / $GOTTY_SERVER, and pick a\n" +
			"session with --session / $GOTTY_SESSION (required: the server keeps\n" +
			"no list of sessions).\n\n" +
			"Output: human-readable on a TTY, one line of JSON otherwise or with\n" +
			"--json. See `gotty usage` for the agent cheat sheet.",
	}
	group.AddCommand(buildAgentCommands()...)
	return group
}

// ---- shared flags & client -------------------------------------------------

// agentFlags are the flags every agent subcommand shares.
type agentFlags struct {
	server  string
	session string
	json    bool
	timeout time.Duration
}

// addAgentFlags registers the common flags on a subcommand.
func addAgentFlags(cmd *cobra.Command, f *agentFlags) {
	flags := cmd.Flags()
	flags.StringVar(&f.server, "server", "",
		"server base URL ($GOTTY_SERVER, else $GOTTY_ADDRESS:$GOTTY_PORT, else http://127.0.0.1:9049)")
	flags.StringVar(&f.session, "session", "",
		"session id (required; $GOTTY_SESSION also works)")
	flags.BoolVar(&f.json, "json", false,
		"force single-line JSON output (the default when stdout is not a TTY)")
	flags.DurationVar(&f.timeout, "timeout", 30*time.Second, "HTTP request timeout")
}

// agentClient talks to one running gotty server.
type agentClient struct {
	base      string // e.g. http://127.0.0.1:9049
	session   string
	forceJSON bool
	client    *http.Client
	out       io.Writer
}

// newAgentClient resolves the server/session pair for a subcommand.
func (f *agentFlags) newAgentClient() (*agentClient, error) {
	session := f.session
	if session == "" {
		session = os.Getenv("GOTTY_SESSION")
	}
	if session == "" {
		return nil, errors.New("no session id: pass --session <id> (or set GOTTY_SESSION)")
	}
	return &agentClient{
		base:      resolveServerURL(f.server),
		session:   session,
		forceJSON: f.json,
		client:    &http.Client{Timeout: f.timeout},
		out:       os.Stdout,
	}, nil
}

// resolveServerURL applies the documented precedence:
// --server > $GOTTY_SERVER > $GOTTY_ADDRESS/$GOTTY_PORT > 127.0.0.1:9049.
// A bare host:port gets an http:// scheme; 0.0.0.0 is normalized to
// 127.0.0.1 because it is a listen address, not a dialable one.
func resolveServerURL(flagVal string) string {
	raw := flagVal
	if raw == "" {
		raw = os.Getenv("GOTTY_SERVER")
	}
	if raw == "" {
		addr := os.Getenv("GOTTY_ADDRESS")
		port := os.Getenv("GOTTY_PORT")
		if addr == "" && port == "" {
			return "http://127.0.0.1:9049"
		}
		if addr == "" {
			addr = "127.0.0.1"
		}
		if port == "" {
			port = "9049"
		}
		raw = addr + ":" + port
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Hostname() == "0.0.0.0" {
		u.Host = "127.0.0.1" + portSuffix(u)
	}
	return strings.TrimRight(u.String(), "/")
}

// portSuffix renders the ":port" part of a URL, empty when defaulted.
func portSuffix(u *url.URL) string {
	if u.Port() == "" {
		return ""
	}
	return ":" + u.Port()
}

// ---- HTTP plumbing ---------------------------------------------------------

// do performs one API call and returns the raw body. A non-2xx answer becomes
// an error carrying the server's own message, so `gotty … | jq .error` works.
func (c *agentClient) do(ctx context.Context, method, path string, query url.Values, body interface{}) ([]byte, error) {
	endpoint := c.base + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach gotty at %s: %w", c.base, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, apiError(resp.StatusCode, data)
	}
	return data, nil
}

// apiError turns an error response body into a readable error.
func apiError(status int, body []byte) error {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.Error != "" {
		return fmt.Errorf("%s (HTTP %d)", payload.Error, status)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Errorf("request failed (HTTP %d)", status)
	}
	return fmt.Errorf("%s (HTTP %d)", text, status)
}

// get/post are the two shapes the agent API uses.
func (c *agentClient) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, query, nil)
}

func (c *agentClient) post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, nil, body)
}

// sessionPath builds /api/sessions/{id}/...
func (c *agentClient) sessionPath(suffix string) string {
	return "/api/sessions/" + url.PathEscape(c.session) + suffix
}

// ---- output ---------------------------------------------------------------

// emit prints either the human form or a single JSON line. Structured results
// go through here so that piping into jq always works.
func (c *agentClient) emit(value interface{}, human string) error {
	if c.forceJSON || !isTerminal(os.Stdout) {
		line, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(c.out, "%s\n", line)
		return err
	}
	if human == "" {
		return nil
	}
	_, err := io.WriteString(c.out, human)
	return err
}

// emitRaw writes bytes verbatim (screen text / PNG).
func (c *agentClient) emitRaw(data []byte) error {
	_, err := c.out.Write(data)
	return err
}

// isTerminal reports whether f is a character device (a TTY).
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ---- screen ---------------------------------------------------------------

func buildAgentScreenCmd() *cobra.Command {
	var (
		f        agentFlags
		format   string
		outPath  string
		mouseCur bool
	)
	cmd := &cobra.Command{
		Use:   "screen",
		Short: "Read the rendered screen of a session",
		Long: "Read the session's screen mirror right now.\n\n" +
			"  text (default)  plain screen text — always printed raw, TTY or not\n" +
			"  json            styled cells + cursor (the full snapshot)\n" +
			"  png             native bitmap (--out file.png; --mouse-cursor overlays the virtual mouse)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			switch format {
			case "text", "json", "png":
			default:
				return fmt.Errorf("unknown format %q (want text|json|png)", format)
			}
			query := url.Values{"format": {format}}
			if mouseCur {
				query.Set("mouse_cursor", "1")
			}
			data, err := c.get(cmd.Context(), c.sessionPath("/screen"), query)
			if err != nil {
				return err
			}
			if format == "png" {
				return writeAgentBytes(outPath, data)
			}
			if format == "json" {
				return c.emitRaw(append(jsonCompact(data), '\n'))
			}
			if len(data) == 0 || data[len(data)-1] != '\n' {
				data = append(data, '\n')
			}
			return c.emitRaw(data)
		},
	}
	addAgentFlags(cmd, &f)
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json | png")
	cmd.Flags().StringVar(&outPath, "out", "", "output path for --format png ('-' = stdout)")
	cmd.Flags().BoolVar(&mouseCur, "mouse-cursor", false, "overlay the virtual mouse cursor (--format png only)")
	return cmd
}

// jsonCompact re-encodes a JSON body onto one line.
func jsonCompact(data []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return bytes.TrimRight(data, "\n")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return bytes.TrimRight(data, "\n")
	}
	return out
}

// scrollback (0005 §2.3): history instead of the visible screen.

func buildAgentScrollbackCmd() *cobra.Command {
	var (
		f      agentFlags
		lines  int
		format string
	)
	cmd := &cobra.Command{
		Use:   "scrollback",
		Short: "Read the session's scrollback history",
		Long: "Read what scrolled off the screen. `lines` counts from the end:\n" +
			"--lines 200 gives the most recent 200 lines; omit it (or 0) for everything.\n" +
			"A full-screen program on the alternate screen has no history — that is\n" +
			"reported as an empty result, not an error.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			switch format {
			case "text", "json":
			default:
				return fmt.Errorf("unknown format %q (want text|json)", format)
			}
			query := url.Values{"part": {"scrollback"}, "format": {format}}
			if lines > 0 {
				query.Set("lines", fmt.Sprint(lines))
			}
			data, err := c.get(cmd.Context(), c.sessionPath("/screen"), query)
			if err != nil {
				return err
			}
			if format == "json" {
				return c.emitRaw(append(jsonCompact(data), '\n'))
			}
			if len(data) == 0 || data[len(data)-1] != '\n' {
				data = append(data, '\n')
			}
			return c.emitRaw(data)
		},
	}
	addAgentFlags(cmd, &f)
	cmd.Flags().IntVar(&lines, "lines", 0, "most recent N lines (0 = everything)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	return cmd
}

// ---- wait -----------------------------------------------------------------

func buildAgentWaitCmd() *cobra.Command {
	var (
		f         agentFlags
		regex     string
		timeoutMS int
		quietMS   int
		format    string
	)
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Block until the screen matches a regex or goes quiet",
		Long: "Long-poll the session until the screen matches --regex, until output\n" +
			"has been silent for --quiet-ms, or until --timeout-ms elapses. The\n" +
			"screen at that moment is returned either way, so a timeout is not an\n" +
			"error — check the `matched` / `quiet` / `timed_out` flags.\n\n" +
			"The CLI default is 5s (aligned with `tu`); the server caps any wait at 5 minutes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			if regex == "" && quietMS <= 0 {
				return errors.New("need --regex or --quiet-ms")
			}
			switch format {
			case "text", "json":
			default:
				return fmt.Errorf("unknown format %q (want text|json)", format)
			}
			body := map[string]interface{}{
				"regex":      regex,
				"timeout_ms": timeoutMS,
				"quiet_ms":   quietMS,
			}
			data, err := c.post(cmd.Context(), c.sessionPath("/wait"), body)
			if err != nil {
				return err
			}
			if format == "json" {
				return c.emitRaw(append(jsonCompact(data), '\n'))
			}
			// Human form: the screen text plus the outcome line.
			var res struct {
				Text     string `json:"text"`
				Matched  bool   `json:"matched"`
				Quiet    bool   `json:"quiet"`
				TimedOut bool   `json:"timed_out"`
			}
			if err := json.Unmarshal(data, &res); err != nil {
				return c.emitRaw(append(jsonCompact(data), '\n'))
			}
			outcome := "timeout"
			switch {
			case res.Matched:
				outcome = "matched"
			case res.Quiet:
				outcome = "quiet"
			}
			if !isTerminal(os.Stdout) || c.forceJSON {
				return c.emitRaw(append(jsonCompact(data), '\n'))
			}
			if _, err := io.WriteString(c.out, res.Text); err != nil {
				return err
			}
			_, err = fmt.Fprintf(c.out, "\n[wait: %s]\n", outcome)
			return err
		},
	}
	addAgentFlags(cmd, &f)
	cmd.Flags().StringVar(&regex, "regex", "", "wait until the screen matches this regex")
	cmd.Flags().IntVar(&timeoutMS, "timeout-ms", 5000, "overall timeout in ms (server caps at 300000)")
	cmd.Flags().IntVar(&quietMS, "quiet-ms", 0, "wait until output has been silent this long (ms)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text | json")
	return cmd
}

// ---- input: type / press / paste ------------------------------------------

func buildAgentTypeCmd() *cobra.Command {
	var (
		f        agentFlags
		encoding string
	)
	cmd := &cobra.Command{
		Use:   "type <text>",
		Short: "Send literal text/bytes to the session",
		Long: "Write raw bytes into the PTY — the low-level path. Prefer `press`\n" +
			"for named keys and `paste` for multi-line text: a bare newline in\n" +
			"typed text makes a shell execute the line.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"input": args[0], "encoding": encoding}
			data, err := c.post(cmd.Context(), c.sessionPath("/keys"), body)
			if err != nil {
				return err
			}
			return c.emit(json.RawMessage(jsonCompact(data)), "")
		},
	}
	addAgentFlags(cmd, &f)
	cmd.Flags().StringVar(&encoding, "encoding", "text", "payload encoding: text | base64")
	return cmd
}

func buildAgentPressCmd() *cobra.Command {
	var f agentFlags
	cmd := &cobra.Command{
		Use:   "press <key> [key...]",
		Short: "Press named keys (Escape, :, w, q, Enter, Ctrl+C, F5, …)",
		Long: "Send a key sequence by name — no terminfo bytes to memorize.\n" +
			"Modifiers: Ctrl+ / Alt+ / Shift+ (e.g. Ctrl+C, Alt+Enter, Shift+Tab).\n" +
			"An unknown name is an error that lists the accepted keys.\n\n" +
			"  gotty press Escape : w q Enter --session $ID   # leave vim",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"keys": args}
			data, err := c.post(cmd.Context(), c.sessionPath("/keys"), body)
			if err != nil {
				return err
			}
			return c.emit(json.RawMessage(jsonCompact(data)), "")
		},
	}
	addAgentFlags(cmd, &f)
	return cmd
}

func buildAgentPasteCmd() *cobra.Command {
	var (
		f     agentFlags
		force bool
	)
	cmd := &cobra.Command{
		Use:   "paste <text>",
		Short: "Paste a text block as ONE paste",
		Long: "Deliver a whole block as a single paste. When the program enabled\n" +
			"bracketed paste (mode 2004) the text is wrapped in the standard\n" +
			"markers, so a shell does not execute it line by line and vim does\n" +
			"not auto-indent it. Without mode 2004 the text is sent as-is —\n" +
			"--force wraps it anyway (only if you know the program wants it).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			body := map[string]interface{}{"paste": args[0], "force_paste": force}
			data, err := c.post(cmd.Context(), c.sessionPath("/keys"), body)
			if err != nil {
				return err
			}
			return c.emit(json.RawMessage(jsonCompact(data)), "")
		},
	}
	addAgentFlags(cmd, &f)
	cmd.Flags().BoolVar(&force, "force", false,
		"wrap in bracketed-paste markers even when the program did not enable mode 2004")
	return cmd
}

// ---- mouse ----------------------------------------------------------------

// mouseFlags are the targeting/modifier flags shared by the mouse actions.
type mouseFlags struct {
	col, row        int
	toCol, toRow    int
	onText, onRe    string
	matchIndex      int
	button          string
	clicks, amount  int
	shift, alt, ctl bool
	force           bool
}

func (m mouseFlags) request(action string) map[string]interface{} {
	body := map[string]interface{}{"action": action}
	if m.button != "" {
		body["button"] = m.button
	}
	if m.col >= 0 && m.row >= 0 {
		body["col"], body["row"] = m.col, m.row
	}
	if m.toCol >= 0 && m.toRow >= 0 {
		body["to_col"], body["to_row"] = m.toCol, m.toRow
	}
	if m.onText != "" {
		body["on_text"] = m.onText
	}
	if m.onRe != "" {
		body["on_regex"] = m.onRe
	}
	if m.matchIndex != 0 {
		body["match_index"] = m.matchIndex
	}
	if m.clicks > 0 {
		body["clicks"] = m.clicks
	}
	if m.amount > 0 {
		body["amount"] = m.amount
	}
	if m.shift || m.alt || m.ctl {
		body["mods"] = map[string]bool{"shift": m.shift, "alt": m.alt, "ctrl": m.ctl}
	}
	if m.force {
		body["force"] = true
	}
	return body
}

// addMouseFlags registers the mouse targeting flags; coordinates default to
// -1 (unset) so an omitted flag is distinguishable from "cell 0".
func addMouseFlags(flags *mouseFlags, set *pflag.FlagSet, withTarget, withDrag, withClicks, withAmount bool) {
	if withTarget {
		set.IntVar(&flags.col, "col", -1, "target column (0-based)")
		set.IntVar(&flags.row, "row", -1, "target row (0-based)")
		set.StringVar(&flags.onText, "on-text", "", "click the cell containing this text instead of coordinates")
		set.StringVar(&flags.onRe, "on-regex", "", "click the cell matching this regex instead of coordinates")
		set.IntVar(&flags.matchIndex, "match-index", 0, "which match to use when there are several (0-based)")
	}
	if withDrag {
		set.IntVar(&flags.toCol, "to-col", -1, "drag destination column (0-based)")
		set.IntVar(&flags.toRow, "to-row", -1, "drag destination row (0-based)")
	}
	if withClicks {
		set.StringVar(&flags.button, "button", "left", "button: left | middle | right")
		set.IntVar(&flags.clicks, "clicks", 1, "click count (2 = double click)")
	}
	if withAmount {
		set.IntVar(&flags.amount, "amount", 1, "how many wheel steps")
	}
	set.BoolVar(&flags.shift, "shift", false, "hold Shift")
	set.BoolVar(&flags.alt, "alt", false, "hold Alt")
	set.BoolVar(&flags.ctl, "ctrl", false, "hold Ctrl")
	set.BoolVar(&flags.force, "force", false,
		"send SGR bytes even when the program enabled no mouse mode")
}

func buildAgentMouseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mouse <click|down|up|move|drag|scroll>",
		Short: "Drive the mouse in a session",
		Long: "Click, drag and scroll a running TUI. Target a cell by coordinates\n" +
			"or by what it says (--on-text / --on-regex) — the latter is what an\n" +
			"agent should use, since it survives layout changes.\n\n" +
			"Programs that never enabled mouse reporting reject the event with 409;\n" +
			"--force sends the bytes anyway.",
	}
	cmd.AddCommand(
		mouseActionCmd("click", "Click at a cell", true, false, true, false),
		mouseActionCmd("down", "Press and hold a button", true, false, true, false),
		mouseActionCmd("up", "Release a held button", true, false, true, false),
		mouseActionCmd("move", "Move the virtual mouse (no button)", true, false, false, false),
		mouseActionCmd("drag", "Drag from one cell to another", true, true, false, false),
		mouseScrollCmd(),
	)
	return cmd
}

// mouseActionCmd builds one mouse action subcommand; the flags offered depend
// on what the action supports (a drag needs a destination, a move has no button).
func mouseActionCmd(action, short string, withTarget, withDrag, withClicks, withAmount bool) *cobra.Command {
	var (
		f agentFlags
		m mouseFlags
	)
	cmd := &cobra.Command{
		Use:   action,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			data, err := c.post(cmd.Context(), c.sessionPath("/mouse"), m.request(action))
			if err != nil {
				return err
			}
			var res struct {
				Col          int    `json:"col"`
				Row          int    `json:"row"`
				Written      int    `json:"written"`
				Mode         string `json:"mode"`
				ResolvedFrom string `json:"resolved_from"`
			}
			if err := json.Unmarshal(data, &res); err != nil {
				return c.emit(json.RawMessage(jsonCompact(data)), "")
			}
			return c.emit(json.RawMessage(jsonCompact(data)),
				fmt.Sprintf("%s at %d,%d (%s, %d bytes)\n", action, res.Col, res.Row, res.ResolvedFrom, res.Written))
		},
	}
	addAgentFlags(cmd, &f)
	addMouseFlags(&m, cmd.Flags(), withTarget, withDrag, withClicks, withAmount)
	return cmd
}

// mouseScrollCmd builds `mouse scroll up|down|left|right`.
func mouseScrollCmd() *cobra.Command {
	var (
		f agentFlags
		m mouseFlags
	)
	cmd := &cobra.Command{
		Use:   "scroll <up|down|left|right>",
		Short: "Scroll the mouse wheel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			dir := strings.ToLower(args[0])
			buttons := map[string]string{
				"up": "wheelup", "down": "wheeldown",
				"left": "wheelleft", "right": "wheelright",
			}
			button, ok := buttons[dir]
			if !ok {
				return fmt.Errorf("unknown direction %q (want up|down|left|right)", args[0])
			}
			m.button = button
			if m.amount <= 0 {
				m.amount = 1
			}
			data, err := c.post(cmd.Context(), c.sessionPath("/mouse"), m.request("scroll"))
			if err != nil {
				return err
			}
			return c.emit(json.RawMessage(jsonCompact(data)),
				fmt.Sprintf("scroll %s x%d\n", dir, m.amount))
		},
	}
	addAgentFlags(cmd, &f)
	addMouseFlags(&m, cmd.Flags(), true, false, false, true)
	return cmd
}

func buildAgentCursorCmd() *cobra.Command {
	var f agentFlags
	cmd := &cobra.Command{
		Use:   "cursor",
		Short: "Show the virtual mouse cursor and mouse-mode state",
		Long: "Reports whether the program enabled mouse reporting, which encoding\n" +
			"was negotiated (sgr/x10), whether bracketed paste is on, where the\n" +
			"virtual cursor is and which buttons are held.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			data, err := c.get(cmd.Context(), c.sessionPath("/mouse"), nil)
			if err != nil {
				return err
			}
			var st struct {
				Enabled        bool   `json:"enabled"`
				Mode           string `json:"mode"`
				BracketedPaste bool   `json:"bracketed_paste"`
				CursorCol      int    `json:"cursor_col"`
				CursorRow      int    `json:"cursor_row"`
				LastEvent      string `json:"last_event"`
			}
			human := ""
			if err := json.Unmarshal(data, &st); err == nil {
				human = fmt.Sprintf("mouse=%v mode=%s bracketed_paste=%v cursor=%d,%d last=%s\n",
					st.Enabled, orDefault(st.Mode, "-"), st.BracketedPaste,
					st.CursorCol, st.CursorRow, orDefault(st.LastEvent, "-"))
			}
			return c.emit(json.RawMessage(jsonCompact(data)), human)
		},
	}
	addAgentFlags(cmd, &f)
	return cmd
}

// ---- session control ------------------------------------------------------

func buildAgentResizeCmd() *cobra.Command {
	var (
		f    agentFlags
		cols int
		rows int
	)
	cmd := &cobra.Command{
		Use:   "resize",
		Short: "Resize the session terminal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			if cols <= 0 || rows <= 0 {
				return errors.New("--cols and --rows must be positive")
			}
			data, err := c.post(cmd.Context(), c.sessionPath("/resize"),
				map[string]int{"width": cols, "height": rows})
			if err != nil {
				return err
			}
			return c.emit(json.RawMessage(jsonCompact(data)), "")
		},
	}
	addAgentFlags(cmd, &f)
	cmd.Flags().IntVar(&cols, "cols", 0, "columns")
	cmd.Flags().IntVar(&rows, "rows", 0, "rows")
	return cmd
}

func buildAgentSignalCmd() *cobra.Command {
	var f agentFlags
	cmd := &cobra.Command{
		Use:   "signal <SIGHUP|SIGINT|SIGQUIT|SIGTERM|SIGKILL>",
		Short: "Send a signal to the session process",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			data, err := c.post(cmd.Context(), c.sessionPath("/signal"),
				map[string]string{"signal": args[0]})
			if err != nil {
				return err
			}
			return c.emit(json.RawMessage(jsonCompact(data)), "")
		},
	}
	addAgentFlags(cmd, &f)
	return cmd
}

func buildAgentStatusCmd() *cobra.Command {
	var f agentFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show session state (running/exited, exit code, size)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			data, err := c.get(cmd.Context(), "/api/sessions/"+url.PathEscape(c.session), nil)
			if err != nil {
				return err
			}
			var st struct {
				ID       string `json:"id"`
				State    string `json:"state"`
				Command  string `json:"command"`
				PID      int    `json:"pid"`
				Exited   bool   `json:"exited"`
				ExitCode *int   `json:"exit_code"`
				Cols     int    `json:"cols"`
				Rows     int    `json:"rows"`
			}
			human := ""
			if err := json.Unmarshal(data, &st); err == nil {
				code := "-"
				if st.ExitCode != nil {
					code = fmt.Sprint(*st.ExitCode)
				}
				human = fmt.Sprintf("%s  state=%s pid=%d exited=%v exit_code=%s size=%dx%d cmd=%s\n",
					st.ID, st.State, st.PID, st.Exited, code, st.Cols, st.Rows, st.Command)
			}
			return c.emit(json.RawMessage(jsonCompact(data)), human)
		},
	}
	addAgentFlags(cmd, &f)
	return cmd
}

// ---- monitor --------------------------------------------------------------

func buildAgentMonitorCmd() *cobra.Command {
	var (
		f      agentFlags
		fps    int
		format string
	)
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Watch a session read-only (dirty-row diff stream)",
		Long: "Subscribe to the session's screen mirror WITHOUT attaching: a human\n" +
			"(or another agent) keeps full control, and the monitor cannot preempt\n" +
			"anyone. Only rows that changed since the previous frame are printed,\n" +
			"at most --fps times per second.\n\n" +
			"This is the SSH / no-browser answer to \"let me watch what it's doing\".",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := f.newAgentClient()
			if err != nil {
				return err
			}
			switch format {
			case "text", "json":
			default:
				return fmt.Errorf("unknown format %q (want text|json)", format)
			}
			return c.monitor(cmd.Context(), fps, format)
		},
	}
	addAgentFlags(cmd, &f)
	cmd.Flags().IntVar(&fps, "fps", 10, "maximum updates per second (1-30)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text (changed rows) | json (one frame per line)")
	return cmd
}

// monitor dials the mirror channel and prints each diff until the context is
// cancelled (Ctrl-C) or the connection dies.
func (c *agentClient) monitor(ctx context.Context, fps int, format string) error {
	endpoint := wsURL(c.base) + "/ws?" + url.Values{
		"session_id": {c.session},
		"mode":       {"mirror"},
		"fps":        {fmt.Sprint(fps)},
	}.Encode()

	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{})
	if err != nil {
		return fmt.Errorf("cannot monitor %s: %w", c.base, err)
	}
	defer conn.CloseNow()

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // Ctrl-C: clean exit
			}
			return err
		}
		if typ != websocket.MessageBinary || len(data) == 0 {
			continue
		}
		if data[0] != terminal.MirrorDiff {
			continue
		}
		frame, err := terminal.DecodeMirrorDiff(data[1:])
		if err != nil {
			continue
		}
		if format == "json" {
			line, _ := json.Marshal(frame)
			if _, err := fmt.Fprintf(c.out, "%s\n", line); err != nil {
				return err
			}
			continue
		}
		for _, l := range frame.Lines {
			if _, err := fmt.Fprintf(c.out, "%3d | %s\n", l.Row, l.Text); err != nil {
				return err
			}
		}
	}
}

// wsURL rewrites an http(s) base URL to its ws(s) counterpart.
func wsURL(base string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	default:
		return base
	}
}

// ---- helpers --------------------------------------------------------------

func orDefault(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// writeAgentBytes writes binary output: stdout for "-", a file otherwise.
func writeAgentBytes(path string, data []byte) error {
	if path == "" || path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
