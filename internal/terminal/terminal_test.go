package terminal

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// envValue returns the value of key in the composed environment, or "".
func envValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return ""
}

func envHas(t *testing.T, env []string, key string) bool {
	t.Helper()
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

// TestBuildEnvDefaults 验证默认注入:TERM=xterm-256color、
// COLORTERM=truecolor,且继承环境中的颜色变量被剥离统一为本层默认值。
func TestBuildEnvDefaults(t *testing.T) {
	t.Setenv("TERM", "vt100")
	t.Setenv("COLORTERM", "1")
	t.Setenv("UNRELATED", "keep-me")

	env := buildEnv("/bin/bash", nil)

	if got := envValue(t, env, "TERM"); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", got)
	}
	if got := envValue(t, env, "COLORTERM"); got != "truecolor" {
		t.Fatalf("COLORTERM = %q, want truecolor", got)
	}
	if envValue(t, env, "UNRELATED") != "keep-me" {
		t.Fatal("unrelated inherited variable must be preserved")
	}
	if envHas(t, env, "vt100") {
		t.Fatal("inherited TERM value must be stripped (no duplicates)")
	}
}

// TestBuildEnvOverride 验证 extras 中的 COLORTERM 可覆盖默认值。
func TestBuildEnvOverride(t *testing.T) {
	env := buildEnv("/bin/sh", []string{
		"COLORTERM=24bit",
	})

	if got := envValue(t, env, "COLORTERM"); got != "24bit" {
		t.Fatalf("COLORTERM = %q, want 24bit", got)
	}
}

// TestBuildEnvLaterOverrideWins 验证多个同类条目按序后者胜。
func TestBuildEnvLaterOverrideWins(t *testing.T) {
	env := buildEnv("/bin/sh", []string{
		"COLORTERM=24bit",
		"COLORTERM=truecolor",
	})

	if got := envValue(t, env, "COLORTERM"); got != "truecolor" {
		t.Fatalf("COLORTERM = %q, want the later value truecolor", got)
	}
}

// TestBuildEnvKeepsExtras 验证普通 env 条目原样保留并去重。
func TestBuildEnvKeepsExtras(t *testing.T) {
	env := buildEnv("/bin/sh", []string{"FOO=bar", "EDITOR=vim"})

	if envValue(t, env, "FOO") != "bar" || envValue(t, env, "EDITOR") != "vim" {
		t.Fatal("plain extras must be preserved")
	}
}

// TestTerminal_ResetSequenceOnPrompt 验证 bash 会话通过 PROMPT_COMMAND
// 在每个提示符前发送终端模式复位序列:鼠标模式
// (?1000l/?1002l/?1003l/?1006l)、显示光标(25h)、关闭括号粘贴(?2004l),
// 防止 TUI 退出/被杀后残留画面、鼠标字节乱码与隐藏光标。
//
// 注意:不发送 ?1049l(离开备用屏)——bash 从不会进入备用屏,该序列会被
// xterm.js 当作"切回主屏并恢复光标"处理,把光标拽到已保存位置,导致
// 新提示符画在历史输出行上(ls 后"提示符+文件行"拼接错乱)。
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
	reset := "\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?25h\x1b[?2004l"
	if !bytes.Contains(got, []byte(reset)) {
		t.Fatalf("prompt reset sequence not found in bash output: %q",
			truncate(got, 300))
	}
	if bytes.Contains(got, []byte("\x1b[?1049l")) {
		t.Fatalf("prompt reset must NOT leave the alternate screen: %q",
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
