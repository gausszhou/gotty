package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gausszhou/gotty/internal/session"
)

// Attached capture engine (M4): ask the browser that is already attached to
// composite its terminal view and return the PNG. See
// docs/design/capture-design.md §8.

// captureResponse is the JSON form of a capture. PNG is base64 so the body
// stays text/JSON like every other endpoint; `?format=png` returns raw bytes
// for callers that just want to save the file.
type captureResponse struct {
	SessionID string `json:"session_id"`
	TakenAt   string `json:"taken_at"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	PNG       string `json:"png"`
}

// captureRequest is the optional POST body.
type captureRequest struct {
	IncludeSelection bool `json:"include_selection"`
	TimeoutMS        int  `json:"timeout_ms"`
}

// maxCaptureTimeout bounds a client-supplied timeout: a stuck browser must not
// pin an HTTP request (or a server goroutine) indefinitely.
const maxCaptureTimeout = 60 * time.Second

// handleCaptureSession implements POST /api/sessions/{id}/capture: trigger a
// capture on the attached client and block until it answers.
func (server *Server) handleCaptureSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := server.manager.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	var req captureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = session.DefaultCaptureTimeout
	}
	if timeout > maxCaptureTimeout {
		timeout = maxCaptureTimeout
	}

	res, err := sess.Capture(r.Context(), session.CaptureOptions{
		IncludeSelection: req.IncludeSelection,
		Timeout:          timeout,
	})
	if err != nil {
		switch {
		case r.Context().Err() != nil:
			// Client hung up; nothing sensible to write.
		case errors.Is(err, session.ErrNoClientAttached):
			// Nobody is attached: the composite happens in the browser, so
			// there is nothing to ask. Use --engine browser or native instead.
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, session.ErrCaptureBusy):
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, session.ErrCaptureTimeout):
			writeError(w, http.StatusGatewayTimeout, err.Error())
		default:
			writeError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	writeCapture(w, r, id, res)
}

// handleLatestCapture implements GET /api/sessions/{id}/captures/latest: the
// last successful capture without triggering a new one (a poller can retry
// cheaply while the browser is busy).
func (server *Server) handleLatestCapture(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := server.manager.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	res := sess.LatestCapture()
	if res == nil {
		writeError(w, http.StatusNotFound, "no capture yet for this session")
		return
	}
	writeCapture(w, r, id, res)
}

// writeCapture renders a capture as raw PNG (?format=png) or JSON.
func writeCapture(w http.ResponseWriter, r *http.Request, id string, res *session.CaptureResult) {
	if r.URL.Query().Get("format") == "png" {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(res.PNG)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(res.PNG)
		return
	}
	writeJSON(w, http.StatusOK, captureResponse{
		SessionID: id,
		TakenAt:   res.At.Format(time.RFC3339),
		Cols:      res.Cols,
		Rows:      res.Rows,
		PNG:       base64.StdEncoding.EncodeToString(res.PNG),
	})
}
