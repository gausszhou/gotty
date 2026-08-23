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
