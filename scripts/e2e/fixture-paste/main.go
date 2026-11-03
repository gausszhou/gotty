// Command fixture-paste is the bracketed-paste probe used by
// scripts/e2e/agent-api.sh.
//
// It enables mode 2004 (bracketed paste), prints a ready marker so the test
// can wait for the mode to reach the screen mirror, then reads ONE burst of
// raw stdin and reports exactly what arrived: was it wrapped in
// ESC[200~ … ESC[201~, how many bytes inside, how many newlines, and how
// many marker pairs.
//
// Raw mode matters: in the default cooked mode the line discipline would
// split the paste at its newlines, which is precisely the "executed line by
// line" failure the conditional-paste feature exists to prevent. Raw mode
// also disables echo, so the report is the only thing on screen.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
)

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

func main() {
	if term.IsTerminal(os.Stdin.Fd()) {
		if _, err := term.MakeRaw(os.Stdin.Fd()); err != nil {
			fmt.Printf("PASTE_FIXTURE_ERROR raw=%v\r\n", err)
			return
		}
	}

	// Enable bracketed paste for the mirror, then advertise readiness.
	fmt.Print("\x1b[?2004h")
	fmt.Print("PASTE_READY\r\n")

	data := readBurst(2*time.Second, 300*time.Millisecond)

	start := bytes.Count(data, []byte(pasteStart))
	end := bytes.Count(data, []byte(pasteEnd))
	wrapped := start == 1 && end == 1 &&
		bytes.HasPrefix(data, []byte(pasteStart)) &&
		bytes.HasSuffix(data, []byte(pasteEnd))

	body := data
	if wrapped {
		body = data[len(pasteStart) : len(data)-len(pasteEnd)]
	}
	head, tail := firstLastLine(body)
	fmt.Printf("PASTE_REPORT wrapped=%s len=%d nl=%d start=%d end=%d head=%s tail=%s\r\n",
		yesNo(wrapped), len(body), bytes.Count(body, []byte("\n")),
		start, end, sanitize(head), sanitize(tail))

	// Keep the session alive long enough for the test to read the report and
	// dump it as evidence; the script destroys the session explicitly.
	time.Sleep(30 * time.Second)
}

// readBurst accumulates stdin until the bracketed-paste terminator shows up,
// input goes idle, or a hard deadline fires. The hard deadline matters for
// the negative case: an unwrapped paste has no terminator.
func readBurst(hard, idle time.Duration) []byte {
	type chunk struct {
		data []byte
		err  error
	}
	chunks := make(chan chunk, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				cp := append([]byte(nil), buf[:n]...)
				chunks <- chunk{data: cp}
			}
			if err != nil {
				chunks <- chunk{err: err}
				return
			}
		}
	}()

	var data []byte
	idleTimer := time.NewTimer(time.Hour)
	idleTimer.Stop()
	defer idleTimer.Stop()
	hardTimer := time.NewTimer(hard)
	defer hardTimer.Stop()
	sawData := false

	for {
		select {
		case c := <-chunks:
			if c.err != nil {
				return data
			}
			data = append(data, c.data...)
			sawData = true
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(idle)
			if bytes.Contains(data, []byte(pasteEnd)) {
				return data
			}
		case <-idleTimer.C:
			if sawData {
				return data
			}
		case <-hardTimer.C:
			return data
		}
	}
}

// firstLastLine returns the first and last newline-separated line of body.
func firstLastLine(body []byte) (string, string) {
	lines := strings.Split(string(body), "\n")
	head := strings.TrimSuffix(lines[0], "\r")
	tail := strings.TrimSuffix(lines[len(lines)-1], "\r")
	return head, tail
}

// sanitize keeps the report on one printable line.
func sanitize(s string) string {
	if s == "" {
		return "-"
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
