package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/gausszhou/gotty/internal/capture"
	"github.com/gausszhou/gotty/internal/session"
	"github.com/gausszhou/gotty/internal/terminal"
)

// Read-only monitor subscription (0005 §2.2):
// GET /ws?session_id=…&mode=mirror
//
// A monitor watches a session WITHOUT attaching to it. That distinction is
// the whole point: attaching is exclusive — a second attach preempts the
// first (1013) — so an agent that just wants to watch a terminal a human is
// driving must not use the attach path. The monitor reads the screen mirror
// instead of the PTY stream, sends nothing into the PTY, and never becomes
// an attachment (so it also cannot keep an idle session alive).
//
// Updates are dirty-row diffs pushed on a tick, never faster than
// monitorMaxFPS: only rows whose text changed since the previous frame are
// sent, and a tick whose mirror version is unchanged sends nothing at all.

const (
	// 30fps is well past what a terminal produces and past what an agent can
	// consume; the ceiling keeps a fast-scrolling session from saturating
	// the socket.
	monitorDefaultFPS = 30
	monitorMinFPS     = 1
	monitorMaxFPS     = 30
)

// errMonitorNoMirror is returned when the session has no usable mirror.
var errMonitorNoMirror = errors.New("screen mirror disabled")

// queryGetter is the subset of url.Values this file needs.
type queryGetter interface {
	Get(key string) string
}

// monitorFPS reads the requested push rate from ?fps=N, clamped to the
// supported range. An unparsable value falls back to the default instead of
// failing the connection: a monitor is an observation aid, not a contract.
func monitorFPS(q queryGetter) int {
	n := 0
	for _, c := range q.Get("fps") {
		if c < '0' || c > '9' {
			return monitorDefaultFPS
		}
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return monitorDefaultFPS
	}
	if n < monitorMinFPS {
		return monitorMinFPS
	}
	if n > monitorMaxFPS {
		return monitorMaxFPS
	}
	return n
}

// isMonitorRequest reports whether the query asks for the read-only channel.
func isMonitorRequest(q queryGetter) bool {
	return q.Get("mode") == "mirror"
}

// serveMonitor runs the monitor loop until the client disconnects, the
// session dies, or ctx is cancelled.
func (server *Server) serveMonitor(ctx context.Context, conn *websocket.Conn, sess *session.Session, fps int) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// A monitor never reads from the PTY, so without a reader goroutine a
	// half-closed client would only be noticed on the next write failure —
	// which, on an idle screen, may never come. This goroutine exists purely
	// to notice the client going away (and to answer pings).
	go monitorReadLoop(ctx, conn, cancel)

	w := &wsConn{conn: conn, ctx: ctx}
	ticker := time.NewTicker(time.Second / time.Duration(fps))
	defer ticker.Stop()

	var (
		baseline []string
		lastVer  uint64
		lastCols int
		lastRows int
		primed   bool
	)

	for {
		if !primed {
			frame, ok := buildMirrorDiff(sess, baseline, true)
			if !ok {
				return
			}
			if writeMirrorFrame(w, frame) != nil {
				return
			}
			baseline, lastVer = applyDiff(frame, baseline), frame.Version
			lastCols, lastRows = frame.Cols, frame.Rows
			primed = true
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if sess.State() == session.StateDestroyed {
			_ = conn.Close(websocket.StatusGoingAway, "session destroyed")
			return
		}

		// Skip the snapshot entirely while the screen is idle: MirrorVersion
		// is bumped on every output chunk, so an unchanged version means
		// there is provably nothing new to diff.
		if sess.MirrorVersion() == lastVer {
			continue
		}

		frame, ok := buildMirrorDiff(sess, baseline, false)
		if !ok {
			return
		}
		if frame.Cols != lastCols || frame.Rows != lastRows {
			// A resize invalidates every row index: resend the whole screen.
			if frame, ok = buildMirrorDiff(sess, nil, true); !ok {
				return
			}
		} else if len(frame.Lines) == 0 {
			continue
		}

		if writeMirrorFrame(w, frame) != nil {
			return
		}
		baseline = applyDiff(frame, baseline)
		lastVer, lastCols, lastRows = frame.Version, frame.Cols, frame.Rows
	}
}

