package api

// Tests for the read-only monitor channel (0005 §4): dirty-row correctness,
// zero cost with no subscribers, goroutine cleanup after a subscriber leaves,
// and the regression that `mode=mirror` never preempts a normal attach.
//
// The monitor is a pull loop: `serveMonitor` diffs the screen mirror on a tick
// and only touches the mirror while a subscriber is connected. That shapes
// what "zero cost" can mean here — the subscription goroutines and their
// buffers simply do not exist when nobody is subscribed — and it is why the
// dirty-row assertions diff a stable screen instead of racing a writer.

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
)

// monSkipUnlessShellAvailable keeps these tests hermetic: they drive a real
// PTY through `sh`, which Windows does not provide. The rest of the package
// already assumes a POSIX shell, so skipping only keeps this file portable.
func monSkipUnlessShellAvailable(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("monitor tests drive a POSIX shell over a PTY")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not found: %s", err)
	}
}

// monNewMonitorServer builds a test server whose sessions get a screen mirror
// (required by the monitor) and returns the *Server so the lifecycle tests can
// inspect activeConns / wsWG. It mirrors newAgentTestServer, which does not
// expose the Server itself.
func monNewMonitorServer(t *testing.T) (*httptest.Server, *session.Manager, *Server) {
	t.Helper()
	monSkipUnlessShellAvailable(t)

	options := &Options{
		Address:        "127.0.0.1",
		Port:           "0",
		TitleFormat:    "test-title",
		PermitWrite:    true,
		DefaultArgs:    []string{},
		TitleVariables: map[string]interface{}{"hostname": "testhost"},
	}
	manager := session.NewManager(session.WithMirrorFactory(MirrorFactory(true, 0)))

	srv, err := New(manager, options)
	if err != nil {
		t.Fatalf("failed to create server: %s", err)
	}
	ts := httptest.NewServer(srv.setupHandlers())
	t.Cleanup(ts.Close)
	return ts, manager, srv
}

// monActiveConns counts the WebSocket connections the server is tracking.
func monActiveConns(srv *Server) int {
	n := 0
	srv.activeConns.Range(func(_, _ interface{}) bool {
		n++
		return true
	})
	return n
}

// monMonitorGoroutines counts goroutines currently running the monitor
// subscription. Reading stacks is the only way to prove both halves of the
// pair (serveMonitor plus its monitorReadLoop reader) exited: wsWG only proves
// the HTTP handler returned, not that the detached reader goroutine is gone.
func monMonitorGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	count := 0
	for _, block := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(block, "internal/api.(*Server).serveMonitor") ||
			strings.Contains(block, "internal/api.monitorReadLoop") {
			count++
		}
	}
	return count
}

// monWaitFor polls a condition so asynchronous teardown has a bounded window.
func monWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// monWaitGroupDrained proves no WebSocket handler is still registered with the
// server. Add must not race with Wait, so callers invoke this only after every
// monitor connection has been closed.
func monWaitGroupDrained(t *testing.T, srv *Server) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		srv.wsWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wsWG did not drain: a monitor handler leaked")
	}
}

