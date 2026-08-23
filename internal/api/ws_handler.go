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

// handleWS implements GET /ws?session_id=xxx — attach to an existing session.
// (无认证:连接建立后直接附着;多路复用协议见 docs/ws-multiplex.md)
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
	// 用),配合 ReadMessage() 按完整消息解析(见 masterToSlave 帧读取)。
	conn.SetReadLimit(16 << 20)

	server.wsWG.Add(1)
	server.activeConns.Store(conn, struct{}{})
	defer func() {
		server.activeConns.Delete(conn)
		server.wsWG.Done()
		conn.CloseNow()
	}()

	log.Printf("New client connected: %s", r.RemoteAddr)

	sess, err := server.manager.Get(r.URL.Query().Get("session_id"))
	if err != nil {
		log.Printf("Session not found for %s: %s", r.RemoteAddr, r.URL.Query().Get("session_id"))
		conn.Close(websocket.StatusPolicyViolation, "session not found")
		return
	}

	attachOpts := session.AttachOptions{
		PermitWrite:      server.options.PermitWrite,
		FixedCols:        server.options.Width,
		FixedRows:        server.options.Height,
		WindowTitle:      server.attachWindowTitle(sess, r.RemoteAddr),
		ReconnectSeconds: server.reconnectSeconds(),
		Preferences:      server.preferencesJSON(),
	}

	adapter := &wsConn{conn: conn, ctx: r.Context()}
	attachErr := sess.Attach(r.Context(), adapter, attachOpts)

	closeReason := "unknown reason"
	switch attachErr {
	case nil:
		closeReason = "finished"
	case session.ErrClientClosed:
		closeReason = "client"
	case session.ErrSessionPreempted:
		closeReason = "preempted"
	case terminal.ErrTerminalClosed:
		closeReason = "terminal"
	default:
		closeReason = attachErr.Error()
	}
	log.Printf("Connection closed by %s: %s, reason: %s", closeReason, r.RemoteAddr, sess.ID())
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
// where each binary message is treated as one protocol frame.
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
// 与 Read 的"字节流"语义不同,它保证一条 WebSocket 消息(一帧)不会被打散:
// masterToSlave 据此把"一次 Read = 一帧"的假设建立在真实边界上 ——
// 超过单次 Read 缓冲(32KB)的输入帧(如大粘贴)不再被拆成两个假帧
// (第二个假帧会因帧首字节不是类型字节而被误判/误解析)。
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
