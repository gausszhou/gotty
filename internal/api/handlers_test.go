package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
)

func newTestServer(t *testing.T, modify func(*Options)) (*httptest.Server, *session.Manager) {
	t.Helper()

	options := &Options{
		Address:        "127.0.0.1",
		Port:           "0",
		TitleFormat:    "test-title",
		PermitWrite:    true,
		DefaultArgs:    []string{},
		TitleVariables: map[string]interface{}{"hostname": "testhost"},
	}
	if modify != nil {
		modify(options)
	}

	manager := session.NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	t.Cleanup(cancel)

	srv, err := New(manager, options)
	if err != nil {
		t.Fatalf("failed to create server: %s", err)
	}

	ts := httptest.NewServer(srv.setupHandlers())
	t.Cleanup(ts.Close)
	return ts, manager
}

func createSession(t *testing.T, ts *httptest.Server, body string) map[string]interface{} {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %s", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %s", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("unexpected status: %d, body: %v", resp.StatusCode, result)
	}

	id, _ := result["id"].(string)
	if id == "" {
		t.Fatal("session id must not be empty")
	}
	return result
}

func TestRESTLifecycle(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	// create
	created := createSession(t, ts, `{"command":"sh","args":["-c","sleep 30"]}`)
	id := created["id"].(string)
	if created["state"] != "idle" {
		t.Fatalf("unexpected state: %v", created["state"])
	}

	// list
	resp, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("failed to list sessions: %s", err)
	}
	defer resp.Body.Close()
	var list listSessionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("failed to decode list: %s", err)
	}
	found := false
	for _, s := range list.Sessions {
		if s.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("created session missing from list")
	}

	// get
	resp, err = http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("failed to get session: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected get status: %d", resp.StatusCode)
	}

	// 404
	resp, err = http.Get(ts.URL + "/api/sessions/does-not-exist")
	if err != nil {
		t.Fatalf("failed to get session: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unexpected status for missing session: %d", resp.StatusCode)
	}

	// resize
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+id+"/resize", strings.NewReader(`{"width":100,"height":30}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to resize: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected resize status: %d", resp.StatusCode)
	}

	// signal
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+id+"/signal", strings.NewReader(`{"signal":"SIGKILL"}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to signal: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected signal status: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+id+"/signal", strings.NewReader(`{"signal":"SIGBOGUS"}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to signal: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unexpected status for unknown signal: %d", resp.StatusCode)
	}

	// delete
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to delete: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected delete status: %d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("failed to get deleted session: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted session must be gone, status: %d", resp.StatusCode)
	}
}

func TestRESTCreateWithoutCommand(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 without command, got: %d", resp.StatusCode)
	}

	// with a default command configured, an empty command uses it
	ts2, _ := newTestServer(t, func(o *Options) {
		o.DefaultCommand = "sh"
		o.DefaultArgs = []string{"-c", "sleep 30"}
	})
	defer ts2.Close()
	createSession(t, ts2, `{}`)
}

func TestSiteEndpoints(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	for _, path := range []string{"/", "/main.js", "/favicon.png"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("failed to GET %s: %s", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("unexpected status for %s: %d", path, resp.StatusCode)
		}
	}

	// 已被替代/移除的旧端点不再提供(token 相关与旧静态路径)
	for _, path := range []string{
		"/auth_token.js", "/config.js", "/api/config",
		"/gotty-bundle.js", "/js/gotty-bundle.js", "/css/index.css",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("failed to GET %s: %s", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("legacy endpoint %s must be gone, got %d", path, resp.StatusCode)
		}
	}
}

// dialWS opens a websocket to /ws?session_id=id with the webtty subprotocol.
func dialWS(t *testing.T, ts *httptest.Server, id string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?session_id=" + id
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: terminal.Protocols,
	})
	if err != nil {
		t.Fatalf("failed to dial websocket: %s", err)
	}
	return conn
}

// readFrame reads the next binary message with a deadline.
func readWSFrame(t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	typ, reader, err := conn.Reader(ctx)
	if err != nil {
		t.Fatalf("failed to read frame: %s", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("unexpected message type: %v", typ)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read frame payload: %s", err)
	}
	return data
}

func TestWSAttachE2E(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	created := createSession(t, ts, `{"command":"cat"}`)
	id := created["id"].(string)

	// 1. attach, receive the window title frame
	conn := dialWS(t, ts, id)
	defer conn.CloseNow()

	frame := readWSFrame(t, conn)
	if frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected first frame type `%c`", frame[0])
	}

	// 2. send input, expect it echoed back by the PTY
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, terminal.EncodeFrame(terminal.Input, []byte("hello\n"))); err != nil {
		t.Fatalf("failed to send input: %s", err)
	}

	got := ""
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(got, "hello") && time.Now().Before(deadline) {
		frame := readWSFrame(t, conn)
		if frame[0] != terminal.Output {
			continue
		}
		got += string(frame[1:])
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("expected echo of `hello` in output, got: %q", got)
	}

	// 3. close; the session detaches but keeps running
	conn.CloseNow()
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("failed to get session: %s", err)
	}
	defer resp.Body.Close()
	var state struct {
		State string `json:"state"`
	}
	json.NewDecoder(resp.Body).Decode(&state)
	if state.State != "idle" {
		t.Fatalf("unexpected state after disconnect: %s", state.State)
	}

	// 4. reconnect: the same session is still alive
	conn2 := dialWS(t, ts, id)
	defer conn2.CloseNow()

	frame = readWSFrame(t, conn2)
	if frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected reattach frame type `%c`", frame[0])
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	if err := conn2.Write(ctx2, websocket.MessageBinary, terminal.EncodeFrame(terminal.Input, []byte("world\n"))); err != nil {
		t.Fatalf("failed to send input: %s", err)
	}

	got = ""
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(got, "world") && time.Now().Before(deadline) {
		frame = readWSFrame(t, conn2)
		if frame[0] != terminal.Output {
			continue
		}
		got += string(frame[1:])
	}
	if !strings.Contains(got, "world") {
		t.Fatalf("expected echo of `world` in output, got: %q", got)
	}
}

func TestWSPreemptsSession(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	created := createSession(t, ts, `{"command":"cat"}`)
	id := created["id"].(string)

	conn := dialWS(t, ts, id)
	defer conn.CloseNow()
	if frame := readWSFrame(t, conn); frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected first frame type `%c`", frame[0])
	}

	// a second attach to the same session preempts the first one
	conn2 := dialWS(t, ts, id)
	defer conn2.CloseNow()

	// the old client is closed with TryAgainLater ("session preempted")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Reader(ctx); err == nil {
		t.Fatal("expected the preempted client to be closed")
	} else {
		var closeErr websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != websocket.StatusTryAgainLater {
			t.Fatalf("unexpected close error: %v", err)
		}
	}

	// the new client owns the session: it receives the init frames
	// (title + replay of anything printed so far)
	if frame := readWSFrame(t, conn2); frame[0] != terminal.SetWindowTitle {
		t.Fatalf("unexpected preempting frame type `%c`", frame[0])
	}
}

func TestWSMissingSession(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	conn := dialWS(t, ts, "no-such-session")
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := conn.Reader(ctx); err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestManagerSweepRemovesExitedSession(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	created := createSession(t, ts, `{"command":"sh","args":["-c","sleep 1"]}`)
	id := created["id"].(string)

	// wait for the process to exit and the manager sweep to remove it
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(ts.URL + "/api/sessions/" + id)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("exited session was not removed by the manager sweep")
}
