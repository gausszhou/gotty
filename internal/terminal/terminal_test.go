package terminal

import (
	"strings"
	"testing"
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

// TestIsBash 覆盖各平台下 bash 的写法。Windows 那条是本轮修的缺陷:
// 默认命令是 $SHELL,在 Windows 上是 `D:\...\Git\usr\bin\bash.exe`
// (反斜杠),原先只测 "/bash.exe" 后缀 → 匹配失败 → PROMPT_COMMAND 注入
// 被整段静默跳过。
func TestIsBash(t *testing.T) {
	yes := []string{
		"bash",
		"bash.exe",
		"BASH.EXE",
		"/bin/bash",
		"/usr/local/bin/bash",
		`D:\Program Files (x86)\Git\usr\bin\bash.exe`,
		`C:\Program Files\Git\bin\bash.exe`,
	}
	for _, command := range yes {
		if !isBash(command) {
			t.Errorf("isBash(%q) = false, want true", command)
		}
	}
	no := []string{
		"sh",
		"/bin/sh",
		"zsh",
		"bashful",
		"/bin/bash-5.2",
		`C:\Windows\System32\cmd.exe`,
		"",
	}
	for _, command := range no {
		if isBash(command) {
			t.Errorf("isBash(%q) = true, want false", command)
		}
	}
}

// TestBuildEnvBashPromptCommand 验证 bash 会话注入的 PROMPT_COMMAND
// 既去掉 Git Bash 提示符开头的空行,又保留终端模式复位序列;
// 且非 bash 不注入、用户显式设置时不覆盖。
func TestBuildEnvBashPromptCommand(t *testing.T) {
	for _, command := range []string{"/bin/bash", `D:\Program Files (x86)\Git\usr\bin\bash.exe`} {
		got := envValue(t, buildEnv(command, nil), "PROMPT_COMMAND")
		if got == "" {
			t.Fatalf("PROMPT_COMMAND not injected for %q", command)
		}
		// 去空行的部分:守卫 + 去掉首个换行转义。
		if !strings.Contains(got, "$PS1") || !strings.Contains(got, `\007\]\n`) {
			t.Errorf("PROMPT_COMMAND for %q lacks the guarded newline strip: %q", command, got)
		}
		if !strings.Contains(got, "${PS1/") {
			t.Errorf("PROMPT_COMMAND for %q does not strip PS1: %q", command, got)
		}
		// 终端模式复位序列必须还在。
		for _, seq := range []string{"?1000l", "?1002l", "?1003l", "?1006l", "?25h", "?2004l"} {
			if !strings.Contains(got, seq) {
				t.Errorf("PROMPT_COMMAND for %q lost the %s reset", command, seq)
			}
		}
		// 不得包含 ?1049l(会把光标拽回陈旧位置,见 buildEnv 注释)。
		if strings.Contains(got, "1049l") {
			t.Errorf("PROMPT_COMMAND for %q must not leave the alternate screen", command)
		}
	}

	if envHas(t, buildEnv("/bin/sh", nil), "PROMPT_COMMAND") {
		t.Error("PROMPT_COMMAND must not be injected for non-bash commands")
	}

	// 用户显式设置时以用户为准(既不覆盖,也不追加我们的复位序列)。
	env := buildEnv("/bin/bash", []string{"PROMPT_COMMAND=echo mine"})
	if got := envValue(t, env, "PROMPT_COMMAND"); got != "echo mine" {
		t.Errorf("PROMPT_COMMAND = %q, want the user's value", got)
	}
}
