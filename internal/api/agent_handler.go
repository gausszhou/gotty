package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gausszhou/gotty/internal/capture"
	"github.com/gausszhou/gotty/internal/keys"
	"github.com/gausszhou/gotty/internal/session"
)

// Agent-driving API: screen read / wait / keys / mouse / scrollback.
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

// renderOptions builds the PNG rasterization options from the serve flags:
// --font/--font-size for glyph coverage (CJK) and the optional virtual
// mouse-cursor overlay.
func (server *Server) renderOptions(sess *session.Session, withMouseCursor bool) []capture.RenderOption {
	var opts []capture.RenderOption
	if server.options.Font != "" || server.options.FontSize > 0 {
		opts = append(opts, capture.WithFont(server.options.Font, float64(server.options.FontSize)))
	}
	if withMouseCursor {
		if st := sess.MouseState(); st.LastEvent != "" {
			opts = append(opts, capture.WithMouseCursor(&capture.MouseCursor{
				Col:  st.CursorCol,
				Row:  st.CursorRow,
				Held: len(st.Held) > 0,
			}))
		}
	}
	return opts
}

// handleGetScreen implements
// GET /api/sessions/{id}/screen?format=text|json|png[&part=scrollback&lines=N].
// Returns the rendered terminal screen at this moment: plain text (default),
// styled JSON cells (RenderDocument-shaped) or a PNG bitmap. With
// part=scrollback it returns the session history instead of the visible
// screen (0005 §2.1).
func (server *Server) handleGetScreen(w http.ResponseWriter, r *http.Request) {
	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	if r.URL.Query().Get("part") == "scrollback" {
		server.handleScrollback(w, r, sess)
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
		opts := server.renderOptions(sess, r.URL.Query().Get("mouse_cursor") != "")
		data, err := raw.PNG(opts...)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to render PNG: "+err.Error())
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

// scrollbackResponse is the wire form of a history read.
type scrollbackResponse struct {
	Mirror          bool               `json:"mirror"`
	SessionID       string             `json:"session_id"`
	Part            string             `json:"part"`
	Lines           int                `json:"lines"`
	Total           int                `json:"total"`
	AlternateScreen bool               `json:"alternate_screen"`
	Cols            int                `json:"cols"`
	Rows            int                `json:"rows"`
	Text            string             `json:"text,omitempty"`
	Cells           []capture.CellJSON `json:"cells,omitempty"`
}

// handleScrollback serves GET /screen?part=scrollback&lines=N.
// lines omits/<=0 = everything, N>0 = the most recent N lines (tail-first:
// an agent almost always wants recent context).
func (server *Server) handleScrollback(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	if r.URL.Query().Get("format") == "png" {
		writeError(w, http.StatusBadRequest, "png is not supported for scrollback; use text or json")
		return
	}
	lines := 0
	if raw := r.URL.Query().Get("lines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid lines parameter")
			return
		}
		lines = n
	}

	view, err := sess.Scrollback(lines)
	if err != nil {
		if errors.Is(err, session.ErrMirrorDisabled) {
			writeError(w, http.StatusServiceUnavailable, "screen mirror disabled (start gotty with --mirror)")
			return
		}
		log.Printf("Failed to read scrollback of %s: %s", sess.ID(), err)
		writeError(w, http.StatusInternalServerError, "failed to read scrollback")
		return
	}
	if view.Disabled {
		writeError(w, http.StatusServiceUnavailable, "scrollback disabled (start gotty with --scrollback=N)")
		return
	}

	resp := scrollbackResponse{
		Mirror:          true,
		SessionID:       sess.ID(),
		Part:            "scrollback",
		Total:           view.Total,
		AlternateScreen: view.Alternate,
		Text:            view.Text,
	}
	// The alternate screen legitimately has no history to show: an empty
	// result is not an error (a full-screen program owns the screen).
	if grid, ok := view.Raw.(*capture.Grid); ok && !view.Alternate {
		resp.Cols = grid.Cols()
		resp.Rows = grid.Rows()
		resp.Lines = grid.Rows()
		if r.URL.Query().Get("format") == "json" {
			resp.Cells = capture.CellsJSON(grid)
		}
	}

	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(resp.Text))
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

// keysRequest is the POST /api/sessions/{id}/keys body. Exactly one of
// input / keys / paste must be set:
//
//	input  raw bytes (encoding: text|base64) — the original agent contract,
//	       unchanged so existing agents keep working;
//	keys   named keys ("Escape", ":", "w", "q", "Enter", "Ctrl+C", "F5");
//	paste  a whole text block as ONE paste, wrapped in the bracketed-paste
//	       markers only when the application enabled mode 2004 (§2.5).
type keysRequest struct {
	Input      string   `json:"input"`
	Encoding   string   `json:"encoding"`
	Keys       []string `json:"keys"`
	Paste      *string  `json:"paste"`
	ForcePaste bool     `json:"force_paste"`
}

