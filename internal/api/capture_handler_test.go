package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gausszhou/gotty/internal/terminal"
)

// M4 attached 引擎的 REST 验收:无附着者 409、请求/应答全链路(含分片)、
// latest 回读、未知会话 404。

// capHTTPResult is a non-t.Fatal HTTP result so the blocking capture call can
// run in a goroutine (t.Fatal must only be called from the test goroutine).
type capHTTPResult struct {
	status int
	body   []byte
	err    error
}

func capDo(method, url, body string) capHTTPResult {
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return capHTTPResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return capHTTPResult{err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return capHTTPResult{err: err}
	}
	return capHTTPResult{status: resp.StatusCode, body: data}
}

func TestCaptureEndpointsE2E(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	created := createSession(t, ts, `{"command":"cat"}`)
	id := created["id"].(string)
	captureURL := ts.URL + "/api/sessions/" + id + "/capture"

	// 1. 没有附着者:合成发生在浏览器,服务端只能 409。
	if res := capDo(http.MethodPost, captureURL, `{"timeout_ms":200}`); res.err != nil {
		t.Fatalf("capture without client: %v", res.err)
	} else if res.status != http.StatusConflict {
		t.Fatalf("capture without client = %d, want 409 (%s)", res.status, res.body)
	}

	// 2. 附着一条多路复用通道(充当真人浏览器)。
	conn := dialMultiplex(t, ts)
	defer conn.CloseNow()
	sendRoute(t, conn, id, terminal.Attach, nil)
	if _, msg := readRoute(t, conn); msg.Type != terminal.AttachOK {
		t.Fatalf("unexpected attach frame type %q", msg.Type)
	}
	for {
		_, msg := readRoute(t, conn)
		if msg.Type == terminal.SetReplayDone {
			break
		}
	}

	// 3. 触发截图:HTTP 请求会阻塞到客户端回传为止,放到 goroutine 里。
	done := make(chan capHTTPResult, 1)
	go func() { done <- capDo(http.MethodPost, captureURL, `{"timeout_ms":5000}`) }()

	sid, payload := readRouteUntil(t, conn, terminal.CaptureRequest)
	if sid != id {
		t.Fatalf("capture request routed to %q, want %q", sid, id)
	}
	if _, err := terminal.DecodeCaptureRequest(payload); err != nil {
		t.Fatalf("capture request payload: %v", err)
	}

	// 4. 回传分片结果(真实客户端按 24000 字符切片,这里切成两片即可)。
	png := []byte("fake-png-bytes")
	b64 := base64.StdEncoding.EncodeToString(png)
	chunks := []string{b64[:4], b64[4:]}
	for seq, chunk := range chunks {
		payload, err := json.Marshal(terminal.CaptureResultFrame{
			Seq: seq, Total: len(chunks), Data: chunk, Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatalf("encode result: %v", err)
		}
		sendRoute(t, conn, id, terminal.CaptureResult, payload)
	}

	var res capHTTPResult
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("capture HTTP request did not return after the client answered")
	}
	if res.err != nil {
		t.Fatalf("capture: %v", res.err)
	}
	if res.status != http.StatusOK {
		t.Fatalf("capture status = %d, want 200 (%s)", res.status, res.body)
	}
	var out struct {
		PNG  string `json:"png"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatalf("decode capture response: %v", err)
	}
	if out.PNG != b64 {
		t.Errorf("png = %q, want the assembled %q", out.PNG, b64)
	}
	if out.Cols != 80 || out.Rows != 24 {
		t.Errorf("geometry = %dx%d, want 80x24", out.Cols, out.Rows)
	}

	// 5. latest 回读:同一次结果,不再触发新请求。
	latest := capDo(http.MethodGet, ts.URL+"/api/sessions/"+id+"/captures/latest", "")
	if latest.err != nil {
		t.Fatalf("latest: %v", latest.err)
	}
	if latest.status != http.StatusOK {
		t.Fatalf("latest status = %d, want 200 (%s)", latest.status, latest.body)
	}
	var latestOut struct {
		PNG string `json:"png"`
	}
	if err := json.Unmarshal(latest.body, &latestOut); err != nil {
		t.Fatalf("decode latest: %v", err)
	}
	if latestOut.PNG != b64 {
		t.Errorf("latest png = %q, want %q", latestOut.PNG, b64)
	}

	// 6. 未知会话:404。
	if res := capDo(http.MethodPost, ts.URL+"/api/sessions/nosuchsession0000/capture", `{}`); res.status != http.StatusNotFound {
		t.Errorf("capture on unknown session = %d, want 404", res.status)
	}
}
