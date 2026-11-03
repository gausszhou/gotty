// Command fixture-tui is the minimal TUI used by scripts/e2e/skill-smoke.sh.
//
// It exercises exactly what the gotty-tui-test harness demonstrates: an
// unambiguous first frame ("Ready"), a key-driven state change ("Done" after
// Enter), and a clean exit on "q". Keep it dependency-free and fast — its only
// job is to prove the skill's template still runs.
package main

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	fmt.Print("Ready\r\n")
	r := bufio.NewReader(os.Stdin)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return
		}
		switch b {
		case '\r', '\n':
			fmt.Print("Done\r\n")
		case 'q':
			fmt.Print("Bye\r\n")
			return
		default:
			fmt.Printf("key:%q\r\n", b)
		}
	}
}
