package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/gausszhou/gotty/internal/api"
	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/utils"
)

// logf is the browser engine's diagnostic logger (mirrors the server log).
func logf(format string, args ...interface{}) { log.Printf(format, args...) }

// BrowserOptions parameterizes a browser-engine capture.
type BrowserOptions struct {
	// Command/Args run in a new session (Shell syntax via `sh -c "..."`).
	Command string
	Args    []string

	// SessionID attaches the existing session instead of creating one.
	SessionID string

	// Cols/Rows fix the terminal and PTY size (defaults 120x30).
	Cols int
	Rows int

	// WaitMs captures when page output has been silent for that long.
	WaitMs int

	// Timeout bounds the whole run; on expiry the current screen is
	// returned with TimedOut set (0 = 30s default).
	Timeout time.Duration

	// Marker captures when this string appears in the rendered text tail.
	Marker string

	// BrowserPath reuses an existing Chrome/Chromium binary; empty looks
	// it up in PATH (google-chrome, chromium, …).
	BrowserPath string
}

// BrowserResult is the pixel-perfect rendering result from the real page.
type BrowserResult struct {
	PNG        []byte
	SessionID  string
	StopReason StopReason
	TimedOut   bool
	Duration   time.Duration
}

// RunBrowser renders the command through the real gotty web terminal in a
// headless Chrome and screenshots the terminal element: pixel-perfect text
// (real fonts, CJK, emoji) and graphics-protocol images, at the cost of a
// Chromium dependency and seconds of runtime. It runs an ephemeral gotty
// server bound to 127.0.0.1, drives the `/#/capture/:sid` render page, and
// cleans the session up afterwards.
func RunBrowser(opts BrowserOptions) (*BrowserResult, error) {
	cols, rows := opts.Cols, opts.Rows
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 30
	}
	if opts.WaitMs < 0 {
		opts.WaitMs = 0
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}

	// 1) 进程内临时 gotty server(只绑本机;会话管理在进程内)
	mgr := session.NewManager()
	apiSrv, err := api.New(mgr, &api.Options{
		Address:     "127.0.0.1",
		Port:        "0",
		PermitWrite: false,
		TitleFormat: "GoTTY capture",
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 不启动管理器的清扫循环(DestroyExpired):它会在一秒内把命令进程
	// 已退出的会话移出注册表。浏览器引擎恰恰要支持"进程退出后再附着"——
	// 命令通常在 headless Chrome 冷启动(数秒)完成前就执行完了,页面随后
	// 才 GET /api/sessions/:id 并附着 WS;环形缓冲 + bridge 重放让已退出
	// 的会话依然可附着、可重放输出,清扫器却会让页面拿到 404。
	// 临时 server 单会话、由下方的 defer deleteRemoteSession 显式清理,
	// 无需周期性清扫;真实 serve 的清扫语义不受影响。

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	httpSrv := &http.Server{Handler: apiSrv.SetupHandlers()}
	go func() { _ = httpSrv.Serve(listener) }()
	defer func() {
		_ = httpSrv.Close()
		_ = listener.Close()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// 2) 创建(或复用)会话
	sid := opts.SessionID
	if sid == "" {
		sid = utils.RandomString(16)
		if err := createRemoteSession(ctx, base, sid, opts.Command, opts.Args, cols, rows); err != nil {
			return nil, err
		}
	}
	defer func() {
		_ = deleteRemoteSession(context.Background(), base, sid)
	}()

	// 3) headless Chrome 打开渲染页
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
	)
	if opts.BrowserPath != "" {
		allocOpts = append(allocOpts, chromedp.ExecPath(opts.BrowserPath))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()
	pageCtx, cancelPage := chromedp.NewContext(allocCtx)
	defer cancelPage()

	start := time.Now()
	captureURL := fmt.Sprintf("%s/#/capture/%s?cols=%d&rows=%d", base, sid, cols, rows)
	logf("browser: navigate %s", captureURL)
	if err := chromedp.Run(pageCtx, chromedp.Navigate(captureURL)); err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}
	logf("browser: navigated, waiting for render-ready")

	// 4) 等待"渲染就绪"(页面收到握手标记);失败信号直接报错
	if err := waitFor(pageCtx, opts.Timeout, func(ctx context.Context) (bool, error) {
		var ready bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__gottyCaptureReady === true`, &ready)); err != nil {
			return false, err
		}
		if ready {
			return true, nil
		}
		var errMsg string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__gottyCaptureError || ''`, &errMsg)); err != nil {
			return false, err
		}
		if errMsg != "" {
			return false, fmt.Errorf("capture page: %s", errMsg)
		}
		return false, nil
	}); err != nil {
		return nil, err
	}
	time.Sleep(50 * time.Millisecond) // 让首帧渲染完成
	logf("browser: render ready, waiting for stop condition")

	// 5) 渲染稳定判定:退出(REST)/marker 与静默(页面状态)
	result := &BrowserResult{SessionID: sid}
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	timeoutTimer := time.NewTimer(opts.Timeout)
	defer timeoutTimer.Stop()
