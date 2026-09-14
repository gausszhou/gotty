package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"syscall"

	"github.com/coder/websocket"

	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
)

// signalByName maps a signal name to syscall.Signal.
func signalByName(name string) (syscall.Signal, bool) {
	signals := map[string]syscall.Signal{
		"SIGHUP":  syscall.SIGHUP,
		"SIGINT":  syscall.SIGINT,
		"SIGQUIT": syscall.SIGQUIT,
		"SIGKILL": syscall.SIGKILL,
		"SIGTERM": syscall.SIGTERM,
	}
	sig, ok := signals[name]
	return sig, ok
}

// handleWS terminates a single multiplexed WebSocket connection (see
// docs/design/ws-multiplex.md). One connection carries many logical channels,
// each scoped to a session by a 16-byte id in the routing header; an all-zero
// id is connection-level (heartbeat / RTT). The legacy per-session endpoint
// `/ws?session_id=xxx` has been removed — the browser is the only consumer and
// upgrades in lockstep (subprotocol `webtty` is unchanged).
//
// The read-only monitor channel (`?mode=mirror`) is a distinct single-session
// connection that must never preempt an attached client, so it is split off
// before the multiplexer takes over.
func (server *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if server.wsOriginMatcher != nil && !server.wsOriginMatcher.MatchString(r.Header.Get("Origin")) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: terminal.Protocols,
	})
	if err != nil {
		log.Printf("Failed to accept websocket from %s: %s", r.RemoteAddr, err)
		return
	}
	// coder/websocket 默认消息读限 32KB,超限直接 1009 断连 —— 浏览器端
	// 一次粘贴/大块输入会整帧上行,很容易超限。调大到 16MB(仍有界,防滥
	// 用);路由帧长度字段另有 64KiB 上限,输入/输出按 32KB 分块,不会触发。
	conn.SetReadLimit(16 << 20)

	// 只读监视通道(mode=mirror)不抢占、不写 PTY,必须在多路复用之前分流 ——
	// 一旦进入多路复用,Attach 会独占会话并把其他附着者(真人/agent)挤掉。
	if isMonitorRequest(r.URL.Query()) {
		sess, err := server.manager.Get(r.URL.Query().Get("session_id"))
		if err != nil {
			log.Printf("Monitor session not found for %s: %s", r.RemoteAddr, r.URL.Query().Get("session_id"))
			conn.Close(websocket.StatusPolicyViolation, "session not found")
			return
		}
		server.serveMonitorHandshake(r, conn, sess)
		return
	}

	server.serveMultiplex(r, conn)
}

// serveMultiplex drives one WebSocket connection as a multiplexer: it reads
// routed frames and fans them out to the per-session virtual connections,
// each of which is an io.ReadWriter the session bridge talks to as if it were
// a dedicated socket.
func (server *Server) serveMultiplex(r *http.Request, conn *websocket.Conn) {
	router := &wsRouter{
		server: server,
		conn:   conn,
		ctx:    r.Context(),
		chans:  make(map[string]*virtualConn),
	}

	server.wsWG.Add(1)
	server.activeConns.Store(conn, struct{}{})
	defer func() {
		server.activeConns.Delete(conn)
		server.wsWG.Done()
		// 整条连接断开:所有逻辑通道回到 IDLE(各自桥接循环因 closedCh 解锁)
		router.closeAll()
		conn.CloseNow()
	}()

	for {
		frame, err := readBinaryMessage(router.ctx, conn)
		if err != nil {
			return
		}
		sid, msg, err := terminal.DecodeRouteFrame(frame)
		if err != nil {
			log.Printf("Invalid routed frame from %s: %s", r.RemoteAddr, err)
			continue
		}

		// 连接级消息(session id 全 0):心跳保活 + RTT 测量
		if terminal.IsConnectionLevel(sid) {
			switch msg.Type {
			case terminal.Ping:
				router.writeConnLevel(terminal.Pong, nil)
			}
			continue
		}

		sidStr := string(sid)
		switch msg.Type {
		case terminal.Attach:
			router.handleAttach(router.ctx, sidStr, r.RemoteAddr)
		case terminal.Detach:
			router.handleDetach(sidStr)
		default:
			// 会话级帧(Input / ResizeTerminal 等):投递到对应通道队列
			router.deliver(sidStr, msg)
		}
	}
}

