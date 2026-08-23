package capture

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"
	"time"
)

// findChrome locates a usable Chrome/Chromium binary for the browser engine.
func findChrome() string {
	for _, p := range []string{
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/opt/google/chrome/chrome",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func TestBrowserEngineText(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome/Chromium binary found; browser engine not exercised")
	}

	res, err := RunBrowser(BrowserOptions{
		Command:     "/bin/sh",
		Args:        []string{"-c", "printf 'browser engine works'"},
		Cols:        60,
		Rows:        15,
		WaitMs:      100,
		Timeout:     30 * time.Second,
		BrowserPath: chrome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopExit {
		t.Errorf("stop reason = %s, want exit", res.StopReason)
	}
	if len(res.PNG) < 8 || res.PNG[0] != 0x89 || res.PNG[1] != 'P' {
		t.Fatalf("screenshot is not a PNG (len=%d)", len(res.PNG))
	}

	// 内容非纯黑:真实字体渲染了文本
	img, err := png.Decode(bytes.NewReader(res.PNG))
	if err != nil {
		t.Fatal(err)
	}
	bounds := img.Bounds()
	nonBlack := 0
	for y := 0; y < bounds.Dy(); y += 4 {
		for x := 0; x < bounds.Dx(); x += 4 {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			if r>>8 > 0x20 || g>>8 > 0x20 || b>>8 > 0x20 {
				nonBlack++
			}
		}
	}
	if nonBlack == 0 {
		t.Error("screenshot is entirely black: text did not render")
	}
}

func TestBrowserEngineIIPImage(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome/Chromium binary found; browser engine not exercised")
	}

	raw := testPNGBytes(t, 18, 18, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	name := base64.StdEncoding.EncodeToString([]byte("px.png"))
	seq := fmt.Sprintf("\033]1337;File=name=%s;size=%d;inline=1:%s\007",
		name, len(raw), base64.StdEncoding.EncodeToString(raw))

	res, err := RunBrowser(BrowserOptions{
		Command:     "/bin/sh",
		Args:        []string{"-c", fmt.Sprintf("printf '%s'", seq)},
		Cols:        40,
		Rows:        10,
		WaitMs:      200,
		Timeout:     30 * time.Second,
		BrowserPath: chrome,
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(res.PNG))
	if err != nil {
		t.Fatal(err)
	}
	// 图片从 (0,0) 格开始渲染成一行:第一格中心应为纯红
	r, g, b, _ := img.At(4, 9).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 {
		t.Errorf("image pixel = (%d,%d,%d), want red (255,0,0)", r>>8, g>>8, b>>8)
	}
}

func TestBrowserEngineMarker(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome/Chromium binary found; browser engine not exercised")
	}

	res, err := RunBrowser(BrowserOptions{
		Command:     "/bin/sh",
		Args:        []string{"-c", "printf 'quick marker'; sleep 2"},
		Cols:        40,
		Rows:        10,
		Marker:      "marker",
		WaitMs:      0,
		Timeout:     30 * time.Second,
		BrowserPath: chrome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopMarker {
		t.Errorf("stop reason = %s, want marker", res.StopReason)
	}
}

func TestBrowserEngineWaitMs(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome/Chromium binary found; browser engine not exercised")
	}

	res, err := RunBrowser(BrowserOptions{
		Command:     "/bin/sh",
		Args:        []string{"-c", "printf 'a'; sleep 0.4; printf 'b'; sleep 2"},
		Cols:        40,
		Rows:        10,
		WaitMs:      150,
		Timeout:     30 * time.Second,
		BrowserPath: chrome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != StopQuiet {
		t.Errorf("stop reason = %s, want quiet", res.StopReason)
	}
	if res.Duration > 2*time.Second {
		t.Errorf("quiet should trigger before the second printf (took %v)", res.Duration)
	}
}

var _ = image.Rect // silence unused import when image pkg not otherwise used
