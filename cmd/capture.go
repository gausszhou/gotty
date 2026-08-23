package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/gausszhou/gotty/internal/capture"
)

// buildCaptureCmd creates the `gotty capture` subcommand.
//
// Usage: gotty capture [flags] [--] command [<arguments...>]
//
// It runs the command in a terminal and captures the rendered result as
// text, styled JSON cells or HTML — the Playwright-of-terminals story:
// execute, wait for the render to settle, take the result away.
func buildCaptureCmd() *cobra.Command {
	var (
		engine  string
		format  string
		cols    int
		rows    int
		cellW   int
		cellH   int
		waitMs  int
		timeout time.Duration
		marker  string
		outPath string
		noCells bool
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
			"      'printf \\033[31mRED\\033[0m'",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if engine != "" && engine != "native" {
				return fmt.Errorf("unknown engine %q (browser engine lands in a later milestone)", engine)
			}
			return runCaptureNative(format, args, captureOptions{
				cols: cols, rows: rows, cellW: cellW, cellH: cellH,
				waitMs: waitMs, timeout: timeout, marker: marker,
				noCells: noCells, outPath: outPath,
			})
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&engine, "engine", "native", "rendering engine: native (browser in a later milestone)")
	flags.StringVar(&format, "format", "text", "result format: text | json | html | png")
	flags.IntVar(&cols, "cols", 120, "terminal columns")
	flags.IntVar(&rows, "rows", 30, "terminal rows")
	flags.IntVar(&cellW, "cell-w", 9, "cell width in pixels (png rendering / image placement)")
	flags.IntVar(&cellH, "cell-h", 18, "cell height in pixels (png rendering / image placement)")
	flags.IntVar(&waitMs, "wait-ms", 500, "capture when output has been silent for this many ms")
	flags.DurationVar(&timeout, "timeout", 30*time.Second, "overall timeout; on expiry the current screen is returned")
	flags.StringVar(&marker, "marker", "", "capture when this string appears in the output stream")
	flags.StringVar(&outPath, "out", "", "output path ('-' or empty = stdout)")
	flags.BoolVar(&noCells, "no-cells", false, "omit per-cell styles from --format json")

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
