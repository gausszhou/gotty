//go:build windows

package capture

// The Windows half of the driver tests. The unix half (driver_unix_test.go)
// drives the driver through `sh -c`; Windows has no `sh`, so the same contracts
// are re-tested through `cmd /c` — the shell a Windows user actually has (see
// terminal's fallbackShell).
//
// Two things cannot be mirrored, and both are deliberate rather than skipped:
//
//   - Sub-second timing: cmd's builtins have no sub-second wait (`timeout` is
//     console-bound, `ping -n` is whole seconds), so the quiet/marker tests use
//     a ~2s gap with a correspondingly larger WaitMs. The contract under test
//     (silence threshold, marker aborts a long run) is unchanged.
//   - Query answering: ConPTY's console host consumes console queries itself and
//     the query never reaches this process, so there is nothing for the capture
//     engine to answer. TestConPTYConsoleHostAnswersQueriesItself pins that
//     platform contract; the unix-only TestRunAnswersQueriesBack covers the
//     engine's own answer path.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/gausszhou/gotty/internal/terminal"
)

// cmdScript is the `cmd /c` form of the shell scripts the unix tests use.
func cmdScript(script string) (string, []string) {
	return "cmd.exe", []string{"/c", script}
}

func TestRunExitText(t *testing.T) {
	cmd, args := cmdScript("echo hello")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    80,
		Rows:    20,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopExit {
		t.Errorf("reason = %s, want exit", res.StopReason)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", res.ExitCode)
	}
	if got := Text(res.Emulator.Screen()); got != "hello" {
		t.Errorf("text = %q, want hello", got)
	}
}

func TestRunExitCode(t *testing.T) {
	cmd, args := cmdScript("exit 3")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    40,
		Rows:    10,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == nil || *res.ExitCode != 3 {
		t.Errorf("exit code = %v, want 3", res.ExitCode)
	}
}

// TestRunMarker checks that hitting the marker aborts a long-running command.
// `ping -n 5` keeps cmd alive for ~4s, so a marker stop well under that proves
// the run was cut short instead of waiting for the process.
func TestRunMarker(t *testing.T) {
	start := time.Now()
	cmd, args := cmdScript("echo abc & ping -n 5 127.0.0.1 >NUL")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    80,
		Rows:    20,
		Marker:  "ab",
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopMarker {
		t.Errorf("reason = %s, want marker", res.StopReason)
	}
	if got := Text(res.Emulator.Screen()); got != "abc" {
		t.Errorf("text = %q, want abc", got)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("marker stop took %v (ping -n 5 was meant to be aborted)", elapsed)
	}
}

// TestRunQuiet mirrors the unix quiet test with cmd-compatible timing: a ~2s
// silence after "a" must stop the run before the later "b" arrives. WaitMs is
// raised from 100ms to 1s because cmd cannot sleep for sub-second amounts.
func TestRunQuiet(t *testing.T) {
	cmd, args := cmdScript("echo a & ping -n 3 127.0.0.1 >NUL & echo b & ping -n 3 127.0.0.1 >NUL")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    80,
		Rows:    20,
		WaitMs:  1000,
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopQuiet {
		t.Errorf("reason = %s, want quiet", res.StopReason)
	}
	// a 之后静默 ≥1s 即触发;b 要 ~2s 后才来,快照里不应有它
	if got := Text(res.Emulator.Screen()); got != "a" {
		t.Errorf("text = %q, want a", got)
	}
}

func TestRunTimeout(t *testing.T) {
	start := time.Now()
	cmd, args := cmdScript("ping -n 30 127.0.0.1 >NUL")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    40,
		Rows:    10,
		WaitMs:  0,
		Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Error("timed_out not set")
	}
	if res.StopReason != StopTimeout {
		t.Errorf("reason = %s, want timeout", res.StopReason)
	}
	if res.ExitCode != nil {
		t.Errorf("exit code = %v, want nil (process still running)", res.ExitCode)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("run took %v (reaping failed?)", elapsed)
	}
}

func TestRunColsRowsPlumbed(t *testing.T) {
	cmd, args := cmdScript("exit 0")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    41,
		Rows:    13,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Emulator.Cols() != 41 || res.Emulator.Rows() != 13 {
		t.Errorf("size = %dx%d, want 41x13", res.Emulator.Cols(), res.Emulator.Rows())
	}
}