// monWaitMirrorIdle blocks until the mirror version stops moving. The
// dirty-row tests diff only a settled screen; otherwise a second in-flight
// output chunk could legitimately dirty another row and flake the assertion.
func monWaitMirrorIdle(t *testing.T, sess *session.Session) uint64 {
	t.Helper()
	var last uint64
	stable := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v := sess.MirrorVersion()
		if v == last {
			stable++
			if stable >= 3 {
				return v
			}
		} else {
			last, stable = v, 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("mirror version never stabilized (last=%d)", last)
	return last
}

// monWaitScreenText waits until a snapshot contains want.
func monWaitScreenText(t *testing.T, sess *session.Session, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snap, err := sess.Screen(); err == nil && strings.Contains(snap.Text, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("screen never contained %q", want)
}

// monInputLine writes one line and waits until the mirror has absorbed it.
func monInputLine(t *testing.T, sess *session.Session, line string) {
	t.Helper()
	if err := sess.Input([]byte(line + "\n")); err != nil {
		t.Fatalf("failed to write %q into the session: %s", line, err)
	}
	monWaitScreenText(t, sess, line)
	monWaitMirrorIdle(t, sess)
}

// monLineRows returns the row indices a diff frame carries, in order.
func monLineRows(frame terminal.MirrorDiffFrame) []int {
	rows := make([]int, len(frame.Lines))
	for i, l := range frame.Lines {
		rows[i] = l.Row
	}
	return rows
}

// monDialMonitor opens a read-only subscription. A monitor request is a plain
// single-session WebSocket, split off before the multiplexer.
func monDialMonitor(t *testing.T, ts *httptest.Server, id string, fps int) *websocket.Conn {
	t.Helper()
	url := fmt.Sprintf("ws%s/ws?session_id=%s&mode=mirror&fps=%d",
		strings.TrimPrefix(ts.URL, "http"), id, fps)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: terminal.Protocols,
	})
	if err != nil {
		t.Fatalf("failed to dial monitor: %s", err)
	}
	return conn
}

// monReadMirrorFrame reads one MirrorDiff frame from a monitor connection.
func monReadMirrorFrame(t *testing.T, conn *websocket.Conn) terminal.MirrorDiffFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	typ, reader, err := conn.Reader(ctx)
	if err != nil {
		t.Fatalf("failed to read monitor frame: %s", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("unexpected monitor message type: %v", typ)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read monitor payload: %s", err)
	}
	if len(data) == 0 || data[0] != terminal.MirrorDiff {
		t.Fatalf("unexpected monitor frame: %q", data)
	}
	frame, err := terminal.DecodeMirrorDiff(data[1:])
	if err != nil {
		t.Fatalf("failed to decode mirror diff: %s", err)
	}
	return frame
}

// monReadMirrorFramesUntilText collects monitor frames up to and including the
// first one whose lines carry want. Frames decoded on the way are returned so
// the caller can assert that *every* pushed frame obeyed the dirty-row rule.
func monReadMirrorFramesUntilText(t *testing.T, conn *websocket.Conn, want string) []terminal.MirrorDiffFrame {
	t.Helper()
	var frames []terminal.MirrorDiffFrame
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		frame := monReadMirrorFrame(t, conn)
		frames = append(frames, frame)
		for _, l := range frame.Lines {
			if strings.Contains(l.Text, want) {
				return frames
			}
		}
	}
	t.Fatalf("no monitor frame carried %q (got %d frames)", want, len(frames))
	return nil
}

// monReadAttachFrame reads one routed frame from a real attach connection and
// reports the WebSocket close status on failure: a preemption closes the old
// slot with 1013 (and a mirror mistake could surface as 1003/1007), so those
// statuses must fail with an explicit "was preempted" message.
func monReadAttachFrame(t *testing.T, conn *websocket.Conn) (string, terminal.ClientMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	typ, reader, err := conn.Reader(ctx)
	if err != nil {
		switch websocket.CloseStatus(err) {
		case websocket.StatusTryAgainLater,
			websocket.StatusInvalidFramePayloadData,
			websocket.StatusPolicyViolation,
			websocket.StatusUnsupportedData:
			t.Fatalf("attach closed with status %d while a monitor was online; mode=mirror must not preempt: %s",
				int(websocket.CloseStatus(err)), err)
		}
		t.Fatalf("failed to read attach frame: %s", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("unexpected attach message type: %v", typ)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read attach payload: %s", err)
	}
	sid, msg, err := terminal.DecodeRouteFrame(data)
	if err != nil {
		t.Fatalf("failed to decode routed frame: %s", err)
	}
	return string(sid), msg
}

// monExpectAttachOutput waits until the browser connection sees want in Output
// frames. Any Event (the multiplexer's preemption notification) or a 1013-style
// close is a hard failure: the attached browser must survive a monitor.
func monExpectAttachOutput(t *testing.T, conn *websocket.Conn, sid, want string) {
	t.Helper()
	acc := ""
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rsid, msg := monReadAttachFrame(t, conn)
		switch msg.Type {
		case terminal.Event:
			t.Fatalf("attach received event %v while a monitor was online; monitor must not preempt", msg.Payload)
		case terminal.Output:
			if rsid == sid {
				acc += string(msg.Payload)
			}
		}
		if strings.Contains(acc, want) {
			return
		}
	}
	t.Fatalf("attach never received %q (got %q)", want, acc)
}