// buildMirrorDiff snapshots the mirror and diffs it against the baseline.
// full forces every row into the frame.
func buildMirrorDiff(sess *session.Session, baseline []string, full bool) (terminal.MirrorDiffFrame, bool) {
	frame := terminal.MirrorDiffFrame{SessionID: sess.ID(), Full: full}

	snap, err := sess.Screen()
	if err != nil {
		return frame, false
	}
	raw, ok := snap.Raw.(*capture.Snapshot)
	if !ok {
		return frame, false
	}

	frame.Version = sess.MirrorVersion()
	frame.Cols, frame.Rows = raw.Cols, raw.Rows
	frame.Cursor = terminal.MirrorCursor{
		Row:     raw.CursorRow,
		Col:     raw.CursorCol,
		Visible: raw.CursorVisible,
	}

	rows := raw.RowTexts()
	for i, text := range rows {
		if !full && i < len(baseline) && baseline[i] == text {
			continue
		}
		frame.Lines = append(frame.Lines, terminal.MirrorDiffLine{Row: i, Text: text})
	}
	return frame, true
}

// applyDiff rebuilds the baseline from a frame: a full frame replaces it
// outright, a partial one patches only the rows it carries.
func applyDiff(frame terminal.MirrorDiffFrame, baseline []string) []string {
	out := make([]string, frame.Rows)
	if !frame.Full {
		copy(out, baseline)
	}
	for _, l := range frame.Lines {
		if l.Row < 0 || l.Row >= len(out) {
			continue
		}
		out[l.Row] = l.Text
	}
	return out
}

// writeMirrorFrame sends one diff frame to the monitor client.
func writeMirrorFrame(w *wsConn, frame terminal.MirrorDiffFrame) error {
	payload, err := terminal.EncodeMirrorDiff(frame)
	if err != nil {
		log.Printf("Failed to encode mirror diff for %s: %s", frame.SessionID, err)
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return nil
}

// monitorReadLoop discards client frames and answers pings so the client can
// detect a dead server; any read error (including a normal close) cancels the
// subscription. Input and resize frames are ignored on purpose: a monitor is
// read-only by contract, and ignoring them beats closing the connection on a
// client that sends a keep-alive by mistake.
func monitorReadLoop(ctx context.Context, conn *websocket.Conn, cancel context.CancelFunc) {
	defer cancel()
	for {
		typ, reader, err := conn.Reader(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return
		}
		msg, err := terminal.DecodeClientFrame(data)
		if err != nil {
			continue
		}
		if msg.Type == terminal.Ping {
			w := &wsConn{conn: conn, ctx: ctx}
			_, _ = w.Write(terminal.EncodePong())
		}
	}
}

// serveMonitorHandshake performs the common WebSocket bookkeeping for a
// monitor connection and then runs the subscription loop.
func (server *Server) serveMonitorHandshake(r *http.Request, conn *websocket.Conn, sess *session.Session) {
	server.wsWG.Add(1)
	server.activeConns.Store(conn, struct{}{})
	defer func() {
		server.activeConns.Delete(conn)
		server.wsWG.Done()
		conn.CloseNow()
	}()

	// Probe the mirror once up front: a session without one can never
	// produce a diff, and failing here is a clean 1007 rather than a
	// connection that opens and immediately dies.
	snap, err := sess.Screen()
	if err != nil {
		log.Printf("Monitor rejected for %s: %s", sess.ID(), err)
		_ = conn.Close(websocket.StatusUnsupportedData, "screen mirror disabled (start gotty with --mirror)")
		return
	}
	if _, ok := snap.Raw.(*capture.Snapshot); !ok {
		log.Printf("Monitor rejected for %s: %v", sess.ID(), errMonitorNoMirror)
		_ = conn.Close(websocket.StatusUnsupportedData, "screen mirror disabled (start gotty with --mirror)")
		return
	}

	log.Printf("Monitor attached to %s from %s", sess.ID(), r.RemoteAddr)
	server.serveMonitor(r.Context(), conn, sess, monitorFPS(r.URL.Query()))
	log.Printf("Monitor detached from %s", sess.ID())
}
