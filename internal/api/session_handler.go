package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/gausszhou/gotty/internal/capture"
	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
	"github.com/gausszhou/gotty/internal/utils"
)

// Rest API — session management.
// 列表由客户端清单(localStorage)驱动;服务端只按 id 提供:
// 创建(幂等/复活)、详情、状态批量查询、销毁、重命名、resize/signal。

type createSessionRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
}

type sessionStatusResponse struct {
	// Sessions keyed by id, alive ones only.
	Sessions map[string]session.StateDescription `json:"sessions"`
}

// handleCreateSession implements POST /api/sessions.
// A client-chosen id (16 base36 chars) makes the call idempotent
// (alive → existing session) or resurrect the recorded session
// (record → rebuild with the recorded command, run_count+1).
// Without an id the server generates one (legacy clients).
func (server *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ID != "" && !utils.IsValidSessionID(req.ID) {
		writeError(w, http.StatusBadRequest, "invalid session id: must be 16 base36 characters")
		return
	}

	command := req.Command
	args := req.Args
	if command == "" {
		// 空命令统一回退到 CLI 默认命令(新建会话用)。
		// 复活会话由 CreateWithID 用记录命令重建,不受此回退影响。
		command = server.options.DefaultCommand
		args = server.options.DefaultArgs
	}

	var termOpts []terminal.Option
	if req.Width > 0 && req.Height > 0 {
		termOpts = append(termOpts, terminal.WithInitialSize(req.Width, req.Height))
	}

	sess, created, err := server.manager.CreateWithID(req.ID, command, args, termOpts...)
	if err != nil {
		switch err {
		case session.ErrTooManySessions:
			writeError(w, http.StatusServiceUnavailable, "too many sessions")
		case session.ErrNoCommand:
			writeError(w, http.StatusBadRequest, "no command given")
		default:
			log.Printf("Failed to create session: %s", err)
			writeError(w, http.StatusInternalServerError, "failed to create session")
		}
		return
	}

	status := http.StatusCreated
	if !created {
		// 幂等命中已有会话
		status = http.StatusOK
	}
	log.Printf("Session created: %s (%s %v)", sess.ID(), sess.Command(), sess.Args())
	writeJSON(w, status, sess.StateDescription())
}

// handleSessionStatus implements POST /api/sessions/status.
// The client manifest polls this to learn which of its ids are alive.
func (server *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp := sessionStatusResponse{
		Sessions: map[string]session.StateDescription{},
	}
	for _, sess := range server.manager.Status(req.IDs) {
		resp.Sessions[sess.ID()] = sess.StateDescription()
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUpdateTitle implements PUT /api/sessions/{id}/title
// (persisted on the server; works for alive and historical sessions).
func (server *Server) handleUpdateTitle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := server.manager.UpdateTitle(r.PathValue("id"), req.Title); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"title": req.Title})
}

// handleGetSession implements GET /api/sessions/{id}.
func (server *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, sess.StateDescription())
}

// handleDeleteSession implements DELETE /api/sessions/{id}.
func (server *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := server.manager.Destroy(id); err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	log.Printf("Session destroyed: %s", id)
	w.WriteHeader(http.StatusNoContent)
}

// handleResizeSession implements POST /api/sessions/{id}/resize.
func (server *Server) handleResizeSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Width <= 0 || req.Height <= 0 {
		writeError(w, http.StatusBadRequest, "width and height must be positive")
		return
	}

	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := sess.Resize(req.Width, req.Height); err != nil {
		writeError(w, http.StatusConflict, "session is not resizable")
		return
	}
	writeJSON(w, http.StatusOK, sess.StateDescription())
}

