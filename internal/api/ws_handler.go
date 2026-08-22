package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"

	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
)

// InitMessage is the first JSON frame the client sends after the
// WebSocket connection is established (subprotocol "webtty").
type InitMessage struct {
	Arguments string `json:"Arguments,omitempty"`
	AuthToken string `json:"AuthToken,omitempty"`
}

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

	server.wsWG.Add(1)
	server.activeConns.Store(conn, struct{}{})
	defer func() {
		server.activeConns.Delete(conn)
		server.wsWG.Done()
		conn.CloseNow()
	}()

	log.Printf("New client connected: %s", r.RemoteAddr)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// The first frame must be a binary init message carrying the auth token.
	typ, reader, err := conn.Reader(ctx)
	if err != nil {
		log.Printf("Failed to read init message from %s: %s", r.RemoteAddr, err)
		return
	}
	if typ != websocket.MessageBinary {
		conn.Close(websocket.StatusPolicyViolation, "init message must be binary")
		return
	}
	initData, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("Failed to read init message from %s: %s", r.RemoteAddr, err)
		return
	}
	var init InitMessage
	if err := json.Unmarshal(initData, &init); err != nil {
		conn.Close(websocket.StatusPolicyViolation, "invalid init message")
		return
	}
	if server.options.Credential != "" && init.AuthToken != server.options.Credential {
		conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}

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
	case session.ErrSessionBusy:
		conn.Close(websocket.StatusTryAgainLater, "session is already attached")
		closeReason = "session busy"
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

func (c *wsConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.Write(c.ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
