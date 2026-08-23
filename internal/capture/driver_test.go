package capture

import (
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
