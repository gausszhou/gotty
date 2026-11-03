package api

// 0004 §4 的输入侧验收:条件括号粘贴的三个分支(§2.5)与 `mouse state` 的两种
// 形态(§2.1)。全部走真实 HTTP 端点 + 真实 PTY:模式状态是镜像从子进程的
// 输出里学到的,而不是测试直接塞进结构体,这样"条件包装"测的是线上语义。
//
// 本文件所有 helper 以 pst 为前缀:同一 package 有并行编写的测试,前缀避免
// 与别人的 helper / 类型重名。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// pstMouseBody 是 GET /api/sessions/{id}/mouse 的响应形状。
// 用结构体而不是 map[string]interface{},是为了让断言读起来像规范条文;
// "字段是否存在"另由原始 JSON 子串检查覆盖(见空会话用例)。
type pstMouseBody struct {
	Mirror         bool     `json:"mirror"`
	SessionID      string   `json:"session_id"`
	Supported      bool     `json:"supported"`
	Enabled        bool     `json:"enabled"`
	Mode           string   `json:"mode"`
	Modes          []int    `json:"modes"`
	BracketedPaste bool     `json:"bracketed_paste"`
	CursorCol      int      `json:"cursor_col"`
	CursorRow      int      `json:"cursor_row"`
	Held           []string `json:"held"`
	LastEvent      string   `json:"last_event"`
}

// pstPasteBody 是 POST /keys {"paste":...} 的响应形状。
// written 是真正写进 PTY 的字节数,因此它是判断"包没包标记"最直接的证据:
// 包装会恰好多出 ESC[200~ + ESC[201~ 共 12 字节。
type pstPasteBody struct {
	Written   int    `json:"written"`
	Mode      string `json:"mode"`
	Bracketed bool   `json:"bracketed"`
}

// pstMouseActionBody 是 POST /api/sessions/{id}/mouse 的响应形状。
type pstMouseActionBody struct {
	Written      int          `json:"written"`
	Col          int          `json:"col"`
	Row          int          `json:"row"`
	Action       string       `json:"action"`
	Button       string       `json:"button"`
	Mode         string       `json:"mode"`
	ResolvedFrom string       `json:"resolved_from"`
	Mouse        pstMouseBody `json:"mouse"`
}

// pstPasteDoJSON 发一个请求并把 JSON 响应解码进 out(nil 表示不关心响应体)。
// 与 package 里已有的 doJSON 的区别:它接受任意 body 值(由 json.Marshal
// 编码,多行文本不用手拼转义),并支持强类型解码。
func pstPasteDoJSON(t *testing.T, method, url string, body, out interface{}) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %s", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request %s %s: %s", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %s", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %s", method, url, err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %q: %s", method, url, raw, err)
		}
	}
	return resp.StatusCode
}

