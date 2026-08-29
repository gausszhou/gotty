package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gausszhou/gotty/internal/session"
)

// newAgentTestServer builds a test server for the agent-driving API.
// The manager's sweep loop is deliberately NOT started: a browser-engine
// style flow may attach/query a session long after its command exited,
// and the sweep would remove it (mirror keeps the final screen).
func newAgentTestServer(t *testing.T, mirror bool, permitWrite bool) (*httptest.Server, *session.Manager) {
	t.Helper()
	options := &Options{
		Address:        "127.0.0.1",
		Port:           "0",
		TitleFormat:    "test-title",
		PermitWrite:    permitWrite,
		DefaultArgs:    []string{},
		TitleVariables: map[string]interface{}{"hostname": "testhost"},
	}
	var manager *session.Manager
	if mirror {
		manager = session.NewManager(session.WithMirrorFactory(MirrorFactory(true)))
	} else {
		manager = session.NewManager()
	}

	srv, err := New(manager, options)
	if err != nil {
		t.Fatalf("failed to create server: %s", err)
	}
	ts := httptest.NewServer(srv.setupHandlers())
	t.Cleanup(ts.Close)
	return ts, manager
}

// doJSON posts a JSON body and decodes the JSON response.
func doJSON(t *testing.T, method, url, body string) (int, map[string]interface{}) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// createAgentSession creates a session running a shell command.
func createAgentSession(t *testing.T, ts *httptest.Server, shell string) string {
	t.Helper()
	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions",
		fmt.Sprintf(`{"command":"sh","args":["-c",%q],"width":40,"height":10}`, shell))
	if status != http.StatusCreated {
		t.Fatalf("create session: status %d, resp %v", status, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("session id must not be empty")
	}
	return id
}

func TestAgentWaitMatchesScreen(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := createAgentSession(t, ts, "printf 'hello agent'")

	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/wait",
		`{"regex":"hello agent","timeout_ms":3000}`)
	if status != http.StatusOK {
		t.Fatalf("wait: status %d, resp %v", status, resp)
	}
	if matched, _ := resp["matched"].(bool); !matched {
		t.Errorf("wait matched = %v, want true", resp["matched"])
	}
	if text, _ := resp["text"].(string); !strings.Contains(text, "hello agent") {
		t.Errorf("wait text = %q, want to contain hello agent", text)
	}
}

func TestAgentWaitQuietAndTimeout(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := createAgentSession(t, ts, "sleep 5")

	// quiet:输出静默 200ms 即返回
	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/wait",
		`{"quiet_ms":200,"timeout_ms":3000}`)
	if status != http.StatusOK {
		t.Fatalf("wait quiet: status %d, resp %v", status, resp)
	}
	if quiet, _ := resp["quiet"].(bool); !quiet {
		t.Errorf("wait quiet = %v, want true", resp["quiet"])
	}

	// timeout:永不匹配的正则
	status, resp = doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/wait",
		`{"regex":"never-appears","timeout_ms":200}`)
	if status != http.StatusOK {
		t.Fatalf("wait timeout: status %d, resp %v", status, resp)
	}
	if timedOut, _ := resp["timed_out"].(bool); !timedOut {
		t.Errorf("wait timed_out = %v, want true", resp["timed_out"])
	}
}

func TestAgentScreenFormats(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := createAgentSession(t, ts, "printf 'screen text 123'")

	// text(默认)
	waitForAgentText(t, ts, id, "screen text 123")
	status, body := doGet(t, ts.URL+"/api/sessions/"+id+"/screen")
	if status != http.StatusOK {
		t.Fatalf("screen text: status %d", status)
	}
	if !strings.Contains(body, "screen text 123") {
		t.Errorf("screen text = %q, want to contain screen text 123", body)
	}

	// json
	status, resp := doJSON(t, http.MethodGet, ts.URL+"/api/sessions/"+id+"/screen?format=json", "")
	if status != http.StatusOK {
		t.Fatalf("screen json: status %d", status)
	}
	if text, _ := resp["text"].(string); !strings.Contains(text, "screen text 123") {
		t.Errorf("screen json text = %q", text)
	}
	if resp["cols"] == nil || resp["rows"] == nil {
		t.Errorf("screen json missing cols/rows: %v", resp)
	}

	// png
	status, pngBody := doGet(t, ts.URL+"/api/sessions/"+id+"/screen?format=png")
	if status != http.StatusOK {
		t.Fatalf("screen png: status %d", status)
	}
	if len(pngBody) < 8 || pngBody[0] != 0x89 || pngBody[1] != 'P' {
		t.Errorf("screen png is not a PNG (len=%d)", len(pngBody))
	}
}

func TestAgentKeysEcho(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := createAgentSession(t, ts, "cat")

	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/keys",
		`{"input":"ping-123\n"}`)
	if status != http.StatusOK {
		t.Fatalf("keys: status %d, resp %v", status, resp)
	}
	if written, _ := resp["written"].(float64); written != 9 { // "ping-123"(8) + \n
		t.Errorf("written = %v, want 9", resp["written"])
	}

	// 输入经 PTY 回显到屏幕(镜像可读)
	waitForAgentText(t, ts, id, "ping-123")
}

func TestAgentKeysBase64(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := createAgentSession(t, ts, "cat")

	// "AB" 的 base64
	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/keys",
		`{"input":"QUI=","encoding":"base64"}`)
	if status != http.StatusOK {
		t.Fatalf("keys base64: status %d, resp %v", status, resp)
	}
	if written, _ := resp["written"].(float64); written != 2 {
		t.Errorf("written = %v, want 2", resp["written"])
	}
}

func TestAgentKeysForbiddenWhenReadOnly(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, false)
	id := createAgentSession(t, ts, "cat")

	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/keys", `{"input":"x"}`)
	if status != http.StatusForbidden {
		t.Errorf("keys status = %d (resp %v), want 403", status, resp)
	}
}

func TestAgentScreenDisabledMirror(t *testing.T) {
	ts, _ := newAgentTestServer(t, false, true)
	id := createAgentSession(t, ts, "cat")

	status, resp := doJSON(t, http.MethodGet, ts.URL+"/api/sessions/"+id+"/screen", "")
	if status != http.StatusServiceUnavailable {
		t.Errorf("screen status = %d (resp %v), want 503", status, resp)
	}
}

func TestAgentInvalidWaitRequest(t *testing.T) {
	ts, _ := newAgentTestServer(t, true, true)
	id := createAgentSession(t, ts, "cat")

	// 无 regex 也无 quiet_ms → 400
	status, _ := doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/wait", `{}`)
	if status != http.StatusBadRequest {
		t.Errorf("wait status = %d, want 400", status)
	}
	// 非法正则 → 400
	status, _ = doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/wait", `{"regex":"("}`)
	if status != http.StatusBadRequest {
		t.Errorf("wait regex status = %d, want 400", status)
	}
	// 未知 encoding → 400
	status, _ = doJSON(t, http.MethodPost, ts.URL+"/api/sessions/"+id+"/keys", `{"input":"x","encoding":"hex"}`)
	if status != http.StatusBadRequest {
		t.Errorf("keys encoding status = %d, want 400", status)
	}
}

// waitForAgentText polls the screen endpoint until it contains want.
func waitForAgentText(t *testing.T, ts *httptest.Server, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, body := doGet(t, ts.URL+"/api/sessions/"+id+"/screen")
		if status == http.StatusOK && strings.Contains(body, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never contained %q", want)
}

// doGet performs a GET and returns the status and raw body.
func doGet(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}
