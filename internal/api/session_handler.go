package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
)

// Rest API — session management.

type createSessionRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Width   int      `json:"width"`
	Height  int      `json:"height"`
}

type listSessionsResponse struct {
	Sessions []session.StateDescription `json:"sessions"`
}

// handleCreateSession implements POST /api/sessions.
func (server *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	command := req.Command
	args := req.Args
	if command == "" {
		// Fall back to the command given on the CLI, together with its args.
		command = server.options.DefaultCommand
		args = server.options.DefaultArgs
	}
	if command == "" {
		writeError(w, http.StatusBadRequest, "no command given")
		return
	}

	var termOpts []terminal.Option
	if req.Width > 0 && req.Height > 0 {
		termOpts = append(termOpts, terminal.WithInitialSize(req.Width, req.Height))
	}

	sess, err := server.manager.Create(command, args, termOpts...)
	if err != nil {
		switch err {
		case session.ErrTooManySessions:
			writeError(w, http.StatusServiceUnavailable, "too many sessions")
		default:
			log.Printf("Failed to create session: %s", err)
			writeError(w, http.StatusInternalServerError, "failed to create session")
		}
		return
	}

	log.Printf("Session created: %s (%s %v)", sess.ID(), sess.Command(), sess.Args())
	writeJSON(w, http.StatusCreated, sess.StateDescription())
}

// handleListSessions implements GET /api/sessions.
func (server *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions := server.manager.List()
	descriptions := make([]session.StateDescription, 0, len(sessions))
	for _, sess := range sessions {
		if sess.State() == session.StateDestroyed {
			continue
		}
		descriptions = append(descriptions, sess.StateDescription())
	}

	writeJSON(w, http.StatusOK, listSessionsResponse{Sessions: descriptions})
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