// handleSignalSession implements POST /api/sessions/{id}/signal.
func (server *Server) handleSignalSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Signal string `json:"signal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	sig, ok := signalByName(req.Signal)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown signal: "+req.Signal)
		return
	}

	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := sess.Signal(sig); err != nil {
		writeError(w, http.StatusConflict, "failed to send signal")
		return
	}
	writeJSON(w, http.StatusOK, sess.StateDescription())
}

// ---------------------------------------------------------------------------
// Agent-driving API: screen read / wait / keys.
// 由 serve 的屏幕镜像(默认开,--mirror=false 关闭)提供数据源,
// 让 AI agent 或脚本像 tu 一样驱动运行中的会话。

// screenResponse is the JSON form of one screen snapshot
// (GET /api/sessions/{id}/screen?format=json 与 wait 的返回体)。
type screenResponse struct {
	Mirror    bool                `json:"mirror"`
	SessionID string              `json:"session_id"`
	TakenAt   string              `json:"taken_at"`
	Cols      int                 `json:"cols"`
	Rows      int                 `json:"rows"`
	Cursor    capture.CursorJSON  `json:"cursor"`
	Text      string              `json:"text"`
	Cells     []capture.CellJSON  `json:"cells,omitempty"`
	Images    []capture.ImageJSON `json:"images,omitempty"`
}

// newScreenResponse renders a session snapshot into its wire form. The
// snapshot's Raw carries the *capture.Snapshot the mirror produced.
func newScreenResponse(id string, snap *session.ScreenSnapshot, withCells bool) (screenResponse, error) {
	raw, ok := snap.Raw.(*capture.Snapshot)
	if !ok {
		return screenResponse{}, fmt.Errorf("unexpected mirror snapshot type %T", snap.Raw)
	}
	resp := screenResponse{
		Mirror:    true,
		SessionID: id,
		TakenAt:   raw.TakenAt.Format(time.RFC3339),
		Cols:      raw.Cols,
		Rows:      raw.Rows,
		Cursor: capture.CursorJSON{
			Row:     raw.CursorRow,
			Col:     raw.CursorCol,
			Visible: raw.CursorVisible,
		},
		Text:   snap.Text,
		Images: capture.ImagesJSON(raw.Images),
	}
	if withCells {
		resp.Cells = raw.CellsJSON()
	}
	return resp, nil
}

// handleGetScreen implements GET /api/sessions/{id}/screen?format=text|json|png.
// Returns the rendered terminal screen at this moment: plain text
// (default), styled JSON cells (RenderDocument-shaped) or a PNG bitmap.
func (server *Server) handleGetScreen(w http.ResponseWriter, r *http.Request) {
	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	snap, err := sess.Screen()
	if err != nil {
		if errors.Is(err, session.ErrMirrorDisabled) {
			writeError(w, http.StatusServiceUnavailable, "screen mirror disabled (start gotty with --mirror)")
			return
		}
		log.Printf("Failed to read screen of %s: %s", sess.ID(), err)
		writeError(w, http.StatusInternalServerError, "failed to read screen")
		return
	}

	switch r.URL.Query().Get("format") {
	case "json":
		resp, err := newScreenResponse(sess.ID(), snap, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to render screen")
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case "png":
		raw, ok := snap.Raw.(*capture.Snapshot)
		if !ok {
			writeError(w, http.StatusInternalServerError, "failed to render screen")
			return
		}
		data, err := raw.PNG()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to render PNG")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	default: // text
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(snap.Text))
	}
}

// handleWaitSession implements POST /api/sessions/{id}/wait — the
// agent-driving "wait for screen state" primitive (tu wait 语义).
// Long-polls until the screen text matches regex, output stays silent
// for quiet_ms, or timeout_ms elapses; returns the screen at that moment.
func (server *Server) handleWaitSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Regex     string `json:"regex"`
		TimeoutMS int    `json:"timeout_ms"`
		QuietMS   int    `json:"quiet_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Regex == "" && req.QuietMS <= 0 {
		writeError(w, http.StatusBadRequest, "regex or quiet_ms required")
		return
	}
	var re *regexp.Regexp
	if req.Regex != "" {
		compiled, err := regexp.Compile(req.Regex)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid regex: "+err.Error())
			return
		}
		re = compiled
	}
	// 默认 30s 超时、上限 5 分钟,避免失控的长轮询占用连接。
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	quiet := time.Duration(req.QuietMS) * time.Millisecond

	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	snap, result, err := sess.Wait(r.Context(), re, timeout, quiet)
	if err != nil {
		if errors.Is(err, session.ErrMirrorDisabled) {
			writeError(w, http.StatusServiceUnavailable, "screen mirror disabled (start gotty with --mirror)")
			return
		}
		if errors.Is(err, context.Canceled) {
			return // 客户端断开,无需写响应
		}
		log.Printf("Wait on %s failed: %s", sess.ID(), err)
		writeError(w, http.StatusInternalServerError, "wait failed")
		return
	}

	resp, err := newScreenResponse(sess.ID(), snap, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to render screen")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		screenResponse
		Matched  bool `json:"matched"`
		Quiet    bool `json:"quiet"`
		TimedOut bool `json:"timed_out"`
	}{screenResponse: resp, Matched: result.Matched, Quiet: result.Quiet, TimedOut: result.TimedOut})
}

// handleKeys implements POST /api/sessions/{id}/keys — agent input
// injection: writes raw bytes into the PTY without an attached client
// (tu type/press 语义;命名键由 agent 直接发字节,服务端不做键名翻译)。
// Honor --permit-write: read-only deployments reject input with 403.
func (server *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	if !server.options.PermitWrite {
		writeError(w, http.StatusForbidden, "write is disabled (--permit-write=false)")
		return
	}
	var req struct {
		Input    string `json:"input"`
		Encoding string `json:"encoding"` // "text"(默认) | "base64"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Input == "" {
		writeJSON(w, http.StatusOK, map[string]int{"written": 0})
		return
	}
	payload := []byte(req.Input)
	switch req.Encoding {
	case "", "text":
		// UTF-8 文本原样写入
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(req.Input)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid base64 input")
			return
		}
		payload = decoded
	default:
		writeError(w, http.StatusBadRequest, "unknown encoding: "+req.Encoding)
		return
	}

	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := sess.Input(payload); err != nil {
		if errors.Is(err, session.ErrSessionDestroyed) {
			writeError(w, http.StatusConflict, "session is destroyed")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to write input")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"written": len(payload)})
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