// TestMonitorDirtyRowsReportOnlyChanges is the unit-level correctness check
// from 0005 §4: two adjacent writes may only put the rows that actually
// changed into the diff, and an idle screen may not carry any rows at all.
func TestMonitorDirtyRowsReportOnlyChanges(t *testing.T) {
	ts, manager, _ := monNewMonitorServer(t)
	// `read` consumes the line the line discipline already echoed without
	// printing anything itself. `cat` would duplicate every line (echo +
	// program output), dirtying two rows per write and hiding a broken diff.
	id := createAgentSession(t, ts, "read a; read b; sleep 30")
	sess, err := manager.Get(id)
	if err != nil {
		t.Fatalf("failed to get session: %s", err)
	}

	// 首帧必须是全量:客户端没有 baseline,只发脏行就重建不出屏幕。
	full, ok := buildMirrorDiff(sess, nil, true)
	if !ok {
		t.Fatal("buildMirrorDiff failed on a mirrored session")
	}
	if !full.Full {
		t.Fatalf("first frame must be full: %+v", full)
	}
	if len(full.Lines) != full.Rows {
		t.Fatalf("full frame carried %d rows, want all %d", len(full.Lines), full.Rows)
	}
	baseline := applyDiff(full, nil)

	monInputLine(t, sess, "alpha")
	partial, ok := buildMirrorDiff(sess, baseline, false)
	if !ok {
		t.Fatal("buildMirrorDiff failed after the first write")
	}
	if partial.Full {
		t.Fatal("a frame after the full snapshot must be partial")
	}
	if rows := monLineRows(partial); !reflect.DeepEqual(rows, []int{0}) {
		t.Fatalf("after writing alpha the diff carried rows %v, want only row 0", rows)
	}
	if partial.Lines[0].Text != "alpha" {
		t.Fatalf("row 0 text = %q, want alpha", partial.Lines[0].Text)
	}
	if partial.Version <= full.Version {
		t.Fatalf("version did not advance after output: %d -> %d", full.Version, partial.Version)
	}
	baseline = applyDiff(partial, baseline)

	monInputLine(t, sess, "beta")
	partial, ok = buildMirrorDiff(sess, baseline, false)
	if !ok {
		t.Fatal("buildMirrorDiff failed after the second write")
	}
	if rows := monLineRows(partial); !reflect.DeepEqual(rows, []int{1}) {
		t.Fatalf("after writing beta the diff carried rows %v, want only row 1", rows)
	}
	if partial.Lines[0].Text != "beta" {
		t.Fatalf("row 1 text = %q, want beta", partial.Lines[0].Text)
	}
	baseline = applyDiff(partial, baseline)

	// applyDiff 只打补丁:未出现在帧里的行必须原样保留。
	if baseline[0] != "alpha" || baseline[1] != "beta" {
		t.Fatalf("baseline after patching = %q, want rows 0/1 = alpha/beta", baseline[:2])
	}

	// 无变化时版本不变,serveMonitor 会直接跳过;即便构建帧也不得出现脏行。
	idleVersion := monWaitMirrorIdle(t, sess)
	none, ok := buildMirrorDiff(sess, baseline, false)
	if !ok {
		t.Fatal("buildMirrorDiff failed on an idle screen")
	}
	if len(none.Lines) != 0 {
		t.Fatalf("idle screen produced dirty rows: %v", none.Lines)
	}
	if none.Version != idleVersion || idleVersion != partial.Version {
		t.Fatalf("idle version = %d (mirror %d, last frame %d), want no advance",
			none.Version, idleVersion, partial.Version)
	}
}

