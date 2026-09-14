package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
	"github.com/gausszhou/gotty/internal/utils"
)

// Rest API — session management.
// 列表由客户端清单(localStorage)驱动;服务端只按 id 提供:
// 创建(幂等/复活)、详情、状态批量查询、销毁、重命名、resize/signal。

// Limits on the per-session launch parameters (0005 §2.4). They are not a
// security boundary — being able to create a session already means running a
// process — but they keep a single request from becoming a memory amplifier.
const (
	maxSessionEnvVars = 256
	maxSessionEnvSize = 64 << 10
	maxEnvNameLen     = 128
	maxEnvValueLen    = 16 << 10
	maxTermLen        = 64
)

type createSessionRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`

	// Per-session launch parameters (0005 §2.4): env overrides, working
	// directory, TERM and the history budget for this session's mirror.
	Env        []string `json:"env"`
	Cwd        string   `json:"cwd"`
	Term       string   `json:"term"`
	Scrollback *int     `json:"scrollback"`
}

// validateSessionParams rejects malformed per-session launch parameters.
func validateSessionParams(req *createSessionRequest) error {
	if req.Cwd != "" {
		info, err := os.Stat(req.Cwd)
		if err != nil {
			return fmt.Errorf("cwd does not exist: %s", req.Cwd)
		}
		if !info.IsDir() {
			return fmt.Errorf("cwd is not a directory: %s", req.Cwd)
		}
	}
	if len(req.Env) > maxSessionEnvVars {
		return fmt.Errorf("too many env entries (max %d)", maxSessionEnvVars)
	}
	total := 0
	for _, kv := range req.Env {
		if err := validateEnvEntry(kv); err != nil {
			return err
		}
		total += len(kv)
	}
	if total > maxSessionEnvSize {
		return fmt.Errorf("env too large (max %d bytes)", maxSessionEnvSize)
	}
	if req.Term != "" {
		if len(req.Term) > maxTermLen {
			return fmt.Errorf("term too long (max %d bytes)", maxTermLen)
		}
		for _, r := range req.Term {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("term contains control characters")
			}
		}
	}
	if req.Scrollback != nil && *req.Scrollback < 0 {
		return fmt.Errorf("scrollback must be >= 0")
	}
	return nil
}

// validateEnvEntry requires a K=V pair with a POSIX-ish variable name.
func validateEnvEntry(kv string) error {
	i := strings.Index(kv, "=")
	if i <= 0 {
		return fmt.Errorf("invalid env entry %q: expected K=V", kv)
	}
	name := kv[:i]
	if len(name) > maxEnvNameLen {
		return fmt.Errorf("invalid env entry: variable name too long (max %d)", maxEnvNameLen)
	}
	for j, r := range name {
		ok := r == '_' ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z') ||
			(j > 0 && r >= '0' && r <= '9')
		if !ok {
			return fmt.Errorf("invalid env entry: bad variable name %q", name)
		}
	}
	if len(kv)-i-1 > maxEnvValueLen {
		return fmt.Errorf("invalid env entry %q: value too long (max %d)", name, maxEnvValueLen)
	}
	return nil
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
	if err := validateSessionParams(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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
	if len(req.Env) > 0 {
		termOpts = append(termOpts, terminal.WithEnv(req.Env))
	}
	if req.Cwd != "" {
		termOpts = append(termOpts, terminal.WithWorkDir(req.Cwd))
	}
	if req.Term != "" {
		termOpts = append(termOpts, terminal.WithTerm(req.Term))
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

	// Session-level history budget (0005 §2.4) overrides the serve default.
	if req.Scrollback != nil {
		sess.SetScrollbackLines(*req.Scrollback)
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
