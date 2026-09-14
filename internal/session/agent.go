package session

import "fmt"

// Agent-driving capabilities beyond screen/wait/keys (0004 + 0005).
//
// session must not import capture (capture's browser engine depends on
// session), so the mirror-side capabilities are declared here as neutral
// types and discovered on the ScreenMirror through optional interfaces —
// exactly like the Size()/frameReader probes elsewhere in this package.
// A mirror that does not implement them simply has no mouse / no scrollback.

// MouseEvent is one mouse request, independent of the wire encoding.
type MouseEvent struct {
	// Action is click | down | up | move | drag | scroll.
	Action string
	// Button is left | middle | right | wheelup | wheeldown | wheelleft |
	// wheelright | backward | forward | button10 | button11 ("" = none).
	Button string
	// Col/Row are 0-based cells, origin top-left.
	Col, Row int
	// ToCol/ToRow is the drag destination (Action == "drag").
	ToCol, ToRow int
	// Modifiers held during the event.
	Shift, Alt, Ctrl bool
	// Clicks repeats a click (2 = double click).
	Clicks int
	// Amount repeats a wheel step.
	Amount int
	// Force sends SGR bytes even when the application enabled no mouse mode.
	Force bool
}

// MouseState is the session-visible mouse/paste mode state (`mouse state`).
type MouseState struct {
	// Supported is false when the mirror cannot report mouse state at all
	// (no mirror, or a mirror without the capability).
	Supported bool `json:"supported"`
	// Enabled is true when the application enabled a mouse reporting mode.
	Enabled bool `json:"enabled"`
	// Mode is the negotiated encoding: "sgr" (mode 1006) or "x10".
	Mode  string `json:"mode,omitempty"`
	Modes []int  `json:"modes,omitempty"`
	// BracketedPaste reflects mode 2004, which gates conditional paste wrapping.
	BracketedPaste bool     `json:"bracketed_paste"`
	CursorCol      int      `json:"cursor_col"`
	CursorRow      int      `json:"cursor_row"`
	Held           []string `json:"held,omitempty"`
	LastEvent      string   `json:"last_event,omitempty"`
}

// ScrollbackSnapshot is the session-visible view of the history buffer.
type ScrollbackSnapshot struct {
	// Disabled is true when the session was configured with no history
	// budget (`--scrollback=0`); the API answers 503.
	Disabled bool
	// Alternate is true when a full-screen program owns the screen.
	Alternate bool
	// Total is the number of retained history lines before `lines` trimming.
	Total int
	// Text is the plain-text rendering of the returned lines.
	Text string
	// Raw is the implementation's render-ready grid (the api layer asserts it
	// back to *capture.Grid for the JSON rendering).
	Raw interface{}
}

// MouseMirror is implemented by mirrors that can drive mouse events and
// report the application's mouse / bracketed-paste mode state.
type MouseMirror interface {
	MouseEnabled() bool
	BracketedPaste() bool
	EncodeMouse(ev MouseEvent) ([]byte, error)
	MouseState() MouseState
}

// ScrollbackMirror is implemented by mirrors that expose screen history.
type ScrollbackMirror interface {
	// ScrollbackSnapshot returns up to limit most recent lines (limit <= 0
	// returns everything).
	ScrollbackSnapshot(limit int) ScrollbackSnapshot
}

// ScrollbackTuner is implemented by mirrors whose history capacity can be
// retuned after construction (the per-session `scrollback` parameter).
type ScrollbackTuner interface {
	SetScrollbackLines(maxLines int)
}

// Mouse sends one mouse event into the PTY (the agent mouse API).
// It returns the number of bytes written. ErrMirrorDisabled when there is no
// mirror, and the mirror's own errors (disabled / out of bounds / unknown
// button) otherwise.
func (s *Session) Mouse(ev MouseEvent) (int, error) {
	s.mirrorMu.Lock()
	mm, ok := s.mirror.(MouseMirror)
	if !ok {
		s.mirrorMu.Unlock()
		return 0, ErrMirrorDisabled
	}
	payload, err := mm.EncodeMouse(ev)
	s.mirrorMu.Unlock()
	if err != nil {
		return 0, err
	}
	if len(payload) == 0 {
		return 0, nil
	}
	if s.State() == StateDestroyed {
		return 0, ErrSessionDestroyed
	}
	if _, err := s.term.Write(payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

// MouseState returns the mirror's mouse/paste state. A session without the
// capability reports Supported=false instead of failing.
func (s *Session) MouseState() MouseState {
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	if mm, ok := s.mirror.(MouseMirror); ok {
		return mm.MouseState()
	}
	return MouseState{}
}

// BracketedPaste reports whether the application enabled mode 2004. It is
// false when there is no mirror: the safest reading, since wrapping a program
// that never asked for it would print the markers as garbage.
func (s *Session) BracketedPaste() bool {
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	if mm, ok := s.mirror.(MouseMirror); ok {
		return mm.BracketedPaste()
	}
	return false
}

// Paste writes a whole text block as ONE paste, wrapped in the bracketed-paste
// markers only when the application enabled mode 2004 (or force is set).
// Without the wrapping a multi-line paste is delivered line by line and a
// shell executes every line — the reason the conditional form matters.
func (s *Session) Paste(text string, force bool) (int, error) {
	payload := wrapPaste(text, s.BracketedPaste(), force)
	if len(payload) == 0 {
		return 0, nil
	}
	if s.State() == StateDestroyed {
		return 0, ErrSessionDestroyed
	}
	if _, err := s.term.Write(payload); err != nil {
		return 0, err
	}
	return len(payload), nil
}

// wrapPaste applies the bracketed-paste markers conditionally.
func wrapPaste(text string, bracketed, force bool) []byte {
	if !bracketed && !force {
		return []byte(text)
	}
	return []byte("\x1b[200~" + text + "\x1b[201~")
}

// Scrollback returns the mirror's history snapshot (0005 §2.1).
func (s *Session) Scrollback(limit int) (ScrollbackSnapshot, error) {
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	if s.mirror == nil {
		return ScrollbackSnapshot{}, ErrMirrorDisabled
	}
	sm, ok := s.mirror.(ScrollbackMirror)
	if !ok {
		return ScrollbackSnapshot{}, ErrMirrorDisabled
	}
	return sm.ScrollbackSnapshot(limit), nil
}

// SetScrollbackLines retunes the mirror's history capacity.
func (s *Session) SetScrollbackLines(maxLines int) {
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	if t, ok := s.mirror.(ScrollbackTuner); ok {
		t.SetScrollbackLines(maxLines)
	}
}

// MirrorVersion returns the counter bumped on every output chunk fed into the
// mirror. A monitor polls it to skip snapshots while the screen is idle.
func (s *Session) MirrorVersion() uint64 {
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	return s.mirrorVer
}

// ScreenSize returns the mirror grid size (0,0 when there is no mirror).
func (s *Session) ScreenSize() (int, int) {
	s.mirrorMu.Lock()
	defer s.mirrorMu.Unlock()
	if sz, ok := s.mirror.(interface{ Size() (int, int) }); ok {
		return sz.Size()
	}
	return 0, 0
}

// DescribeMouseEvent renders a mouse event for error messages / logs.
func (ev MouseEvent) Describe() string {
	out := ev.Action
	if ev.Button != "" {
		out += " " + ev.Button
	}
	return fmt.Sprintf("%s@%d,%d", out, ev.Col, ev.Row)
}