// TestMonitorWebSocketPushesOnlyChangedRows runs the same correctness check
// over the real subscription: every partial frame pushed on the wire must only
// mention the row that the write changed.
func TestMonitorWebSocketPushesOnlyChangedRows(t *testing.T) {
	ts, manager, _ := monNewMonitorServer(t)
	id := createAgentSession(t, ts, "read a; read b; sleep 30")
	sess, err := manager.Get(id)
	if err != nil {
		t.Fatalf("failed to get session: %s", err)
	}

	conn := monDialMonitor(t, ts, id, monitorMaxFPS)
	defer conn.CloseNow()

	first := monReadMirrorFrame(t, conn)
	if !first.Full {
		t.Fatalf("first pushed frame must be full: %+v", first)
	}
	if len(first.Lines) != first.Rows {
		t.Fatalf("first pushed frame carried %d rows, want all %d", len(first.Lines), first.Rows)
	}

	monInputLine(t, sess, "alpha")
	for _, frame := range monReadMirrorFramesUntilText(t, conn, "alpha") {
		if frame.Full {
			t.Fatalf("monitor re-sent a full frame without a resize: %+v", frame)
		}
		for _, line := range frame.Lines {
			if line.Row != 0 {
				t.Fatalf("after writing alpha a frame touched row %d, want only row 0: %+v", line.Row, frame)
			}
		}
	}

	monInputLine(t, sess, "beta")
	for _, frame := range monReadMirrorFramesUntilText(t, conn, "beta") {
		if frame.Full {
			t.Fatalf("monitor re-sent a full frame without a resize: %+v", frame)
		}
		for _, line := range frame.Lines {
			if line.Row != 1 {
				t.Fatalf("after writing beta a frame touched row %d, want only row 1: %+v", line.Row, frame)
			}
		}
	}
}

// TestMonitorWithoutSubscribersStartsNoWork covers the "subscribers: 0 means
// zero cost" acceptance point. Because the monitor is pull-based, that cost is
// structural: with no subscriber there is no serveMonitor goroutine, no
// reader, no baseline slice and no connection bookkeeping — the write path
// keeps only the pre-existing mirror version bump. The test proves none of
// those appear while the session is actively producing output.
func TestMonitorWithoutSubscribersStartsNoWork(t *testing.T) {
	ts, manager, srv := monNewMonitorServer(t)
	id := createAgentSession(t, ts, "read a; sleep 30")
	sess, err := manager.Get(id)
	if err != nil {
		t.Fatalf("failed to get session: %s", err)
	}

	baseGoroutines := monMonitorGoroutines()
	if got := monActiveConns(srv); got != 0 {
		t.Fatalf("fresh server already tracks %d WebSocket connection(s)", got)
	}

	// 生产侧照常输出:镜像版本必须推进,但不该因此产生任何订阅侧对象。
	for i := 0; i < 5; i++ {
		if err := sess.Input([]byte(fmt.Sprintf("row-%d\n", i))); err != nil {
			t.Fatalf("failed to write row-%d: %s", i, err)
		}
	}
	monWaitScreenText(t, sess, "row-4")
	monWaitMirrorIdle(t, sess)
	if sess.MirrorVersion() == 0 {
		t.Fatal("mirror version did not advance; the exercise did not run")
	}

	if got := monMonitorGoroutines(); got > baseGoroutines {
		t.Fatalf("%d monitor goroutine(s) appeared while no subscriber was connected", got-baseGoroutines)
	}
	if got := monActiveConns(srv); got != 0 {
		t.Fatalf("%d connection(s) registered while no subscriber was connected", got)
	}
	monWaitGroupDrained(t, srv)
}

