package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// maxPageTitleLen guards the persisted page title length (browser tab
// titles are short; anything longer is truncated).
const maxPageTitleLen = 200

// handleGetTitle implements GET /api/title — returns the deployment-wide
// page title (empty when unset). The web UI applies it as the browser tab
// title on load.
func (server *Server) handleGetTitle(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"title": server.titleStore.Get()})
}

// handlePutTitle implements PUT /api/title — body {"title": "..."}.
// The value is trimmed and truncated, then persisted (survives restarts).
// An empty title clears the setting (the UI falls back to the default
// page title).
func (server *Server) handlePutTitle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	title := strings.TrimSpace(req.Title)
	if len(title) > maxPageTitleLen {
		title = title[:maxPageTitleLen]
	}

	if err := server.titleStore.Set(title); err != nil {
		log.Printf("Failed to persist page title: %s", err)
		writeError(w, http.StatusInternalServerError, "failed to persist page title")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"title": title})
}
