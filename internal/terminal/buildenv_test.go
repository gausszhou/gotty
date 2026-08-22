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
// COLORTERM=truecolor、COLORFGBG=15;0(深色),且继承环境中
// 的颜色变量被剥离统一为本层默认值。
func TestBuildEnvDefaults(t *testing.T) {
	t.Setenv("TERM", "vt100")
	t.Setenv("COLORTERM", "1")
	t.Setenv("COLORFGBG", "7;0")
	t.Setenv("UNRELATED", "keep-me")

	env := buildEnv("/bin/bash", nil)

	if got := envValue(t, env, "TERM"); got != "xterm-256color" {
		t.Fatalf("TERM = %q, want xterm-256color", got)
	}
	if got := envValue(t, env, "COLORTERM"); got != "truecolor" {
		t.Fatalf("COLORTERM = %q, want truecolor", got)
	}
	if got := envValue(t, env, "COLORFGBG"); got != "15;0" {
		t.Fatalf("COLORFGBG = %q, want 15;0 (dark)", got)
	}
	if envValue(t, env, "UNRELATED") != "keep-me" {
		t.Fatal("unrelated inherited variable must be preserved")
	}
	if envHas(t, env, "vt100") {
		t.Fatal("inherited TERM value must be stripped (no duplicates)")
	}
}

// TestBuildEnvThemeOverride 验证 extras 中的 COLORFGBG/COLORTERM
// 覆盖默认值(浅色会话由服务端经 WithEnv 注入 "COLORFGBG=0;15")。
func TestBuildEnvThemeOverride(t *testing.T) {
	t.Setenv("COLORFGBG", "15;0")

	env := buildEnv("/bin/sh", []string{
		"COLORFGBG=0;15", // 浅色(黑字白底)
		"COLORTERM=24bit",
	})

	if got := envValue(t, env, "COLORFGBG"); got != "0;15" {
		t.Fatalf("COLORFGBG = %q, want 0;15 (light)", got)
	}
	if got := envValue(t, env, "COLORTERM"); got != "24bit" {
		t.Fatalf("COLORTERM = %q, want 24bit", got)
	}
}

// TestBuildEnvLaterOverrideWins 验证多个同类条目按序后者胜(服务端
// base 配置 env 在前、客户端主题 env 在后 → 主题优先)。
func TestBuildEnvLaterOverrideWins(t *testing.T) {
	env := buildEnv("/bin/sh", []string{
		"COLORFGBG=15;0",
		"COLORFGBG=0;15",
	})

	if got := envValue(t, env, "COLORFGBG"); got != "0;15" {
		t.Fatalf("COLORFGBG = %q, want the later value 0;15", got)
	}
}

// TestBuildEnvKeepsExtras 验证普通 env 条目原样保留并去重。
func TestBuildEnvKeepsExtras(t *testing.T) {
	env := buildEnv("/bin/sh", []string{"FOO=bar", "EDITOR=vim"})

	if envValue(t, env, "FOO") != "bar" || envValue(t, env, "EDITOR") != "vim" {
		t.Fatal("plain extras must be preserved")
	}
}