// handleKeys implements POST /api/sessions/{id}/keys — agent input injection
// without an attached client (tu type/press/paste 语义).
// Honor --permit-write: read-only deployments reject input with 403.
func (server *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	if !server.options.PermitWrite {
		writeError(w, http.StatusForbidden, "write is disabled (--permit-write=false)")
		return
	}
	var req keysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	provided := 0
	if req.Input != "" {
		provided++
	}
	if len(req.Keys) > 0 {
		provided++
	}
	if req.Paste != nil {
		provided++
	}
	if provided > 1 {
		writeError(w, http.StatusBadRequest, "input, keys and paste are mutually exclusive")
		return
	}

	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}

	// paste: one atomic paste, conditionally bracketed.
	if req.Paste != nil {
		written, err := sess.Paste(*req.Paste, req.ForcePaste)
		if err != nil {
			writeInputError(w, sess, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"written":   written,
			"mode":      "paste",
			"bracketed": sess.BracketedPaste(),
		})
		return
	}

	// keys: named keys translated by internal/keys.
	if len(req.Keys) > 0 {
		payload, err := keys.EncodeSequence(req.Keys)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := sess.Input(payload); err != nil {
			writeInputError(w, sess, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"written": len(payload),
			"mode":    "keys",
		})
		return
	}

	// input: raw bytes (legacy path, semantics unchanged).
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

	if err := sess.Input(payload); err != nil {
		writeInputError(w, sess, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"written": len(payload),
		"mode":    "input",
	})
}

// writeInputError maps a session write failure onto the REST error space.
func writeInputError(w http.ResponseWriter, sess *session.Session, err error) {
	if errors.Is(err, session.ErrSessionDestroyed) {
		writeError(w, http.StatusConflict, "session is destroyed")
		return
	}
	log.Printf("Failed to write input to %s: %s", sess.ID(), err)
	writeError(w, http.StatusInternalServerError, "failed to write input")
}

// mouseRequest is the POST /api/sessions/{id}/mouse body (0004 §2.1–2.3).
// Position comes either from explicit coordinates or from a semantic target
// (on_text / on_regex), which is resolved to the matching cell's center.
type mouseRequest struct {
	Action string `json:"action"` // click(default) | down | up | move | drag | scroll
	Button string `json:"button"` // left(default for click) | middle | right | wheelup | ...
	Col    *int   `json:"col"`
	Row    *int   `json:"row"`
	// ToCol/ToRow is the drag destination (action == "drag").
	ToCol *int `json:"to_col"`
	ToRow *int `json:"to_row"`
	// Semantic targeting: click the label instead of guessing coordinates.
	OnText     string `json:"on_text"`
	OnRegex    string `json:"on_regex"`
	MatchIndex int    `json:"match_index"`
	Mods       struct {
		Shift bool `json:"shift"`
		Alt   bool `json:"alt"`
		Ctrl  bool `json:"ctrl"`
	} `json:"mods"`
	Clicks int  `json:"clicks"`
	Amount int  `json:"amount"`
	Force  bool `json:"force"`
}

// mouseResponse is the wire form of a mouse action.
type mouseResponse struct {
	Written      int                `json:"written"`
	Col          int                `json:"col"`
	Row          int                `json:"row"`
	Action       string             `json:"action"`
	Button       string             `json:"button"`
	Mode         string             `json:"mode"`
	ResolvedFrom string             `json:"resolved_from"`
	Mouse        session.MouseState `json:"mouse"`
}