// readBinaryMessage reads the next binary WebSocket message, skipping any
// non-binary noise. It returns an error (ending the connection loop) when the
// underlying read fails.
func readBinaryMessage(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
	for {
		typ, reader, err := conn.Reader(ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageBinary {
			io.Copy(io.Discard, reader)
			continue
		}
		return io.ReadAll(reader)
	}
}

// wsRouter owns the single WebSocket connection and the set of logical
// channels multiplexed over it. chans maps a session id to its virtualConn;
// writeMu serializes writes to the connection (independent of chans so the
// read loop can push output without deadlocking on a channel lookup).
type wsRouter struct {
	server *Server
	conn   *websocket.Conn
	ctx    context.Context

	mu      sync.Mutex
	chans   map[string]*virtualConn
	writeMu sync.Mutex
}

// handleAttach binds a logical channel to a session. It is idempotent on a
// single connection (a repeat Attach for an already-attached sid just gets
// AttachOK, no second bridge); a second attach from a *different* connection
// preempts the first via Session.Attach's ownership handshake.
func (r *wsRouter) handleAttach(ctx context.Context, sid, remoteAddr string) {
	r.mu.Lock()
	if _, ok := r.chans[sid]; ok {
		r.mu.Unlock()
		r.writeRoute(sid, terminal.AttachOK, nil)
		return
	}
	r.mu.Unlock()

	sess, err := r.server.manager.Get(sid)
	if err != nil || sess.State() == session.StateDestroyed {
		r.writeRoute(sid, terminal.AttachErr, []byte("session not found"))
		return
	}

	opts := session.AttachOptions{
		PermitWrite:      r.server.options.PermitWrite,
		FixedCols:        r.server.options.Width,
		FixedRows:        r.server.options.Height,
		WindowTitle:      r.server.attachWindowTitle(sess, remoteAddr),
		ReconnectSeconds: r.server.reconnectSeconds(),
		Preferences:      r.server.preferencesJSON(),
	}

	vc := newVirtualConn(r, sid)
	r.mu.Lock()
	r.chans[sid] = vc
	r.mu.Unlock()

	// 先确认附着,init 帧(标题/偏好/重放)随后经该通道推送
	r.writeRoute(sid, terminal.AttachOK, nil)

	go func() {
		attachErr := sess.Attach(vc.ctx, vc, opts)
		// 桥接结束:若本 vc 仍是注册通道(未被新附着/分离替换),摘除之。
		r.mu.Lock()
		if r.chans[sid] == vc {
			delete(r.chans, sid)
		}
		r.mu.Unlock()
		// 解锁该通道上可能阻塞的读(会话已在抢占/销毁时经 CloseWithReason 通知)
		vc.detach()
		_ = attachErr
	}()
}

// handleDetach releases a logical channel without destroying the session.
func (r *wsRouter) handleDetach(sid string) {
	r.mu.Lock()
	vc, ok := r.chans[sid]
	if ok {
		delete(r.chans, sid)
	}
	r.mu.Unlock()
	if ok {
		vc.detach() // 正常分离,不发送 'E' 事件帧
	}
}

// deliver routes a session-scoped frame to the attached channel's queue.
func (r *wsRouter) deliver(sid string, msg terminal.ClientMessage) {
	r.mu.Lock()
	vc := r.chans[sid]
	r.mu.Unlock()
	if vc == nil {
		return
	}
	vc.push(msg)
}

// writeRoute wraps a session message in a routed frame and writes it to the
// single connection. sid must be exactly RouteSessionIDLen bytes.
func (r *wsRouter) writeRoute(sid string, typ byte, payload []byte) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	sidBytes := []byte(sid)
	if len(sidBytes) != terminal.RouteSessionIDLen {
		// 防御:真实 id 恰为 16 字节;不足则补零,过长则截断。
		buf := make([]byte, terminal.RouteSessionIDLen)
		copy(buf, sidBytes)
		sidBytes = buf
	}
	frame, err := terminal.EncodeRouteFrame(sidBytes, typ, payload)
	if err != nil {
		log.Printf("drop route frame (sid=%s type=%c): %s", sid, typ, err)
		return
	}
	if err := r.conn.Write(r.ctx, websocket.MessageBinary, frame); err != nil {
		// 连接已断:读循环会自行退场并清理
		_ = err
	}
}

// writeConnLevel writes a connection-level (all-zero session id) frame.
func (r *wsRouter) writeConnLevel(typ byte, payload []byte) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	sid := make([]byte, terminal.RouteSessionIDLen) // 全 0 = 连接级
	frame, err := terminal.EncodeRouteFrame(sid, typ, payload)
	if err != nil {
		return
	}
	_ = r.conn.Write(r.ctx, websocket.MessageBinary, frame)
}

// closeAll detaches every logical channel; used when the whole connection
// drops so each session returns to IDLE rather than being stuck attached.
func (r *wsRouter) closeAll() {
	r.mu.Lock()
	vcs := make([]*virtualConn, 0, len(r.chans))
	for _, vc := range r.chans {
		vcs = append(vcs, vc)
	}
	r.chans = make(map[string]*virtualConn)
	r.mu.Unlock()
	for _, vc := range vcs {
		vc.detach()
	}
}

