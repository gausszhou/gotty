package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// createSessionWithID posts an explicit client id; expectedStatus asserts
// the response status (200 idempotent hit, 201 fresh create).
func createSessionWithID(t *testing.T, ts *httptest.Server, id, body string, expectedStatus int) map[string]interface{} {
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
	if resp.StatusCode != expectedStatus {
		t.Fatalf("unexpected status: %d (want %d), body: %v", resp.StatusCode, expectedStatus, result)
	}
	return result
}

// postStatus queries the liveness of the given ids.
func postStatus(t *testing.T, ts *httptest.Server, ids []string) sessionStatusResponse {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"ids": ids})
	resp, err := http.Post(ts.URL+"/api/sessions/status", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to post status: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status code: %d", resp.StatusCode)
	}
	var out sessionStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("failed to decode status: %s", err)
	}
	return out
}

func TestRESTLifecycle(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	// create
	created := createSession(t, ts, `{"command":"sh","args":["-c","sleep 30"]}`)
	id := created["id"].(string)
	if created["state"] != "idle" {
		t.Fatalf("unexpected state: %v", created["state"])
	}

	// alive check via the status endpoint (list endpoint no longer exists)
	status := postStatus(t, ts, []string{id, "zzzzzzzzzzzzzzzz"})
	if _, ok := status.Sessions[id]; !ok {
		t.Fatal("created session missing from status")
	}
	if _, ok := status.Sessions["zzzzzzzzzzzzzzzz"]; ok {
		t.Fatal("unknown session must not be reported alive")
	}

	// get
	resp, err := http.Get(ts.URL + "/api/sessions/" + id)
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

// dialMultiplex opens a multiplexed websocket to /ws (no session_id).
func dialMultiplex(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols: terminal.Protocols,
	})
	if err != nil {
		t.Fatalf("failed to dial websocket: %s", err)
	}
	return conn
}

// sendRoute sends one routed frame over the multiplexed connection.
func sendRoute(t *testing.T, conn *websocket.Conn, sid string, typ byte, payload []byte) {
	t.Helper()
	frame, err := terminal.EncodeRouteFrame([]byte(sid), typ, payload)
	if err != nil {
		t.Fatalf("failed to encode route frame: %s", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
		t.Fatalf("failed to send route frame: %s", err)
	}
}

// readRoute reads the next binary message and decodes it as a routed frame.
func readRoute(t *testing.T, conn *websocket.Conn) (string, terminal.ClientMessage) {
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
	rsid, msg, err := terminal.DecodeRouteFrame(data)
	if err != nil {
		t.Fatalf("failed to decode route frame: %s", err)
	}
	return string(rsid), msg
}

// readRouteUntil reads routed frames until one of the wanted type arrives
// (other types are skipped). Returns the session id and payload.
func readRouteUntil(t *testing.T, conn *websocket.Conn, wantType byte) (string, []byte) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sid, msg := readRoute(t, conn)
		if msg.Type == wantType {
			return sid, msg.Payload
		}
	}
	t.Fatalf("timed out waiting for route frame type %c", wantType)
	return "", nil
}

// TestMultiplexAttachE2E: one connection attaches a session, exchanges input
// and output, detaches, and the same session survives for a later re-attach.
func TestMultiplexAttachE2E(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	created := createSession(t, ts, `{"command":"cat"}`)
	id := created["id"].(string)

	conn := dialMultiplex(t, ts)
	defer conn.CloseNow()

	// 1. attach -> AttachOK, then the init frames (title + replay-done)
	sendRoute(t, conn, id, terminal.Attach, nil)
	if sid, msg := readRoute(t, conn); msg.Type != terminal.AttachOK || sid != id {
		t.Fatalf("unexpected attach frame sid=%q type=%c", sid, msg.Type)
	}
	// 消费 init 帧(标题 + 重放完成标记)再发输入
	for {
		sid, msg := readRoute(t, conn)
		if sid != id {
			t.Fatalf("init frame routed to wrong sid %q", sid)
		}
		if msg.Type == terminal.SetReplayDone {
			break
		}
	}

	// 2. send input, expect it echoed back on the SAME session channel
	sendRoute(t, conn, id, terminal.Input, []byte("hello\n"))

	got := ""
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(got, "hello") && time.Now().Before(deadline) {
		sid, msg := readRoute(t, conn)
		if sid != id {
			t.Fatalf("echo routed to wrong sid %q", sid)
		}
		if msg.Type != terminal.Output {
			continue
		}
		got += string(msg.Payload)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("expected echo of `hello` in output, got: %q", got)
	}

	// 3. detach; the session keeps running but returns to idle
	sendRoute(t, conn, id, terminal.Detach, nil)
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
		t.Fatalf("unexpected state after detach: %s", state.State)
	}

	// 4. re-attach from a fresh connection proves the session survived
	conn2 := dialMultiplex(t, ts)
	defer conn2.CloseNow()
	sendRoute(t, conn2, id, terminal.Attach, nil)
	if _, msg := readRoute(t, conn2); msg.Type != terminal.AttachOK {
		t.Fatalf("unexpected reattach frame type `%c`", msg.Type)
	}
}

