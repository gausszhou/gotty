package api

import (
	"testing"
)

// TestThemeColorFgBg 验证页面主题 → COLORFGBG 映射:
// 浅色 = 黑字白底(0;15),其余(深色/未传/未知)一律深色白字黑底(15;0)。
func TestThemeColorFgBg(t *testing.T) {
	cases := []struct {
		theme string
		want  string
	}{
		{"light", "0;15"},
		{"dark", "15;0"},
		{"", "15;0"},
		{"unknown", "15;0"},
	}
	for _, c := range cases {
		if got := themeColorFgBg(c.theme); got != c.want {
			t.Errorf("themeColorFgBg(%q) = %q, want %q", c.theme, got, c.want)
		}
	}
}

// TestCreateSessionAcceptsTheme 验证创建接口透传 theme 字段不报错
// (浅色/深色/缺省三种场景都能正常创建会话)。
func TestCreateSessionAcceptsTheme(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	for _, theme := range []string{"light", "dark", ""} {
		body := `{"command":"sh","args":["-c","sleep 30"]}`
		if theme != "" {
			body = `{"command":"sh","args":["-c","sleep 30"],"theme":"` + theme + `"}`
		}
		created := createSession(t, ts, body)
		if created["state"] != "idle" {
			t.Fatalf("theme=%q: unexpected state: %v", theme, created["state"])
		}
	}
}