// pstPasteSession 创建一个 40x10 的 sh 会话并在用例结束时销毁它。
// 尺寸固定是为了让越界/坐标断言有确定边界;销毁是为了不留下 30s 的 sleep
// 子进程——manager 的 sweep 在测试里没有启动(见 newAgentTestServer 注释)。
func pstPasteSession(t *testing.T, ts *httptest.Server, shell string) string {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	status := pstPasteDoJSON(t, http.MethodPost, ts.URL+"/api/sessions", map[string]interface{}{
		"command": "sh",
		"args":    []string{"-c", shell},
		"width":   40,
		"height":  10,
	}, &created)
	if status != http.StatusCreated {
		t.Fatalf("create session with %q: status %d", shell, status)
	}
	if created.ID == "" {
		t.Fatal("session id must not be empty")
	}
	id := created.ID
	t.Cleanup(func() {
		req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
		if err != nil {
			return
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	})
	return id
}

// pstPasteMouseState 读取一次 `mouse state`;非 200 视为测试失败。
func pstPasteMouseState(t *testing.T, ts *httptest.Server, id string) pstMouseBody {
	t.Helper()
	var out pstMouseBody
	status := pstPasteDoJSON(t, http.MethodGet, ts.URL+"/api/sessions/"+id+"/mouse", nil, &out)
	if status != http.StatusOK {
		t.Fatalf("mouse state: status %d", status)
	}
	return out
}

// pstPasteWaitMouse 轮询 `mouse state` 直到 pred 成立。
// 必须轮询:镜像是在会话的输出泵里异步消化子进程输出的,POST /api/sessions
// 返回时 ESC[?2004h / ?1000h 未必已经被解析成状态。
func pstPasteWaitMouse(t *testing.T, ts *httptest.Server, id, what string, pred func(pstMouseBody) bool) pstMouseBody {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last pstMouseBody
	for time.Now().Before(deadline) {
		last = pstPasteMouseState(t, ts, id)
		if pred(last) {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("mouse state never reached %s; last = %+v", what, last)
	return last
}

// pstPasteSend 触发一次 paste,并返回响应。
func pstPasteSend(t *testing.T, ts *httptest.Server, id, text string, force bool) pstPasteBody {
	t.Helper()
	body := map[string]interface{}{"paste": text}
	if force {
		body["force_paste"] = true
	}
	var out pstPasteBody
	status := pstPasteDoJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/keys", body, &out)
	if status != http.StatusOK {
		t.Fatalf("paste (force=%v): status %d", force, status)
	}
	return out
}

// pstPasteWaitScreen 轮询 /screen 直到它包含 want,并返回整屏文本。
// 必须轮询:回显也要经输出泵异步进入镜像,写完立刻 GET 可能还看不到。
func pstPasteWaitScreen(t *testing.T, ts *httptest.Server, id, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		status, body := doGet(t, ts.URL+"/api/sessions/"+id+"/screen")
		if status == http.StatusOK {
			last = body
			if strings.Contains(body, want) {
				return body
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("screen never contained %q; last screen:\n%s", want, last)
	return last
}

// pstPasteCheckEcho 断言"应用实际收到的字节"在回显里的形态。
// PTY 默认 ECHO+ECHOCTL,ESC 会回显成 "^[";于是应用侧真实收到的
// ESC[200~ … ESC[201~ 会以 "^[[200~" / "^[[201~" 出现在镜像屏上——
// 这是不依赖 od/stty 的逐字节证据(镜像自己会把真 CSI 序列吞掉,所以才要
// 借回显观测)。probe 是粘贴文本的首行,用于确认标记确实夹住了正文;
// wantMarkers=false 时反过来要求屏上没有包装标记。
func pstPasteCheckEcho(t *testing.T, screen, probe string, wantMarkers bool) {
	t.Helper()
	start := strings.Index(screen, "^[[200~")
	end := strings.Index(screen, "^[[201~")
	if !wantMarkers {
		if start >= 0 || end >= 0 {
			t.Errorf("screen shows bracketed-paste markers although the app never asked for them:\n%s", screen)
		}
		return
	}
	if start < 0 {
		t.Fatalf("screen lacks the opening marker ^[[200~ (ECHOCTL rendering):\n%s", screen)
	}
	if end < 0 {
		t.Fatalf("screen lacks the closing marker ^[[201~ (ECHOCTL rendering):\n%s", screen)
	}
	body := strings.Index(screen, probe)
	if body < 0 {
		t.Fatalf("screen lacks the pasted text %q:\n%s", probe, screen)
	}
	if !(start < body && body < end) {
		t.Errorf("markers do not bracket the pasted text (open=%d text=%d close=%d):\n%s", start, body, end, screen)
	}
}

// TestPasteBracketedModeOffWritesRaw 覆盖 §2.5 的"未启用 → 原样写入"分支:
// 应用没开 ?2004 时包装标记只会变成对方屏幕上的垃圾字节。
func TestPasteBracketedModeOffWritesRaw(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := pstPasteSession(t, ts, "cat")

	// 前置条件:cat 从不打印 ?2004h,确认这条用例真的落在"未启用"分支上。
	if st := pstPasteMouseState(t, ts, id); st.BracketedPaste {
		t.Fatalf("precondition: cat session already has bracketed paste on: %+v", st)
	}

	text := "line one\nline two\n"
	resp := pstPasteSend(t, ts, id, text, false)
	if resp.Mode != "paste" {
		t.Errorf("mode = %q, want %q", resp.Mode, "paste")
	}
	if resp.Written != len(text) {
		t.Errorf("written = %d, want %d (raw write, no bracketed-paste markers)", resp.Written, len(text))
	}
	if resp.Bracketed {
		t.Error("bracketed = true, want false: a forced-less paste must not claim the app enabled ?2004")
	}
	// 一次粘贴不应改写镜像跟踪到的应用模式。
	if st := pstPasteMouseState(t, ts, id); st.BracketedPaste {
		t.Error("tracked bracketed_paste = true after raw paste, want false")
	}
	// 反向证据:回显里只有正文,没有包装标记——应用收到的就是纯文本。
	pstPasteCheckEcho(t, pstPasteWaitScreen(t, ts, id, "line one"), "line one", false)
}

// TestPasteForcePasteWrapsWithoutMode 覆盖"force_paste 强制包装"分支:
// 应用没开 ?2004,agent 仍显式要求按括号粘贴发送(tu --force 语义)。
func TestPasteForcePasteWrapsWithoutMode(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := pstPasteSession(t, ts, "cat")

	text := "alpha\nbeta\n"
	resp := pstPasteSend(t, ts, id, text, true)
	// ESC[200~ / ESC[201~ 各 6 字节:多出来的 12 字节就是两侧标记。
	if want := len(text) + 12; resp.Written != want {
		t.Errorf("written = %d, want %d (len(%d) + 12 marker bytes)", resp.Written, want, len(text))
	}
	// force 只作用于本次请求,不能让镜像误以为应用自己开启了 ?2004。
	if resp.Bracketed {
		t.Error("bracketed = true after forced paste, want false (tracked mode must stay off)")
	}
	if st := pstPasteMouseState(t, ts, id); st.BracketedPaste {
		t.Error("tracked bracketed_paste = true after forced paste, want false")
	}
	// force 真的把 ESC[200~/ESC[201~ 写进了 PTY:回显里能看到标记夹住正文。
	// 等结尾标记而不是开头标记:看到它就是全部字节已回显,避免只读到半截。
	pstPasteCheckEcho(t, pstPasteWaitScreen(t, ts, id, "^[[201~"), "alpha", true)
}

// TestPasteBracketedModeOnWraps 覆盖"镜像跟踪到 ?2004 → 包装"分支。
// 子进程自己打印 ?2004h 再保持存活,让镜像通过真实输出学到该模式。
func TestPasteBracketedModeOnWraps(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := pstPasteSession(t, ts, `printf '\033[?2004h'; sleep 30`)
	pstPasteWaitMouse(t, ts, id, "?2004h tracked", func(st pstMouseBody) bool {
		return st.BracketedPaste
	})

	text := "line one\nline two\n"
	resp := pstPasteSend(t, ts, id, text, false)
	if want := len(text) + 12; resp.Written != want {
		t.Errorf("written = %d, want %d (text + ESC[200~/ESC[201~ markers)", resp.Written, want)
	}
	if !resp.Bracketed {
		t.Error("bracketed = false, want true (the mirror tracked ?2004h)")
	}
	// 不只数字节:回显证明应用侧收到的正是 ESC[200~ … ESC[201~,且夹住正文。
	// 等结尾标记而不是开头标记:看到它就是全部字节已回显,避免只读到半截。
	pstPasteCheckEcho(t, pstPasteWaitScreen(t, ts, id, "^[[201~"), "line one", true)

	// 模式已开时 force 与自动判断叠加,仍只能包一层——双包会把标记当正文。
	forced := pstPasteSend(t, ts, id, text, true)
	if want := len(text) + 12; forced.Written != want {
		t.Errorf("forced written = %d, want %d (exactly one wrapper layer)", forced.Written, want)
	}
}

// TestMouseStateBeforeAnyMouseMode 覆盖 §4 的第一种形态:空会话(应用未开启
// 任何鼠标模式)必须给出 enabled:false 的可判定状态。
func TestMouseStateBeforeAnyMouseMode(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := pstPasteSession(t, ts, "cat")

	// 键必须显式出现:agent 靠它区分"应用没开鼠标"与"这个服务端不支持状态"。
	status, raw := doGet(t, ts.URL+"/api/sessions/"+id+"/mouse")
	if status != http.StatusOK {
		t.Fatalf("mouse state: status %d (body %s)", status, raw)
	}
	if !strings.Contains(raw, `"enabled":false`) {
		t.Errorf("mouse state body %s does not contain \"enabled\":false", raw)
	}

	var st pstMouseBody
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("decode mouse state %q: %s", raw, err)
	}
	if !st.Mirror {
		t.Error("mirror = false, want true")
	}
	if st.SessionID != id {
		t.Errorf("session_id = %q, want %q", st.SessionID, id)
	}
	// Supported 才是"镜像具备鼠标能力";Enabled 是"应用请求了鼠标"。
	if !st.Supported {
		t.Error("supported = false, want true for a mirror-backed session")
	}
	if st.Enabled {
		t.Error("enabled = true, want false: cat never enabled mouse reporting")
	}
	if st.Mode != "" {
		t.Errorf("mode = %q, want empty while disabled", st.Mode)
	}
	if len(st.Modes) != 0 {
		t.Errorf("modes = %v, want none while disabled", st.Modes)
	}
	if st.BracketedPaste {
		t.Error("bracketed_paste = true, want false")
	}
	if st.CursorCol != 0 || st.CursorRow != 0 {
		t.Errorf("cursor = (%d,%d), want (0,0) on a fresh session", st.CursorCol, st.CursorRow)
	}
	if len(st.Held) != 0 {
		t.Errorf("held = %v, want none", st.Held)
	}
	if st.LastEvent != "" {
		t.Errorf("last_event = %q, want empty on a fresh session", st.LastEvent)
	}
}

// TestMouseStateAfterReportModeEnabled 覆盖 §4 的第二种形态:应用开启
// ?1000h(普通鼠标上报)+ ?1006h(SGR 编码)后,mode/modes/held/虚拟光标都要对。
func TestMouseStateAfterReportModeEnabled(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	// 应用先声明鼠标模式,然后 sleep 保持 PTY 可写(不读取输入也无妨,写入只进
	// 内核缓冲)。镜像靠这段输出把状态点亮。
	id := pstPasteSession(t, ts, `printf '\033[?1000h\033[?1006h'; sleep 30`)
	st := pstPasteWaitMouse(t, ts, id, "mouse reporting enabled", func(s pstMouseBody) bool {
		return s.Enabled
	})
	if !st.Supported {
		t.Error("supported = false, want true")
	}
	if st.Mode != "sgr" {
		t.Errorf("mode = %q, want %q (?1006h selects SGR)", st.Mode, "sgr")
	}
	if !slices.Equal(st.Modes, []int{1000}) {
		t.Errorf("modes = %v, want [1000] (only the reporting mode we set)", st.Modes)
	}
	if st.CursorCol != 0 || st.CursorRow != 0 || len(st.Held) != 0 {
		t.Errorf("fresh state = (%d,%d) held %v, want (0,0) and none", st.CursorCol, st.CursorRow, st.Held)
	}

	// 按下:held 与虚拟光标由镜像合成,应同时反映在 POST 返回值里。
	var down pstMouseActionBody
	status := pstPasteDoJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/mouse", map[string]interface{}{
		"action": "down",
		"button": "left",
		"col":    3,
		"row":    2,
	}, &down)
	if status != http.StatusOK {
		t.Fatalf("mouse down: status %d", status)
	}
	if down.Written == 0 {
		t.Error("mouse down wrote 0 bytes, want the SGR press sequence")
	}
	if down.ResolvedFrom != "coordinates" {
		t.Errorf("resolved_from = %q, want %q", down.ResolvedFrom, "coordinates")
	}
	if down.Mode != "sgr" {
		t.Errorf("mode = %q, want %q", down.Mode, "sgr")
	}
	if down.Mouse.CursorCol != 3 || down.Mouse.CursorRow != 2 {
		t.Errorf("cursor = (%d,%d), want (3,2)", down.Mouse.CursorCol, down.Mouse.CursorRow)
	}
	if !slices.Equal(down.Mouse.Held, []string{"left"}) {
		t.Errorf("held = %v, want [left] while the button is down", down.Mouse.Held)
	}
	if down.Mouse.LastEvent != "down left@3,2" {
		t.Errorf("last_event = %q, want %q", down.Mouse.LastEvent, "down left@3,2")
	}

	// GET 必须读到同一份状态——mouse state 是 agent 在多次调用之间保持
	// "现在按着什么、鼠标在哪"的唯一依据。
	st = pstPasteMouseState(t, ts, id)
	if !st.Enabled || st.Mode != "sgr" {
		t.Errorf("state after down: enabled=%v mode=%q, want true/sgr", st.Enabled, st.Mode)
	}
	if !slices.Equal(st.Held, []string{"left"}) {
		t.Errorf("state after down: held = %v, want [left]", st.Held)
	}
	if st.CursorCol != 3 || st.CursorRow != 2 {
		t.Errorf("state after down: cursor = (%d,%d), want (3,2)", st.CursorCol, st.CursorRow)
	}

	// 抬起:不带坐标,复用虚拟光标;held 必须清空,光标留在原地。
	var up pstMouseActionBody
	status = pstPasteDoJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/mouse", map[string]interface{}{
		"action": "up",
	}, &up)
	if status != http.StatusOK {
		t.Fatalf("mouse up: status %d", status)
	}
	if len(up.Mouse.Held) != 0 {
		t.Errorf("held after up = %v, want none", up.Mouse.Held)
	}
	if up.Mouse.CursorCol != 3 || up.Mouse.CursorRow != 2 {
		t.Errorf("cursor after up = (%d,%d), want (3,2) (released in place)", up.Mouse.CursorCol, up.Mouse.CursorRow)
	}
	if up.Mouse.LastEvent != "up@3,2" {
		t.Errorf("last_event after up = %q, want %q", up.Mouse.LastEvent, "up@3,2")
	}
	if st = pstPasteMouseState(t, ts, id); len(st.Held) != 0 {
		t.Errorf("state after up: held = %v, want none", st.Held)
	}
}

// TestMouseStateUnavailableWithoutMirror 是 0004 §4 验收 3 的一小段:没有镜像时
// `mouse state` 必须 503,而不是回一个"看起来一切正常"的空状态。
func TestMouseStateUnavailableWithoutMirror(t *testing.T) {
	ts, _ := newAgentTestServer(t, false, true)
	id := pstPasteSession(t, ts, "cat")

	status, resp := doJSON(t, http.MethodGet, ts.URL+"/api/sessions/"+id+"/mouse", "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("mouse state status = %d (resp %v), want 503", status, resp)
	}
}