// virtualConn is the logical channel a session bridge talks to. It implements
// io.ReadWriter + frameReader (so the session's existing bridge pumps bytes
// unchanged) and CloseReasoner (so the server can report preemption/destroy).
//
//	Read/ReadMessage: drain session frames queued by the router's read loop.
//	Write: wrap [sid] routing header and write back on the single connection.
//	detach: normal close (no event); CloseWithReason: sends an 'E' event.
type virtualConn struct {
	router *wsRouter
	sid    string

	mu       sync.Mutex
	pending  []byte
	queue    chan []byte
	closed   bool
	closeErr error
	closedCh chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

func newVirtualConn(router *wsRouter, sid string) *virtualConn {
	ctx, cancel := context.WithCancel(router.ctx)
	return &virtualConn{
		router:   router,
		sid:      sid,
		queue:    make(chan []byte, 64),
		closedCh: make(chan struct{}),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Read serves the byte stream of queued session frames ([type][payload]).
// It blocks until the next frame is available or the channel is closed.
func (c *virtualConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if len(c.pending) > 0 {
			n := copy(p, c.pending)
			c.pending = c.pending[n:]
			c.mu.Unlock()
			return n, nil
		}
		if c.closed {
			err := c.closeErr
			c.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		c.mu.Unlock()

		select {
		case <-c.closedCh:
			c.mu.Lock()
			err := c.closeErr
			c.mu.Unlock()
			if err == nil {
				err = io.EOF
			}
			return 0, err
		case frame := <-c.queue:
			c.mu.Lock()
			c.pending = frame
			c.mu.Unlock()
		}
	}
}

// ReadMessage returns one complete session frame per call (the frameReader
// interface the session bridge relies on for "one WS message = one frame").
func (c *virtualConn) ReadMessage() ([]byte, error) {
	select {
	case <-c.closedCh:
		c.mu.Lock()
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return nil, err
	case frame := <-c.queue:
		return frame, nil
	}
}

// Write wraps a session frame ([type][payload]) with the routing header and
// emits it on the single connection.
func (c *virtualConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.router.writeRoute(c.sid, p[0], p[1:])
	return len(p), nil
}

// push enqueues one session frame for the bridge to read. It is a no-op once
// the channel is closed; it blocks (bounded) while the bridge drains the queue.
func (c *virtualConn) push(msg terminal.ClientMessage) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	frame := terminal.EncodeFrame(msg.Type, msg.Payload)
	select {
	case c.queue <- frame:
	case <-c.closedCh:
	}
}

// detach performs a normal channel close (no event frame): the client sent
// Detach, or the whole connection dropped.
func (c *virtualConn) detach() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = session.ErrClientClosed
	c.mu.Unlock()
	c.cancel()
	close(c.closedCh)
}

// Close implements io.Closer for a generic (preempted) teardown.
func (c *virtualConn) Close() error {
	return c.CloseWithReason(terminal.EventPreempted)
}

// CloseWithReason tears the channel down and notifies the client on the same
// WebSocket connection with an 'E' event frame (preempted vs destroyed), so
// the frontend can show the right dialog and avoid a preemption ping-pong.
func (c *virtualConn) CloseWithReason(reason byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.closeErr = session.ErrSessionPreempted
	if reason == terminal.EventDestroyed {
		c.closeErr = session.ErrSessionDestroyed
	}
	c.mu.Unlock()
	c.cancel()
	close(c.closedCh)
	c.router.writeRoute(c.sid, terminal.Event, []byte{reason})
	return nil
}

// reconnectSeconds returns the reconnect delay for clients, 0 = disabled.
func (server *Server) reconnectSeconds() int {
	if server.options.EnableReconnect {
		return server.options.ReconnectTime
	}
	return 0
}

// preferencesJSON marshals the server preferences, nil when unset.
func (server *Server) preferencesJSON() []byte {
	if server.options.Preferences == nil {
		return nil
	}
	data, err := json.Marshal(server.options.Preferences)
	if err != nil {
		log.Printf("Failed to marshal preferences: %s", err)
		return nil
	}
	return data
}

// wsConn adapts a websocket.Conn into an io.ReadWriter over binary messages,
// where each binary message is treated as one protocol frame. It is used by
// the read-only monitor channel, which never participates in multiplexing.
type wsConn struct {
	conn *websocket.Conn
	ctx  context.Context

	mu      sync.Mutex
	pending []byte
}

func (c *wsConn) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		typ, reader, err := c.conn.Reader(c.ctx)
		if err != nil {
			return 0, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return 0, err
		}
		c.pending = data
	}

	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

// ReadMessage returns one complete client message per call (frame-oriented).
func (c *wsConn) ReadMessage() ([]byte, error) {
	for {
		typ, reader, err := c.conn.Reader(c.ctx)
		if err != nil {
			return nil, err
		}
		if typ != websocket.MessageBinary {
			continue
		}
		return io.ReadAll(reader)
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.Write(c.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close 实现 io.Closer:被同 id 的新 attach 抢占时由 Session 调用,
// 以 1013 关闭帧优雅告知旧客户端(浏览器据此显示"已被其他客户端接管")。
func (c *wsConn) Close() error {
	return c.conn.Close(websocket.StatusTryAgainLater, "session preempted by another client")
}
