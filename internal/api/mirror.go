package api

import (
	"github.com/gausszhou/gotty/internal/capture"
	"github.com/gausszhou/gotty/internal/session"
)

// captureMirror adapts a capture.Emulator to the session.ScreenMirror
// interface. session must not import capture — the capture package's
// browser engine depends on session, and a session → capture edge would
// create an import cycle — so the adapter lives here in the api layer,
// which imports both.
//
// Besides the base mirror contract it also implements the optional capability
// interfaces session probes for (MouseMirror / ScrollbackMirror /
// ScrollbackTuner / Size), which is what makes mouse driving, mouse state and
// scrollback reads available without session ever naming a capture type.
type captureMirror struct {
	emu *capture.Emulator
}

// MirrorFactory returns the session.MirrorFactory that wires the capture
// emulator in as the per-session screen mirror. A disabled mirror
// (--mirror=false) yields a nil factory, so sessions never allocate the
// grid and the screen/wait endpoints answer 503.
//
// scrollback is the default history capacity for each mirror (0 disables it,
// negative keeps x/vt's own default); a session-level `scrollback` creation
// parameter retunes it afterwards via session.ScrollbackTuner.
func MirrorFactory(enabled bool, scrollback int) session.MirrorFactory {
	if !enabled {
		return nil
	}
	return func(term session.Terminal) session.ScreenMirror {
		cols, rows := 80, 24 // mirror 默认尺寸;首个 resize 会同步
		if sz, ok := term.(interface{ Size() (int, int) }); ok {
			if c, r := sz.Size(); c > 0 && r > 0 {
				cols, rows = c, r
			}
		}
		return &captureMirror{emu: capture.NewEmulatorWithScrollback(cols, rows, scrollback)}
	}
}

func (m *captureMirror) Write(p []byte) (int, error) { return m.emu.Write(p) }

func (m *captureMirror) Resize(cols, rows int) { m.emu.Resize(cols, rows) }

func (m *captureMirror) DrainAnswers() []byte { return m.emu.DrainAnswers() }

func (m *captureMirror) Snapshot() session.ScreenSnapshot {
	snap := m.emu.Snapshot()
	return session.ScreenSnapshot{Text: snap.Text(), Raw: snap}
}

// Size reports the mirror grid size (session falls back to it when the PTY
// size was never set explicitly).
func (m *captureMirror) Size() (int, int) { return m.emu.Cols(), m.emu.Rows() }

// ---- MouseMirror ----------------------------------------------------------

// MouseEnabled reports whether the application enabled mouse reporting.
func (m *captureMirror) MouseEnabled() bool { return m.emu.MouseEnabled() }

// BracketedPaste reports whether the application enabled mode 2004.
func (m *captureMirror) BracketedPaste() bool { return m.emu.BracketedPaste() }

// EncodeMouse converts the session-level event into the capture mouse model
// and encodes it with the negotiated protocol.
func (m *captureMirror) EncodeMouse(ev session.MouseEvent) ([]byte, error) {
	return m.emu.EncodeMouse(capture.MouseEvent{
		Action: capture.MouseAction(ev.Action),
		Button: ev.Button,
		Col:    ev.Col,
		Row:    ev.Row,
		ToCol:  ev.ToCol,
		ToRow:  ev.ToRow,
		Mods:   capture.MouseMods{Shift: ev.Shift, Alt: ev.Alt, Ctrl: ev.Ctrl},
		Clicks: ev.Clicks,
		Amount: ev.Amount,
		Force:  ev.Force,
	})
}

// MouseState reports the mirror's tracked mouse/paste state.
func (m *captureMirror) MouseState() session.MouseState {
	st := m.emu.MouseState()
	return session.MouseState{
		Supported:      true,
		Enabled:        st.Enabled,
		Mode:           st.Mode,
		Modes:          st.Modes,
		BracketedPaste: st.BracketedPaste,
		CursorCol:      st.CursorCol,
		CursorRow:      st.CursorRow,
		Held:           st.Held,
		LastEvent:      st.LastEvent,
	}
}

// MouseCursor returns the synthetic cursor for the PNG overlay (nil when the
// agent never moved the virtual mouse).
func (m *captureMirror) MouseCursor() *capture.MouseCursor {
	st := m.emu.MouseState()
	if st.LastEvent == "" {
		return nil
	}
	return &capture.MouseCursor{Col: st.CursorCol, Row: st.CursorRow, Held: len(st.Held) > 0}
}

// ---- ScrollbackMirror / ScrollbackTuner -----------------------------------

// ScrollbackSnapshot returns the retained history (limit <= 0 = everything).
func (m *captureMirror) ScrollbackSnapshot(limit int) session.ScrollbackSnapshot {
	v := m.emu.Scrollback(limit)
	out := session.ScrollbackSnapshot{
		Disabled:  v.Disabled,
		Alternate: v.Alternate,
		Total:     v.Total,
	}
	if v.Grid != nil {
		out.Text = capture.Text(v.Grid)
		out.Raw = v.Grid
	}
	return out
}

// SetScrollbackLines retunes the history capacity (per-session `scrollback`).
func (m *captureMirror) SetScrollbackLines(maxLines int) {
	m.emu.SetScrollbackLines(maxLines)
}
