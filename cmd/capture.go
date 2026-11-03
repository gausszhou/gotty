package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gausszhou/gotty/internal/browser"
	"github.com/gausszhou/gotty/internal/capture"
)

// buildCaptureCmd creates the `gotty capture` subcommand.
//
// Usage: gotty capture [flags] [--] command [<arguments...>]
//
// It runs the command in a terminal and captures the rendered result —
// via the in-process native engine (text / styled JSON cells / HTML /
// PNG bitmap) or the browser engine (pixel-perfect screenshot of the real
// page, needs a Chrome/Chromium).
func buildCaptureCmd() *cobra.Command {
	var (
		engine      string
		format      string
		cols        int
		rows        int
		cellW       int
		cellH       int
		waitMs      int
		timeout     time.Duration
		marker      string
		outPath     string
		noCells     bool
		browserPath string
		sessionID   string
		serverURL   string
		keysBefore  []string
	)

	cmd := &cobra.Command{
		Use:   "capture [flags] [--] command [<arguments...>]",
		Short: "Run a command and capture its rendered terminal output",
		Long: "Run a command in a terminal and capture the rendered result\n" +
			"as text, styled cells (JSON) or styled HTML. Use `--` before the\n" +
			"command so its own flags are not consumed by capture.\n\n" +
			"Examples:\n" +
			"  gotty capture --format text -- ls -la\n" +
			"  gotty capture --format json -- chafa --format symbols logo.png\n" +
			"  gotty capture --format html --out out.html -- \\\n" +
			"      'printf \\033[31mRED\\033[0m'\n" +
			"  gotty capture --engine browser --format png --out shot.png -- htop\n" +
			"  gotty capture --engine attached --session-id $ID --out shot.png",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch engine {
			case "", "native":
				return runCaptureNative(format, args, captureOptions{
					cols: cols, rows: rows, cellW: cellW, cellH: cellH,
					waitMs: waitMs, timeout: timeout, marker: marker,
					noCells: noCells, outPath: outPath,
				})
			case "browser":
				if format != "png" {
					return fmt.Errorf("engine browser renders --format png only; use --engine native for text/json/html")
				}
				if sessionID == "" && len(args) == 0 {
					return fmt.Errorf("no command given: `gotty capture --engine browser -- <command>` or pass --session-id")
				}
				base, shutdown, err := browser.NewEmbeddedServer(nil)
				if err != nil {
					return err
				}
				defer shutdown()
				res, err := browser.RunBrowser(base, browser.BrowserOptions{
					Command:     firstOr(args, 0, ""),
					Args:        restOr(args),
					SessionID:   sessionID,
					Cols:        cols,
					Rows:        rows,
					WaitMs:      waitMs,
					Timeout:     timeout,
					Marker:      marker,
					BrowserPath: browserPath,
				})
				if err != nil {
					return err
				}
				log.Printf("capture(browser): session=%s reason=%s duration=%v", res.SessionID, res.StopReason, res.Duration)
				return writeCaptureBytes(outPath, res.PNG)
			case "attached":
				if format != "png" {
					return fmt.Errorf("engine attached renders --format png only")
				}
				return runCaptureAttached(resolveServerURL(serverURL), sessionID, keysBefore, captureOptions{
					waitMs: waitMs, timeout: timeout, outPath: outPath,
				})
			default:
				return fmt.Errorf("unknown engine %q (want native|browser|attached)", engine)
			}
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&engine, "engine", "native", "rendering engine: native | browser | attached (browser needs Chrome/Chromium; attached asks an already-attached browser)")
	flags.StringVar(&format, "format", "text", "result format: text | json | html | png")
	flags.IntVar(&cols, "cols", 120, "terminal columns")
	flags.IntVar(&rows, "rows", 30, "terminal rows")
	flags.IntVar(&cellW, "cell-w", 9, "cell width in pixels (native png rendering / image placement)")
	flags.IntVar(&cellH, "cell-h", 18, "cell height in pixels (native png rendering / image placement)")
	flags.IntVar(&waitMs, "wait-ms", 500, "capture when output has been silent for this many ms")
	flags.DurationVar(&timeout, "timeout", 30*time.Second, "overall timeout; on expiry the current screen is returned")
	flags.StringVar(&marker, "marker", "", "capture when this string appears in the output stream")
	flags.StringVar(&outPath, "out", "", "output path ('-' or empty = stdout)")
	flags.BoolVar(&noCells, "no-cells", false, "omit per-cell styles from --format json")
	flags.StringVar(&browserPath, "browser-path", "", "engine browser: path to a Chrome/Chromium binary (default: search PATH)")
	flags.StringVar(&sessionID, "session-id", "", "engine browser/attached: session to capture instead of creating one")
	flags.StringVar(&serverURL, "server", "", "engine attached: GoTTY server URL (default http://127.0.0.1:9049 or $GOTTY_SERVER)")
	flags.StringSliceVar(&keysBefore, "keys", nil, "engine attached: key names to press before capturing (comma separated, e.g. Escape,Enter)")

	return cmd
}

