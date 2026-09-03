package capture

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunExitText(t *testing.T) {
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf hello"},
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
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "exit 3"},
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

func TestRunMarker(t *testing.T) {
	start := time.Now()
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf abc; sleep 1"},
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
	if time.Since(start) > 2*time.Second {
		t.Error("marker stop took too long (sleep 1 was meant to be aborted)")
	}
}

func TestRunQuiet(t *testing.T) {
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf a; sleep 0.3; printf b; sleep 1"},
		Cols:    80,
		Rows:    20,
		WaitMs:  100,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopQuiet {
		t.Errorf("reason = %s, want quiet", res.StopReason)
	}
	// a 之后输出静默 ≥100ms 即触发;b 要 300ms 后才来,快照里不应有它
	if got := Text(res.Emulator.Screen()); got != "a" {
		t.Errorf("text = %q, want a", got)
	}
}

func TestRunTimeout(t *testing.T) {
	start := time.Now()
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "sleep 2"},
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
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("run took %v (reaping failed?)", elapsed)
	}
}

func TestRunColsRowsPlumbed(t *testing.T) {
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "true"},
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

func TestRunWideOutput(t *testing.T) {
	// 输出超过屏幕宽度时应正确折行,而不是宽度溢出 panic
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf '1234567890abc'"},
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
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", "printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaamarker-end'; sleep 0.5"},
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

func TestRunAnswersQueriesBack(t *testing.T) {
	// 程序发 DSR6 并用 dd 读回应答:捕获引擎必须把应答写回 PTY,
	// dd 才能读到 6 字节的 CPR 并以十六进制打印出来。
	script := `printf "\033[6n"; dd bs=1 count=6 2>/dev/null | od -An -tx1`
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", script},
		Cols:    40,
		Rows:    10,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopExit {
		t.Errorf("reason = %s, want exit", res.StopReason)
	}
	// 光标在 (0,0) → CPR `\x1b[1;1R`,od 打印 "1b 5b 31 3b 31 52"
	text := Text(res.Emulator.Screen())
	if !strings.Contains(text, "1b 5b 31 3b 31 52") {
		t.Errorf("DSR reply not written back to the program; screen:\n%s", text)
	}
}

func TestRunTimeoutFallbackWithQueries(t *testing.T) {
	// 发出查询但程序继续睡眠:即使有应答,"超时兜底"路径仍返回当前屏
	res, err := Run(Options{
		Command: "/bin/sh",
		Args:    []string{"-c", `printf "\033[6n"; printf X; sleep 10`},
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
