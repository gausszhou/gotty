package terminal

import (
	"bytes"
	"testing"
	"time"
)

// TestTerminal_ResetSequenceOnPrompt 验证 bash 会话通过 PROMPT_COMMAND
// 在每个提示符前发送终端模式复位序列:离开备用屏(?1049l)、鼠标模式
// (?1000l/?1002l/?1003l/?1006l)、显示光标(25h)、关闭括号粘贴(?2004l),
// 防止 TUI 退出/被杀后残留画面、鼠标字节乱码与隐藏光标。
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
	reset := "\x1b[?1049l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?25h\x1b[?2004l"
	if !bytes.Contains(got, []byte(reset)) {
		t.Fatalf("prompt reset sequence not found in bash output: %q",
			truncate(got, 300))
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