// captureOptions bundles the native-engine CLI flags.
type captureOptions struct {
	cols, rows   int
	cellW, cellH int
	waitMs       int
	timeout      time.Duration
	marker       string
	noCells      bool
	outPath      string
}

// runCaptureNative executes the in-process engine (no browser needed).
func runCaptureNative(format string, args []string, o captureOptions) error {
	switch format {
	case "text", "json", "html", "png":
	default:
		return fmt.Errorf("unknown format %q (want text|json|html|png)", format)
	}
	if len(args) == 0 {
		return fmt.Errorf("no command given: `gotty capture -- <command> [args...]`")
	}

	res, err := capture.Run(capture.Options{
		Command: args[0],
		Args:    args[1:],
		Cols:    o.cols,
		Rows:    o.rows,
		CellW:   o.cellW,
		CellH:   o.cellH,
		WaitMs:  o.waitMs,
		Timeout: o.timeout,
		Marker:  o.marker,
	})
	if err != nil {
		return err
	}

	switch format {
	case "text":
		return writeCaptureOutput(o.outPath, capture.Text(res.Emulator.Screen()))
	case "html":
		return writeCaptureOutput(o.outPath, capture.HTML(res.Emulator.Screen()))
	case "json":
		doc := capture.NewDocument(res.Emulator, args[0], args[1:],
			res.ExitCode, res.TimedOut, res.Duration, res.StopReason,
			res.Emulator.Images(), o.cellW, o.cellH, !o.noCells)
		b, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		return writeCaptureOutput(o.outPath, string(b)+"\n")
	case "png":
		data, err := capture.PNG(res.Emulator.Screen(), res.Emulator.Images(), o.cellW, o.cellH)
		if err != nil {
			return fmt.Errorf("render png: %w", err)
		}
		return writeCaptureBytes(o.outPath, data)
	}
	return nil
}

// runCaptureAttached asks the browser that is already attached to a running
// session to composite its terminal view (capture-design.md §8). No Chromium
// and no extra client: the server sends a CaptureRequest on the attached
// channel and the page answers with a PNG, so the session's attachment is
// untouched.
func runCaptureAttached(baseURL, sessionID string, keys []string, o captureOptions) error {
	if sessionID == "" {
		return fmt.Errorf("engine attached needs --session-id: a browser must already be attached to that session")
	}
	base := strings.TrimRight(baseURL, "/")
	httpc := &http.Client{Timeout: o.timeout}
	path := base + "/api/sessions/" + url.PathEscape(sessionID)

	// --wait-ms: 先等输出静默,与 native/browser 的稳定判定对齐。
	// best-effort:--mirror=false 时该端点 503,不应阻断截图。
	if o.waitMs > 0 {
		_, _ = postJSON(httpc, path+"/wait", map[string]int{
			"quiet_ms":   o.waitMs,
			"timeout_ms": int(o.timeout / time.Millisecond),
		})
	}

	// --keys:截图前先发按键(关掉对话框/切到目标页面),复用 agent keys 端点。
	// 这是"attached 交互"那一半:没有它,想截一张"点开菜单后"的图就只能
	// 手工先按一下,截图时机不可复现。
	if len(keys) > 0 {
		if _, err := postJSON(httpc, path+"/keys", map[string]interface{}{"keys": keys}); err != nil {
			return fmt.Errorf("send keys before capture: %w", err)
		}
	}

	body, err := postJSON(httpc, path+"/capture", map[string]int{
		"timeout_ms": int(o.timeout / time.Millisecond),
	})
	if err != nil {
		return err
	}
	var out struct {
		PNG  string `json:"png"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("invalid capture response: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(out.PNG)
	if err != nil {
		return fmt.Errorf("invalid capture payload: %w", err)
	}
	log.Printf("capture(attached): session=%s %dx%d %d bytes", sessionID, out.Cols, out.Rows, len(data))
	return writeCaptureBytes(o.outPath, data)
}

// postJSON POSTs a JSON body and returns the raw response body, turning a
// non-2xx status into an error that carries the server's message.
func postJSON(c *http.Client, endpoint string, body interface{}) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func firstOr(args []string, i int, dflt string) string {
	if i < len(args) {
		return args[i]
	}
	return dflt
}

func restOr(args []string) []string {
	if len(args) > 1 {
		return args[1:]
	}
	return nil
}

// writeCaptureOutput writes the payload to a file, or stdout when empty/-.
func writeCaptureOutput(path, payload string) error {
	if path == "" || path == "-" {
		_, err := os.Stdout.WriteString(payload)
		return err
	}
	return os.WriteFile(path, []byte(payload), 0o644)
}

// writeCaptureBytes writes binary output (png): stdout when "-", a default
// timestamped file when --out is unset.
func writeCaptureBytes(path string, data []byte) error {
	if path == "" {
		path = "gotty-capture-" + time.Now().Format("20060102-150405") + ".png"
	}
	if path == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
