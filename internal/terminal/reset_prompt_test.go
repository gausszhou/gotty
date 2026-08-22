package terminal

import (
	"bytes"
	"testing"
	"time"
)

// TestTerminal_ResetSequenceOnPrompt 验证 bash 会话通过 PROMPT_COMMAND
// 在每个提示符前发送鼠标模式复位序列(?1000l/?1002l/?1003l/?1006l),
// 防止 TUI 退出/被杀后鼠标字节被 shell echo 成明文乱码。
// 暂缓项断言:2004l / 1049l / 25h 等其它复位暂不启用。
//
// 注意:清理只关闭 PTY 不强等进程回收(go test 环境对 bash 的
// SIGHUP→Wait 回收有异常;生产路径(独立进程)已验证正常)。
func TestTerminal_ResetSequenceOnPrompt(t *testing.T) {
	term, err := New("bash", nil)
	if err != nil {
		t.Fatalf("failed to start bash: %s", err)
	}
	t.Cleanup(func() {
		_ = term.pty.Close()
		if term.cmd.Process != nil {
			_ = term.cmd.Process.Kill()
		}
	})

	got := collectOutput(term, 5*time.Second)
	if !bytes.Contains(got, []byte("\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l")) {
		t.Fatalf("prompt reset sequence not found in bash output: %q",
			truncate(got, 300))
	}
	// 暂缓项:以下复位序列不应出现
	if bytes.Contains(got, []byte("\x1b[?2004l")) {
		t.Fatal("unexpected bracketed-paste reset (?2004l), not enabled yet")
	}
	if bytes.Contains(got, []byte("\x1b[?1049l")) {
		t.Fatal("unexpected alternate-screen reset (?1049l), not enabled yet")
	}
	if bytes.Contains(got, []byte("\x1b[25h")) {
		t.Fatal("unexpected cursor-show (25h), not enabled yet")
	}
}

// collectOutput reads the PTY until the reset sequence appears or the
// deadline expires.
func collectOutput(term *Terminal, timeout time.Duration) []byte {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, err := term.Read(tmp)
		if err != nil {
			break
		}
		buf = append(buf, tmp[:n]...)
		if bytes.Contains(buf, []byte("\x1b[?1000l")) {
			break
		}
	}
	return buf
}

func truncate(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}