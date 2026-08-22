package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

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
	// Theme is the lighting of the creating device's page ("dark"|"light").
	// It is translated into the COLORFGBG env var of the PTY so that
	// programs inside (screen, vifm, …) render for the actual background.
	// Empty or unknown values fall back to the dark default.
	Theme string `json:"theme"`
}

// themeColorFgBg maps a page theme to the rxvt COLORFGBG value
// (foreground;background ANSI indices). Light = black on white,
// anything else (dark/unset) = white on black.
func themeColorFgBg(theme string) string {
	if theme == "light" {
		return "0;15"
	}
	return "15;0"
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
	// 客户端主题决定 PTY 的 COLORFGBG(浅色 0;15 / 深色 15;0)。
	// 放在 base 选项之后 → colorFgBg 覆盖服务端配置 env 中的 COLORFGBG,
	// 因为只有客户端知道页面此刻实际渲染的深浅背景。
	termOpts = append(termOpts, terminal.WithEnv([]string{
		"COLORFGBG=" + themeColorFgBg(req.Theme),
	}))

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

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