// TestMultiplexMultipleSessions: a single connection carries two sessions
// whose input/output are isolated by session id.
func TestMultiplexMultipleSessions(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	c1 := createSession(t, ts, `{"command":"cat"}`)
	c2 := createSession(t, ts, `{"command":"cat"}`)
	id1 := c1["id"].(string)
	id2 := c2["id"].(string)

	conn := dialMultiplex(t, ts)
	defer conn.CloseNow()

	sendRoute(t, conn, id1, terminal.Attach, nil)
	if _, msg := readRoute(t, conn); msg.Type != terminal.AttachOK {
		t.Fatalf("unexpected attach1 frame type `%c`", msg.Type)
	}
	readRouteUntil(t, conn, terminal.SetReplayDone)
	sendRoute(t, conn, id2, terminal.Attach, nil)
	if _, msg := readRoute(t, conn); msg.Type != terminal.AttachOK {
		t.Fatalf("unexpected attach2 frame type `%c`", msg.Type)
	}
	readRouteUntil(t, conn, terminal.SetReplayDone)

	// input to session 1 must echo only on channel 1
	sendRoute(t, conn, id1, terminal.Input, []byte("one\n"))
	sid, payload := readEchoRoute(t, conn, id1, "one")
	if sid != id1 {
		t.Fatalf("session 1 echo routed to %q", sid)
	}
	if !strings.Contains(string(payload), "one") {
		t.Fatalf("session 1 echo missing 'one': %q", payload)
	}

	// input to session 2 must echo only on channel 2
	sendRoute(t, conn, id2, terminal.Input, []byte("two\n"))
	sid, payload = readEchoRoute(t, conn, id2, "two")
	if sid != id2 {
		t.Fatalf("session 2 echo routed to %q", sid)
	}
	if !strings.Contains(string(payload), "two") {
		t.Fatalf("session 2 echo missing 'two': %q", payload)
	}
}

// readEchoRoute reads frames until the expected text shows up in Output
// frames of the wanted session and returns that frame. Unlike the framing
// assertions above it must tolerate stray frames: after SetReplayDone the
// attach-time SIGWINCH jitter (jitterSize) can make the PTY emit a repaint
// burst (Git Bash's winpty re-emits the init sequence on resize), and the
// echo may be split across several chunks. Frames of other sessions are
// ignored so routing isolation is still what is asserted.
func readEchoRoute(t *testing.T, conn *websocket.Conn, sid, want string) (string, []byte) {
	t.Helper()
	acc := ""
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rsid, msg := readRoute(t, conn)
		if msg.Type != terminal.Output {
			continue
		}
		if rsid != sid {
			continue // 另一会话的迟到重绘帧,与本次回显断言无关
		}
		acc += string(msg.Payload)
		if strings.Contains(acc, want) {
			return rsid, msg.Payload
		}
	}
	t.Fatalf("timed out waiting for echo %q on session %q (got %q)", want, sid, acc)
	return "", nil
}

