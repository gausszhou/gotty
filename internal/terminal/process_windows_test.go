//go:build windows

package terminal

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"testing"
	"time"
)

// These tests are the Windows counterpart of TestTerminal_ResetSequenceOnPrompt:
// they drive a real ConPTY session, which is what process_windows.go exists
// for. They are also the regression test for the bug where every session on
// Windows failed with "failed to start command `...`: unsupported" because
// creack/pty has no Windows backend.

// readUntil reads until want shows up in the output, or the deadline passes.
// It returns everything read (the caller usually only needs the error).
func readUntil(term *Terminal, want string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	got := make([]byte, 0, 4096)
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		n, err := term.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			if bytes.Contains(got, []byte(want)) {
				return got, nil
			}
		}
		if err != nil {
			return got, fmt.Errorf("read ended before %q appeared: %w (got %q)", want, err, tailOf(got))
		}
	}
	return got, fmt.Errorf("%q never appeared within %s (got %q)", want, timeout, tailOf(got))
}

func tailOf(b []byte) []byte {
	const limit = 400
	if len(b) > limit {
		return b[len(b)-limit:]
	}
	return b
}

// TestConPTYAvailable pins that the ConPTY entry points are resolved explicitly
// before use. x/sys resolves them lazily and panics when they are missing, so
// without requireConPTY an older Windows 10 (pre-1809) would crash instead of
// reporting the "requires Windows 10 1809 or newer" error. This machine has
// ConPTY, so the assertion is that the probe succeeds and stays wired in.
func TestConPTYAvailable(t *testing.T) {
	if err := requireConPTY(); err != nil {
		t.Fatalf("ConPTY must be available on a machine that can run these tests: %s", err)
	}
}

// TestConPTYRoundTrip starts an interactive cmd.exe on a pseudoconsole and
// checks the full path in both directions: prompt output, typed input,
// command output and a resize, then a clean exit that the session notices
// without any unix-style EIO.
func TestConPTYRoundTrip(t *testing.T) {
	term, err := New("cmd.exe", nil, WithInitialSize(100, 30))
	if err != nil {
		t.Fatalf("failed to start cmd.exe on a pseudoconsole: %s", err)
	}
	t.Cleanup(func() {
		_ = term.proc.Kill()
		_ = term.proc.Close()
	})

	// 1. 交互式 shell 会打印 banner 与提示符:证明输出通道是通的。
	if _, err := readUntil(term, ">", 20*time.Second); err != nil {
		t.Fatalf("no shell prompt: %s", err)
	}

	// 2. 写入命令,读回命令产生的输出(证明输入通道与回显是通的)。
	const marker = "gotty-conpty-roundtrip"
	if _, err := term.Write([]byte("echo " + marker + "\r\n")); err != nil {
		t.Fatalf("failed to write to the pseudoconsole: %s", err)
	}
	if _, err := readUntil(term, marker, 20*time.Second); err != nil {
		t.Fatalf("command output never came back: %s", err)
	}

	// 3. resize 走 ResizePseudoConsole,不再返回 creack/pty 的 ErrUnsupported。
	if err := term.Resize(120, 40); err != nil {
		t.Fatalf("failed to resize the pseudoconsole: %s", err)
	}

	// 4. shell 退出:Exited() 必须变 true,而且是在没有 unix 那种
	//    master EIO 帮忙的情况下(靠 releaseAfterExit 关闭 pseudoconsole)。
	if _, err := term.Write([]byte("exit\r\n")); err != nil {
		t.Fatalf("failed to write exit: %s", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !term.Exited() {
		if time.Now().After(deadline) {
			t.Fatal("session did not report exit after the shell was told to exit")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := term.Wait(); err != nil {
		t.Fatalf("exit status after `exit`: %s", err)
	}

	// 5. 读取端也必须收尾:进程退出后 releaseAfterExit 关掉 pseudoconsole,
	//    在这里读到的应该是 io.EOF 而不是永久阻塞。
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			if _, err := term.Read(buf); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		// Read 把断开的通道归一为 io.EOF(见 process_windows.go)。
		if !errors.Is(err, io.EOF) {
			t.Errorf("output channel ended with %v, want io.EOF", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("output channel never broke after the process exited (pump would hang)")
	}
}

// TestConPTYExitCode pins the contract capture/driver.go relies on: a
// non-zero exit comes back as *exec.ExitError carrying the real code.
func TestConPTYExitCode(t *testing.T) {
	term, err := New("cmd.exe", []string{"/c", "exit 3"})
	if err != nil {
		t.Fatalf("failed to start cmd.exe on a pseudoconsole: %s", err)
	}
	t.Cleanup(func() {
		_ = term.proc.Kill()
		_ = term.proc.Close()
	})

	waitErr := term.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("wait error = %v (%T), want *exec.ExitError", waitErr, waitErr)
	}
	if code := exitErr.ExitCode(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

// TestConPTYCloseTerminatesSession checks that tearing a session down ends
// the process (destroy / idle timeout path) instead of leaving an orphan:
// Windows has no SIGHUP, so closing the pseudoconsole is the termination.
func TestConPTYCloseTerminatesSession(t *testing.T) {
	// ping -n 600 存活时间远超测试窗口,只有 Close() 能结束它。
	term, err := New("cmd.exe", []string{"/c", "ping -n 600 127.0.0.1 >NUL"})
	if err != nil {
		t.Fatalf("failed to start cmd.exe on a pseudoconsole: %s", err)
	}
	t.Cleanup(func() { _ = term.proc.Close() })

	start := time.Now()
	if err := term.Close(); err != nil {
		t.Fatalf("close failed: %s", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("close took %s, want a prompt termination", elapsed)
	}
	if !term.Exited() {
		t.Fatal("process still running after Close")
	}
}