// TestMonitorDisconnectLeavesNoGoroutines covers the leak acceptance point the
// same way the server tracks connections: activeConns must be empty and wsWG
// drained, and the monitor's own reader goroutine must be gone too. The loop
// reconnects repeatedly so a leak accumulates instead of hiding in one run.
func TestMonitorDisconnectLeavesNoGoroutines(t *testing.T) {
	ts, _, srv := monNewMonitorServer(t)
	id := createAgentSession(t, ts, "read a; sleep 30")

	baseGoroutines := monMonitorGoroutines()
	if got := monActiveConns(srv); got != 0 {
		t.Fatalf("fresh server already tracks %d WebSocket connection(s)", got)
	}

	for i := 0; i < 3; i++ {
		conn := monDialMonitor(t, ts, id, monitorMaxFPS)
		if first := monReadMirrorFrame(t, conn); !first.Full {
			t.Fatalf("first frame of subscription %d is not full: %+v", i, first)
		}
		if got := monMonitorGoroutines(); got <= baseGoroutines {
			t.Fatal("monitor goroutines were not observed while connected; the leak check would be vacuous")
		}
		if got := monActiveConns(srv); got < 1 {
			t.Fatal("connected monitor is missing from activeConns")
		}
		// 用 CloseNow 模拟"订阅者突然消失"这一最苛刻的断开方式。
		_ = conn.CloseNow()
	}

	monWaitFor(t, "monitor goroutines and activeConns to drain", func() bool {
		return monMonitorGoroutines() == baseGoroutines && monActiveConns(srv) == 0
	})
	monWaitGroupDrained(t, srv)
}

// TestMonitorMirrorModeDoesNotPreemptAttach is the coexistence regression from
// 0005 §4: a human browser is attached and a monitor connects at the same time.
// The monitor must not take the attach slot (no Event preemption, no 1013/1007
// close), the browser must keep exchanging input, and the monitor must observe
// the same changes.
func TestMonitorMirrorModeDoesNotPreemptAttach(t *testing.T) {
	ts, manager, _ := monNewMonitorServer(t)
	id := createAgentSession(t, ts, "read a; read b; sleep 30")

	// 真人浏览器:多路复用连接上 attach 会话。
	browser := dialMultiplex(t, ts)
	defer browser.CloseNow()
	sendRoute(t, browser, id, terminal.Attach, nil)
	if _, msg := readRoute(t, browser); msg.Type != terminal.AttachOK {
		t.Fatalf("unexpected attach answer `%c`", msg.Type)
	}
	readRouteUntil(t, browser, terminal.SetReplayDone)

	sess, err := manager.Get(id)
	if err != nil {
		t.Fatalf("failed to get session: %s", err)
	}
	if state := sess.State(); state != session.StateRunning {
		t.Fatalf("attached session is in state %s, want running", state)
	}

	// monitor 同时在线:只订阅镜像,绝不能走 Attach(否则会抢占浏览器)。
	monitor := monDialMonitor(t, ts, id, monitorMaxFPS)
	defer monitor.CloseNow()
	if first := monReadMirrorFrame(t, monitor); !first.Full {
		t.Fatalf("monitor's first frame is not full: %+v", first)
	}

	// 浏览器在 monitor 在线时仍能收到自己的输入回显。
	sendRoute(t, browser, id, terminal.Input, []byte("mon-coexist\n"))
	monExpectAttachOutput(t, browser, id, "mon-coexist")
	if state := sess.State(); state != session.StateRunning {
		t.Fatalf("monitor changed the attach state to %s, want running", state)
	}

	// monitor 也确实收到了这次变化,证明两者是在并行观察同一会话。
	if frames := monReadMirrorFramesUntilText(t, monitor, "mon-coexist"); len(frames) == 0 {
		t.Fatal("monitor did not observe the browser's input")
	}

	// 第二次输入:若连接在第一次输出后被异步踢掉,这里就会暴露。
	sendRoute(t, browser, id, terminal.Input, []byte("mon-still\n"))
	monExpectAttachOutput(t, browser, id, "mon-still")

	// monitor 断开同样不能影响 attach,浏览器继续可用。
	_ = monitor.CloseNow()
	sendRoute(t, browser, id, terminal.Input, []byte("mon-after\n"))
	monExpectAttachOutput(t, browser, id, "mon-after")
}