// TestMultiplexPreemptsSession: a second connection attaching the same
// session preempts the first; the first receives an 'E' event (0x01).
func TestMultiplexPreemptsSession(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	created := createSession(t, ts, `{"command":"cat"}`)
	id := created["id"].(string)

	conn1 := dialMultiplex(t, ts)
	defer conn1.CloseNow()
	sendRoute(t, conn1, id, terminal.Attach, nil)
	if _, msg := readRoute(t, conn1); msg.Type != terminal.AttachOK {
		t.Fatalf("unexpected attach frame type `%c`", msg.Type)
	}
	readRouteUntil(t, conn1, terminal.SetReplayDone)

	// a second connection attaches the same session -> conn1 is preempted
	conn2 := dialMultiplex(t, ts)
	defer conn2.CloseNow()
	sendRoute(t, conn2, id, terminal.Attach, nil)

	// conn1 receives an 'E' event with status 0x01 (preempted) on its channel
	sid, payload := readRouteUntil(t, conn1, terminal.Event)
	if sid != id {
		t.Fatalf("event routed to wrong sid %q", sid)
	}
	if len(payload) != 1 || payload[0] != terminal.EventPreempted {
		t.Fatalf("expected preempted event, got payload %v", payload)
	}

	// conn2 now owns the session: AttachOK + init frames
	if _, msg := readRoute(t, conn2); msg.Type != terminal.AttachOK {
		t.Fatalf("unexpected preempting frame type `%c`", msg.Type)
	}
}

// TestMultiplexMissingSession: attaching an unknown session is rejected with
// an AttachErr route frame (not a connection close).
func TestMultiplexMissingSession(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	conn := dialMultiplex(t, ts)
	defer conn.CloseNow()
	sendRoute(t, conn, "zzzzzzzzzzzzzzzz", terminal.Attach, nil)

	sid, msg := readRoute(t, conn)
	if msg.Type != terminal.AttachErr {
		t.Fatalf("unexpected frame type `%c`", msg.Type)
	}
	if sid != "zzzzzzzzzzzzzzzz" {
		t.Fatalf("AttachErr routed to wrong sid %q", sid)
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

// TestCreateWithClientIDIdempotent: 同一客户端 id 重复创建 → 幂等,返回同一会话。
func TestCreateWithClientIDIdempotent(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	const id = "a000000000000000" // 16 base36 chars

	first := createSessionWithID(t, ts, id, `{"id":"`+id+`","command":"sh","args":["-c","sleep 30"]}`, http.StatusCreated)
	second := createSessionWithID(t, ts, id, `{"id":"`+id+`","command":"sh","args":["-c","sleep 30"]}`, http.StatusOK)

	if first["id"] != id || second["id"] != id {
		t.Fatalf("session id mismatch: %v vs %v", first["id"], second["id"])
	}
	if first["pid"].(float64) != second["pid"].(float64) {
		t.Fatalf("idempotent create must return the same session, pids differ: %v vs %v", first["pid"], second["pid"])
	}

	// 幂等命中不额外创建:第三次仍是同一会话
	thrice := createSessionWithID(t, ts, id, `{"id":"`+id+`","command":"sh","args":["-c","sleep 30"]}`, http.StatusOK)
	if thrice["pid"].(float64) != first["pid"].(float64) {
		t.Fatal("repeated idempotent create must keep returning the same session")
	}
}

// TestCreateWithClientIDResurrect: 销毁后凭同 id 重建 → 复活(记录命令),run_count+1。
func TestCreateWithClientIDResurrect(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	const id = "a000000000000001"

	created := createSessionWithID(t, ts, id, `{"id":"`+id+`","command":"sh","args":["-c","sleep 30"]}`, http.StatusCreated)
	pid1 := created["pid"].(float64)
	_ = created

	// 销毁后记录仍在服务端
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to destroy: %s", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unexpected destroy status: %d", resp.StatusCode)
	}
	if st := postStatus(t, ts, []string{id}); len(st.Sessions) != 0 {
		t.Fatal("destroyed session must not be alive")
	}

	// 重跑:同 id 复活,新的 pid,记录命令保留(sh -c sleep 30)
	resurrected := createSessionWithID(t, ts, id, `{"id":"`+id+`"}`, http.StatusCreated)
	if resurrected["id"] != id {
		t.Fatalf("resurrected session id mismatch: %v", resurrected["id"])
	}
	if resurrected["pid"].(float64) == pid1 {
		t.Fatal("resurrected session must be a new process")
	}
	if resurrected["command"] != "sh" {
		t.Fatalf("resurrected session must use the recorded command, got: %v", resurrected["command"])
	}
}

