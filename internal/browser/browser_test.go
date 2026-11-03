//go:build browser_e2e

// 浏览器引擎端到端测试:驱动真实 headless Chrome 渲染页面并截图。
// 带 `browser_e2e` 标签,默认 `go test ./...`(CI 单测)不含本文件;
// 本机需有 Chrome/Chromium 时用 `go test -tags browser_e2e ./internal/browser/`
// 或 `make test-browser` 运行。这类测试对 Chrome 启动耗时敏感,不宜进 CI。
package browser

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gausszhou/gotty/internal/capture"
)

// newBrowserTestServer boots an ephemeral gotty server for the browser
// engine and registers its shutdown.
func newBrowserTestServer(t *testing.T) string {
	t.Helper()
	base, shutdown, err := NewEmbeddedServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(shutdown)
	return base
}

// testPNGBytes builds a solid-color PNG (mirrors the helper in capture).
func testPNGBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// findChrome locates a usable Chrome/Chromium binary for the browser engine.
//
// Windows 之前完全没覆盖:候选只有 POSIX 路径,于是 `make test-browser` 在
// Windows 上永远走 SKIP(表现为"测试通过",实际一次都没跑过浏览器引擎)。
// 这里按环境变量拼 Windows 的安装位置,并把 Edge(同为 Chromium 内核,
// chromedp 直接可用)作为没有 Chrome 时的兜底。
func findChrome() string {
	candidates := []string{
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/opt/google/chrome/chrome",
	}
	for _, dir := range []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("LOCALAPPDATA"),
	} {
		if dir == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(dir, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(dir, `Microsoft\Edge\Application\msedge.exe`),
		)
	}
	for _, p := range candidates {
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
	base := newBrowserTestServer(t)

	cmd, args := shellCmd(shellStep{write: "browser engine works"})
	res, err := RunBrowser(base, BrowserOptions{
		Command:     cmd,
		Args:        args,
		Cols:        60,
		Rows:        15,
		WaitMs:      100,
		Timeout:     30 * time.Second,
		BrowserPath: chrome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != capture.StopExit {
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
	base := newBrowserTestServer(t)

	raw := testPNGBytes(t, 18, 18, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	name := base64.StdEncoding.EncodeToString([]byte("px.png"))
	seq := fmt.Sprintf("\033]1337;File=name=%s;size=%d;inline=1:%s\007",
		name, len(raw), base64.StdEncoding.EncodeToString(raw))

	cmd, args := shellCmd(shellStep{write: seq})
	res, err := RunBrowser(base, BrowserOptions{
		Command:     cmd,
		Args:        args,
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
	base := newBrowserTestServer(t)

	cmd, args := shellCmd(shellStep{write: "quick marker"}, shellStep{sleep: 2 * time.Second})
	res, err := RunBrowser(base, BrowserOptions{
		Command:     cmd,
		Args:        args,
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
	if res.StopReason != capture.StopMarker {
		t.Errorf("stop reason = %s, want marker", res.StopReason)
	}
}

func TestBrowserEngineWaitMs(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome/Chromium binary found; browser engine not exercised")
	}
	base := newBrowserTestServer(t)

	// 命令必须存活到页面附着之后:慢速 CI 上 Chrome 冷启动可达数秒,
	// 若命令先退出,exit 判定会先于 quiet 触发,测试就变成环境敏感的。
	// 10s 的尾部等待给出远超任何合理启动时间的窗口;quiet 在收到 'b' 后
	// 静默 150ms 即触发(0.4s lull 内),远早于 10s 后的退出。
	cmd, args := shellCmd(
		shellStep{write: "a"},
		shellStep{sleep: 400 * time.Millisecond},
		shellStep{write: "b"},
		shellStep{sleep: 10 * time.Second},
	)
	res, err := RunBrowser(base, BrowserOptions{
		Command:     cmd,
		Args:        args,
		Cols:        40,
		Rows:        10,
		WaitMs:      150,
		Timeout:     30 * time.Second,
		BrowserPath: chrome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != capture.StopQuiet {
		t.Errorf("stop reason = %s, want quiet", res.StopReason)
	}
	// quiet 在进程仍在运行(退出约 10.4s)时就应触发,证明判定对象是
	// "静默"而非"退出";9s 上界给慢启动留足余量(附着 < ~8.4s 即可)。
	if res.Duration > 9*time.Second {
		t.Errorf("quiet should trigger while the process is still running (took %v)", res.Duration)
	}
}

// TestBrowserEngineGIF 覆盖 M5 动画录制:判定循环内按帧率采样、停止后补收尾帧,
// 帧已是 ≤256 色的 Paletted(量化在采样时完成,不把原始 RGBA 留在缓冲里),
// 且编码出的 GIF 首末帧内容不同(证明真的录到了动画而不是同一张图重复)。
func TestBrowserEngineGIF(t *testing.T) {
	chrome := findChrome()
	if chrome == "" {
		t.Skip("no Chrome/Chromium binary found; browser engine not exercised")
	}
	base := newBrowserTestServer(t)

	res, err := RunBrowser(base, BrowserOptions{
		Command:     "/bin/sh",
		Args:        []string{"-c", "for i in 1 2 3 4 5; do printf '\\r%d' $i; sleep 0.15; done"},
		Cols:        60,
		Rows:        10,
		WaitMs:      300,
		Timeout:     30 * time.Second,
		BrowserPath: chrome,
		GIF:         &capture.GifOptions{FPS: 20, MaxFrames: 100, Optimize: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Frames) < 2 {
		t.Fatalf("frames = %d, want >= 2 (animation should be sampled)", len(res.Frames))
	}
	for i, f := range res.Frames {
		if len(f.Image.Palette) > 256 {
			t.Errorf("frame %d palette = %d, want <= 256", i, len(f.Image.Palette))
		}
		if f.Image.Bounds().Dx() != 60*9 || f.Image.Bounds().Dy() != 10*18 {
			t.Errorf("frame %d size = %v, want %dx%d", i, f.Image.Bounds(), 60*9, 10*18)
		}
	}

	var buf bytes.Buffer
	if err := capture.EncodeGIF(&buf, res.Frames, res.GIFEnd, true); err != nil {
		t.Fatal(err)
	}
	dec, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Image) < 2 {
		t.Fatalf("decoded frames = %d, want >= 2", len(dec.Image))
	}
	first := gifFrameRGBA(dec.Image[0])
	last := gifFrameRGBA(dec.Image[len(dec.Image)-1])
	if bytes.Equal(first.Pix, last.Pix) {
		t.Error("first and last GIF frames should differ (progress animation)")
	}
}

// gifFrameRGBA 把调色板帧还原成 RGBA,便于按像素比较内容。
func gifFrameRGBA(p *image.Paletted) *image.RGBA {
	b := p.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, p, b.Min, draw.Src)
	return dst
}
