package capture

// Tests that make no assumption about the platform's shell: they either run a
// bare executable or look one up first. Everything that drives the driver
// through a shell script lives in driver_unix_test.go (`sh -c`) and
// driver_windows_test.go (`cmd /c`), because those two scripts are not the same
// language and the POSIX side exercises termios/raw-mode behaviour that has no
// Windows equivalent.
//
// See TestConPTYConsoleHostAnswersQueriesItself (driver_windows_test.go) for why
// the query-answer test is unix-only.

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunCommandNotFound(t *testing.T) {
	_, err := Run(Options{
		Command: "definitely-not-a-command-xyz",
		Timeout: 5 * time.Second,
	})
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should hint shell usage, got: %v", err)
	}
}

// 回归:查询应答让全屏程序不再挂起(0002 验收 1/2/3)。

func TestRunVimExitsWithoutHanging(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim not installed")
	}
	start := time.Now()
	res, err := Run(Options{
		Command: "vim",
		Args:    []string{"-u", "NONE", "-c", "q"},
		Cols:    80,
		Rows:    24,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TimedOut {
		t.Fatal("vim blocked on terminal queries: timed out instead of exiting")
	}
	if res.StopReason != StopExit {
		t.Errorf("reason = %s, want exit", res.StopReason)
	}
	// 验收:DA/DSR 应答后 1s 内退出
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("vim took %v to exit (queries answered too slowly?)", elapsed)
	}
}

func TestRunHtopRendersInterface(t *testing.T) {
	if _, err := exec.LookPath("htop"); err != nil {
		t.Skip("htop not installed")
	}
	res, err := Run(Options{
		Command: "htop",
		Args:    []string{"-d", "1"},
		Cols:    100,
		Rows:    30,
		Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Errorf("reason = %s, want timeout (htop is a long-running UI)", res.StopReason)
	}
	text := Text(res.Emulator.Screen())
	// htop 的头部(CPU/Mem/Tasks 栏)出现即视为界面画出来了,而非空屏
	if !strings.Contains(text, "Tasks") && !strings.Contains(text, "Mem") {
		t.Errorf("htop screen missing header, got %d lines:\n%s",
			strings.Count(text, "\n")+1, text)
	}
}