func TestRunWideOutput(t *testing.T) {
	// 输出超过屏幕宽度时应正确折行,而不是宽度溢出 panic
	cmd, args := cmdScript("echo 1234567890abc")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    5,
		Rows:    10,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := Text(res.Emulator.Screen())
	want := "12345\n67890\nabc"
	if got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func TestRunMarkerAcrossChunks(t *testing.T) {
	// 大量输出把 marker 拆到两个读块之间:仍应命中
	cmd, args := cmdScript("echo " + strings.Repeat("a", 44) + "marker-end & ping -n 3 127.0.0.1 >NUL")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    40,
		Rows:    20,
		Marker:  "marker",
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopMarker {
		t.Errorf("reason = %s, want marker", res.StopReason)
	}
}

// TestRunTimeoutFallbackReturnsScreen covers the timeout fallback: the screen
// produced before the deadline is returned with TimedOut set.
//
// The unix counterpart additionally emits a DSR query to prove that an answer
// does not derail the timeout path. That half cannot exist here: on Windows the
// query never reaches this process and the line that carried it renders blank,
// so emitting it would only corrupt the expected screen (see
// TestConPTYConsoleHostAnswersQueriesItself).
func TestRunTimeoutFallbackReturnsScreen(t *testing.T) {
	cmd, args := cmdScript("echo X & ping -n 30 127.0.0.1 >NUL")
	res, err := Run(Options{
		Command: cmd,
		Args:    args,
		Cols:    40,
		Rows:    10,
		Timeout: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.StopReason != StopTimeout {
		t.Errorf("reason = %s timedout=%v, want timeout", res.StopReason, res.TimedOut)
	}
	if got := Text(res.Emulator.Screen()); got != "X" {
		t.Errorf("text = %q, want X (screen up to the timeout)", got)
	}
}

// ---------------------------------------------------------------------------
// ConPTY query contract
// ---------------------------------------------------------------------------

const (
	// queryHelperEnv marks the re-executed test binary as the session process.
	queryHelperEnv = "GOTTY_CAPTURE_QUERY_HELPER"
	// queryReplyMarker prefixes the hex reply the helper read back.
	queryReplyMarker = "CAPTURE-QUERY-REPLY="
	// queryReplyTimeout is reported when nothing answered the query.
	queryReplyTimeout = "CAPTURE-QUERY-REPLY-TIMEOUT"
)

// TestCaptureQueryHelper is not a test. It is the child process that
// TestConPTYConsoleHostAnswersQueriesItself runs on a pseudoconsole: the parent
// re-executes this very test binary with queryHelperEnv set, and this function
// then plays the part of a tiny TUI that asks for the cursor position (DSR,
// ESC[6n) and reports whatever answered.
//
// It is a no-op during an ordinary test run.
func TestCaptureQueryHelper(t *testing.T) {
	if os.Getenv(queryHelperEnv) != "1" {
		return
	}
	rawConsoleInput()

	if _, err := os.Stdout.WriteString("\x1b[6n"); err != nil {
		os.Exit(0)
	}

	buf := make([]byte, 6)
	read := make(chan int, 1)
	go func() {
		n, _ := io.ReadFull(os.Stdin, buf)
		read <- n
	}()

	select {
	case n := <-read:
		_, _ = fmt.Fprintf(os.Stdout, "\r\n%s%s\r\n", queryReplyMarker, hex.EncodeToString(buf[:n]))
	case <-time.After(3 * time.Second):
		_, _ = os.Stdout.WriteString("\r\n" + queryReplyTimeout + "\r\n")
	}
	time.Sleep(100 * time.Millisecond)
	os.Exit(0)
}

// rawConsoleInput turns off line buffering and echo on the console input.
//
// Without this the reply conhost pushes into the input buffer is never delivered
// to Read (cooked mode waits for a newline) and the echoed escape sequence shows
// up on screen — both were observed while establishing this contract. A real TUI
// does the same at startup, plus ENABLE_VIRTUAL_TERMINAL_INPUT.
func rawConsoleInput() {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return
	}
	_ = windows.SetConsoleMode(h, mode&^(windows.ENABLE_LINE_INPUT|windows.ENABLE_ECHO_INPUT))
}

// TestConPTYConsoleHostAnswersQueriesItself pins the Windows query contract that
// makes TestRunAnswersQueriesBack unix-only:
//
//  1. ConPTY's console host consumes a child's console query itself — the query
//     never reaches this process, so the capture engine's answer path is not
//     exercised (and is not needed) for DSR on Windows;
//  2. the child still gets a well-formed reply, so querying programs do not hang.
//
// If a future Windows stops consuming queries (or stops answering), this test
// goes red and the unix-only decision for TestRunAnswersQueriesBack has to be
// revisited.
func TestConPTYConsoleHostAnswersQueriesItself(t *testing.T) {
	const cols, rows = 60, 12
	emu := NewEmulator(cols, rows)

	term, err := terminal.New(os.Args[0],
		[]string{"-test.run=^TestCaptureQueryHelper$"},
		terminal.WithInitialSize(cols, rows),
		terminal.WithEnv([]string{queryHelperEnv + "=1"}),
	)
	if err != nil {
		t.Fatalf("failed to start the query helper: %s", err)
	}
	defer func() { _ = term.Close() }()

	// Mirror driver.Run: feed the emulator and write its answers back.
	raw := make([]byte, 0, 8192)
	answers := make([]byte, 0, 64)
	buf := make([]byte, 4096)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		n, rerr := term.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
			emu.Write(buf[:n])
			if a := emu.DrainAnswers(); len(a) > 0 {
				answers = append(answers, a...)
				_, _ = term.Write(a)
			}
		}
		if rerr != nil {
			break
		}
	}

	text := Text(emu.Screen())

	// (2) the child got a reply from the console host.
	if strings.Contains(text, queryReplyTimeout) {
		t.Fatalf("nothing answered the child's DSR query; screen:\n%s", text)
	}
	idx := strings.Index(text, queryReplyMarker)
	if idx < 0 {
		t.Fatalf("the helper never reported a reply; screen:\n%s", text)
	}
	line := text[idx+len(queryReplyMarker):]
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	reply, err := hex.DecodeString(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("reply %q is not hex: %s", line, err)
	}
	if len(reply) != 6 || reply[0] != 0x1b || reply[1] != '[' || reply[5] != 'R' {
		t.Errorf("reply % x is not a cursor-position report (ESC [ row ; col R)", reply)
	}

	// (1) the query itself never reached this process.
	if bytes.Contains(raw, []byte("\x1b[6n")) {
		t.Errorf("the child's DSR query reached the host app; if ConPTY now " +
			"forwards queries, the capture engine must answer them and " +
			"TestRunAnswersQueriesBack needs a Windows counterpart")
	}
	if len(answers) > 0 {
		t.Errorf("the host app answered %q; it should never see the query", answers)
	}
}