// TestCreateWithClientIDRejectsBadFormat: 非法 id 格式 → 400。
func TestCreateWithClientIDRejectsBadFormat(t *testing.T) {
	ts, _ := newTestServer(t, nil)

	for _, bad := range []string{"", "short", "UPPERCASE00000000", "invalid-char-00000", "too-long-0000000000000"} {
		if bad == "" {
			continue // 空 id 合法(服务端生成)
		}
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions",
			strings.NewReader(`{"id":"`+bad+`","command":"sh"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("failed to create with bad id: %s", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for id %q, got %d", bad, resp.StatusCode)
		}
	}
}

// TestSessionStatusBatch: status 只返回存活的清单 id,顺序无关。
func TestSessionStatusBatch(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	const (
		ida = "a00000000000000a"
		idb = "a00000000000000b"
	)

	createSessionWithID(t, ts, ida, `{"id":"`+ida+`","command":"sh","args":["-c","sleep 30"]}`, http.StatusCreated)
	createSessionWithID(t, ts, idb, `{"id":"`+idb+`","command":"sh","args":["-c","sleep 30"]}`, http.StatusCreated)

	st := postStatus(t, ts, []string{idb, ida, "dead000000000000"})
	if len(st.Sessions) != 2 {
		t.Fatalf("expected 2 alive sessions, got %d", len(st.Sessions))
	}
	if _, ok := st.Sessions[ida]; !ok {
		t.Fatal("ida must be alive")
	}
	if _, ok := st.Sessions[idb]; !ok {
		t.Fatal("idb must be alive")
	}
	if _, ok := st.Sessions["dead000000000000"]; ok {
		t.Fatal("dead id must not be reported")
	}
}

// getTitle fetches GET /api/title and returns the decoded title.
func getTitle(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/title")
	if err != nil {
		t.Fatalf("failed to GET /api/title: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status for GET /api/title: %d", resp.StatusCode)
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode GET /api/title response: %s", err)
	}
	return body.Title
}

// putTitle sends PUT /api/title and expects the given status; the decoded
// title is returned when status == OK.
func putTitle(t *testing.T, ts *httptest.Server, body string, wantStatus int) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/title", strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build PUT /api/title request: %s", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to PUT /api/title: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("unexpected status for PUT /api/title: %d (want %d)", resp.StatusCode, wantStatus)
	}
	var result struct {
		Title string `json:"title"`
	}
	if wantStatus == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode PUT /api/title response: %s", err)
		}
	}
	return result.Title
}

func TestPageTitleUnsetByDefault(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	if got := getTitle(t, ts); got != "" {
		t.Fatalf("expected empty page title by default, got %q", got)
	}
}

func TestPageTitleRoundTrip(t *testing.T) {
	titleFile := filepath.Join(t.TempDir(), "title.json")
	ts, _ := newTestServer(t, func(o *Options) { o.TitleFile = titleFile })

	// 保存:值会被 trim,并原样返回
	if got := putTitle(t, ts, `{"title":"  我的终端室  "}`, http.StatusOK); got != "我的终端室" {
		t.Fatalf("expected trimmed title, got %q", got)
	}
	if got := getTitle(t, ts); got != "我的终端室" {
		t.Fatalf("expected saved title, got %q", got)
	}

	// 空值 = 清除
	if got := putTitle(t, ts, `{"title":""}`, http.StatusOK); got != "" {
		t.Fatalf("expected empty title after clear, got %q", got)
	}
	if got := getTitle(t, ts); got != "" {
		t.Fatalf("expected empty page title after clear, got %q", got)
	}

	// 超长被截断到 maxPageTitleLen
	long := strings.Repeat("x", maxPageTitleLen+50)
	if got := putTitle(t, ts, `{"title":"`+long+`"}`, http.StatusOK); len(got) != maxPageTitleLen {
		t.Fatalf("expected title truncated to %d chars, got %d", maxPageTitleLen, len(got))
	}

	// 非法 JSON → 400
	putTitle(t, ts, `not json`, http.StatusBadRequest)
}

func TestPageTitlePersistsAcrossRestart(t *testing.T) {
	titleFile := filepath.Join(t.TempDir(), "title.json")

	ts1, _ := newTestServer(t, func(o *Options) { o.TitleFile = titleFile })
	putTitle(t, ts1, `{"title":"ops console"}`, http.StatusOK)
	ts1.Close()

	// 新实例加载同一文件:标题仍在
	ts2, _ := newTestServer(t, func(o *Options) { o.TitleFile = titleFile })
	if got := getTitle(t, ts2); got != "ops console" {
		t.Fatalf("expected persisted title after restart, got %q", got)
	}
}