// handleMouse implements POST /api/sessions/{id}/mouse — click/drag/scroll in
// a running session, including "click the thing that says OK" via
// on_text/on_regex. Applications that never enabled mouse reporting get 409
// unless "force": true is set.
func (server *Server) handleMouse(w http.ResponseWriter, r *http.Request) {
	if !server.options.PermitWrite {
		writeError(w, http.StatusForbidden, "write is disabled (--permit-write=false)")
		return
	}
	var req mouseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Action == "" {
		req.Action = string(capture.MouseClickAction)
	}

	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if sess.State() == session.StateDestroyed {
		writeError(w, http.StatusConflict, "session is destroyed")
		return
	}

	state := sess.MouseState()
	if !state.Supported {
		writeError(w, http.StatusServiceUnavailable, "screen mirror disabled (start gotty with --mirror)")
		return
	}

	col, row, resolvedFrom, ok := server.resolveMouseTarget(w, sess, &req)
	if !ok {
		return // response already written
	}

	ev := session.MouseEvent{
		Action: req.Action,
		Button: req.Button,
		Col:    col,
		Row:    row,
		Shift:  req.Mods.Shift,
		Alt:    req.Mods.Alt,
		Ctrl:   req.Mods.Ctrl,
		Clicks: req.Clicks,
		Amount: req.Amount,
		Force:  req.Force,
	}
	if req.ToCol != nil && req.ToRow != nil {
		ev.ToCol, ev.ToRow = *req.ToCol, *req.ToRow
	}

	written, err := sess.Mouse(ev)
	if err != nil {
		switch {
		case errors.Is(err, capture.ErrMouseDisabled):
			writeMouseDisabled(w, sess, state)
		case errors.Is(err, capture.ErrMouseOutOfBounds):
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":      err.Error(),
				"cols":       snapshotCols(sess),
				"rows":       snapshotRows(sess),
				"mouse":      state,
				"session_id": sess.ID(),
			})
		case errors.Is(err, capture.ErrMouseButton), errors.Is(err, capture.ErrMouseAction):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, session.ErrSessionDestroyed):
			writeError(w, http.StatusConflict, "session is destroyed")
		case errors.Is(err, session.ErrMirrorDisabled):
			writeError(w, http.StatusServiceUnavailable, "screen mirror disabled (start gotty with --mirror)")
		default:
			log.Printf("Failed to send mouse event to %s: %s", sess.ID(), err)
			writeError(w, http.StatusInternalServerError, "failed to send mouse event")
		}
		return
	}

	after := sess.MouseState()
	writeJSON(w, http.StatusOK, mouseResponse{
		Written:      written,
		Col:          col,
		Row:          row,
		Action:       req.Action,
		Button:       req.Button,
		Mode:         after.Mode,
		ResolvedFrom: resolvedFrom,
		Mouse:        after,
	})
}

// resolveMouseTarget computes the cell to act on. Explicit coordinates win;
// otherwise on_text/on_regex are located on the visible grid. When neither is
// given the session's virtual mouse cursor is reused (so `up`/`scroll` do not
// need coordinates). A missing semantic target answers 404.
func (server *Server) resolveMouseTarget(w http.ResponseWriter, sess *session.Session, req *mouseRequest) (col, row int, resolvedFrom string, ok bool) {
	if req.OnText != "" || req.OnRegex != "" {
		snap, err := sess.Screen()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "screen mirror disabled (start gotty with --mirror)")
			return 0, 0, "", false
		}
		raw, isSnap := snap.Raw.(*capture.Snapshot)
		if !isSnap {
			writeError(w, http.StatusInternalServerError, "failed to read screen")
			return 0, 0, "", false
		}
		grid := raw.Grid()

		var (
			c  int
			rw int
			ok bool
		)
		switch {
		case req.OnText != "":
			rw, c, ok = grid.LocateText(req.OnText, req.MatchIndex)
		default:
			re, err := regexp.Compile(req.OnRegex)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid regex: "+err.Error())
				return 0, 0, "", false
			}
			rw, c, ok = grid.LocateRegex(re, req.MatchIndex)
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]interface{}{
				"error":        "target not found on screen",
				"on_text":      req.OnText,
				"on_regex":     req.OnRegex,
				"match_index":  req.MatchIndex,
				"session_id":   sess.ID(),
				"screen_lines": len(raw.RowTexts()),
			})
			return 0, 0, "", false
		}
		name := "on_text"
		if req.OnText == "" {
			name = "on_regex"
		}
		return c, rw, name, true
	}

	if req.Col != nil && req.Row != nil {
		return *req.Col, *req.Row, "coordinates", true
	}

	state := sess.MouseState()
	return state.CursorCol, state.CursorRow, "cursor", true
}

// writeMouseDisabled answers 409 with the state that explains why, so an
// agent can tell "the app never asked for mouse events" from "my command was
// wrong" and retry with force if it really wants the bytes.
func writeMouseDisabled(w http.ResponseWriter, sess *session.Session, state session.MouseState) {
	writeJSON(w, http.StatusConflict, map[string]interface{}{
		"error":      "application did not enable mouse reporting; retry with \"force\": true to send SGR bytes anyway",
		"session_id": sess.ID(),
		"mouse":      state,
	})
}

// handleMouseState implements GET /api/sessions/{id}/mouse — the tracked
// mouse/paste mode state plus the synthetic cursor and held buttons.
func (server *Server) handleMouseState(w http.ResponseWriter, r *http.Request) {
	sess, err := server.manager.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	state := sess.MouseState()
	if !state.Supported {
		writeError(w, http.StatusServiceUnavailable, "screen mirror disabled (start gotty with --mirror)")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Mirror    bool   `json:"mirror"`
		SessionID string `json:"session_id"`
		session.MouseState
	}{Mirror: true, SessionID: sess.ID(), MouseState: state})
}

// snapshotCols/rows report the visible screen size for error messages.
func snapshotCols(sess *session.Session) int {
	cols, _ := sess.ScreenSize()
	return cols
}

func snapshotRows(sess *session.Session) int {
	_, rows := sess.ScreenSize()
	return rows
}