loop:
	for {
		select {
		case <-timeoutTimer.C:
			result.TimedOut = true
			result.StopReason = StopTimeout
			break loop
		case <-poll.C:
			exited, err := sessionExited(ctx, base, sid)
			if err != nil {
				return nil, err
			}
			if exited {
				result.StopReason = StopExit
				break loop
			}
			var activity int64
			var tail string
			if err := chromedp.Run(pageCtx,
				chromedp.Evaluate(`window.__gottyLastActivity || 0`, &activity),
				chromedp.Evaluate(`window.__gottyTextTail || ''`, &tail),
			); err != nil {
				return nil, err
			}
			if opts.Marker != "" && strings.Contains(tail, opts.Marker) {
				result.StopReason = StopMarker
				break loop
			}
			if opts.WaitMs > 0 && activity > 0 &&
				time.Since(time.UnixMilli(activity)) >= time.Duration(opts.WaitMs)*time.Millisecond {
				result.StopReason = StopQuiet
				break loop
			}
		}
	}

	// 6) 停止条件满足后,给图形协议图片的异步渲染留出时间(进程可能刚退出)
	time.Sleep(400 * time.Millisecond)

	// 6b) 视口对齐终端尺寸后整页截图(capture 页无 UI 杂项;compositor 截图
	// 不受 preserveDrawingBuffer 限制;元素级 Screenshot 在 headless 下不稳,
	// 故用视口定尺寸 + 全页截图)
	var shot []byte
	logf("browser: stop=%s, taking screenshot", result.StopReason)
	shotCtx, shotCancel := context.WithTimeout(pageCtx, 10*time.Second)
	defer shotCancel()
	if err := chromedp.Run(shotCtx,
		chromedp.EmulateViewport(int64(cols*9), int64(rows*18)),
		chromedp.CaptureScreenshot(&shot),
	); err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	logf("browser: screenshot %d bytes", len(shot))
	result.PNG = shot
	result.Duration = time.Since(start)
	return result, nil
}

// waitFor polls cond until it returns true, the deadline passes, or an error.
func waitFor(ctx context.Context, timeout time.Duration, cond func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, err := cond(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("capture page did not become ready within %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func createRemoteSession(ctx context.Context, base, sid, command string, args []string, cols, rows int) error {
	body, _ := json.Marshal(map[string]any{
		"id": sid, "command": command, "args": args,
		"width": cols, "height": rows,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/sessions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create session: %s", strings.TrimSpace(string(b)))
	}
	return nil
}

func sessionExited(ctx context.Context, base, sid string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/sessions/"+sid, nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return true, nil // 会话没了(被销毁/清理)
	}
	var info struct {
		Exited bool `json:"exited"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return false, err
	}
	return info.Exited, nil
}

func deleteRemoteSession(ctx context.Context, base, sid string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, base+"/api/sessions/"+sid, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
