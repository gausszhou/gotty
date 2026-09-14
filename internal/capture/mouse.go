package capture

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Private DECSET/DECRST modes the mirror tracks for itself: x/vt applies
// every mode it knows, but does not expose the state, and the agent API
// needs it to pick the wire encoding and to refuse clicks an application
// never asked for (0004 §2.1).
const (
	modeCursorVisible = 25

	modeMouseX10         = 9
	modeMouseNormal      = 1000
	modeMouseHighlight   = 1001
	modeMouseButtonEvent = 1002
	modeMouseAnyEvent    = 1003
	modeMouseUTF8        = 1005
	modeMouseSGR         = 1006
	modeMouseUrxvt       = 1015

	modeBracketedPaste = 2004
)

// mouseReportingModes are the modes that mean "this application wants mouse
// events" (any of them enabled ⇒ the session is mouse-enabled).
var mouseReportingModes = []int{
	modeMouseX10, modeMouseNormal, modeMouseHighlight,
	modeMouseButtonEvent, modeMouseAnyEvent,
}

// Errors returned by the capture mouse API.
var (
	// ErrMouseDisabled means the application never enabled a mouse reporting
	// mode; the API maps it to 409 unless the caller forces the event.
	ErrMouseDisabled = errors.New("application did not enable mouse reporting")
	// ErrMouseOutOfBounds means the requested cell is outside the screen.
	ErrMouseOutOfBounds = errors.New("mouse coordinates out of bounds")
	// ErrMouseButton means the button name is unknown.
	ErrMouseButton = errors.New("unknown mouse button")
	// ErrMouseAction means the action name is unknown.
	ErrMouseAction = errors.New("unknown mouse action")
)

// MouseAction is the requested mouse action (tu mouse 语义).
type MouseAction string

// Supported mouse actions.
const (
	MouseClickAction  MouseAction = "click"
	MouseDownAction   MouseAction = "down"
	MouseUpAction     MouseAction = "up"
	MouseMoveAction   MouseAction = "move"
	MouseDragAction   MouseAction = "drag"
	MouseScrollAction MouseAction = "scroll"
)

// MouseMods are the modifier keys held during a mouse event.
type MouseMods struct {
	Shift bool `json:"shift,omitempty"`
	Alt   bool `json:"alt,omitempty"`
	Ctrl  bool `json:"ctrl,omitempty"`
}

// MouseEvent is one normalized mouse request. Coordinates are 0-based cells
// with the origin at the top-left of the screen.
type MouseEvent struct {
	Action MouseAction
	Button string // left/middle/right/wheelup/... (empty = none held)
	Col    int
	Row    int
	// ToCol/ToRow are the drag destination (Action == MouseDrag only).
	ToCol, ToRow int
	Mods         MouseMods
	// Clicks repeats a click N times (2 = double click).
	Clicks int
	// Amount repeats a wheel step N times.
	Amount int
	// Force sends SGR bytes even when the application never enabled mouse
	// reporting (tu mouse --force).
	Force bool
}

// MouseEncoding is the negotiated mouse protocol of a session.
type MouseEncoding struct {
	// Enabled is true when the application enabled mouse reporting.
	Enabled bool
	// Mode is the wire encoding: "sgr" (mode 1006) or "x10" (default).
	Mode string
	// Modes lists every tracked mouse-tracking mode number.
	Modes []int
}

// MouseStateJSON is the wire form of GET /api/sessions/{id}/mouse.
type MouseStateJSON struct {
	Enabled        bool     `json:"enabled"`
	Mode           string   `json:"mode,omitempty"`
	Modes          []int    `json:"modes,omitempty"`
	BracketedPaste bool     `json:"bracketed_paste"`
	CursorCol      int      `json:"cursor_col"`
	CursorRow      int      `json:"cursor_row"`
	Held           []string `json:"held,omitempty"`
	LastEvent      string   `json:"last_event,omitempty"`
}

// MouseEnabled reports whether the application enabled any mouse reporting mode.
func (e *Emulator) MouseEnabled() bool {
	for _, m := range mouseReportingModes {
		if e.modes[m] {
			return true
		}
	}
	return false
}

// BracketedPaste reports whether the application enabled bracketed paste (2004).
// Conditional paste wrapping keys off this: wrapping unconditionally would
// spray ESC sequences into programs that never asked for them.
func (e *Emulator) BracketedPaste() bool { return e.modes[modeBracketedPaste] }

// MouseEncoding returns the negotiated protocol (X10 unless 1006 is on).
func (e *Emulator) MouseEncoding() MouseEncoding {
	enc := MouseEncoding{Enabled: e.MouseEnabled()}
	for _, m := range mouseReportingModes {
		if e.modes[m] {
			enc.Modes = append(enc.Modes, m)
		}
	}
	sort.Ints(enc.Modes)
	if !enc.Enabled {
		return enc
	}
	if e.modes[modeMouseSGR] {
		enc.Mode = "sgr"
	} else {
		enc.Mode = "x10"
	}
	return enc
}

// MouseState returns the negotiated protocol plus the synthetic state an
// agent left behind (virtual cursor, held buttons, last event).
func (e *Emulator) MouseState() MouseStateJSON {
	enc := e.MouseEncoding()
	st := MouseStateJSON{
		Enabled:        enc.Enabled,
		Mode:           enc.Mode,
		Modes:          enc.Modes,
		BracketedPaste: e.BracketedPaste(),
		CursorCol:      e.mouseCol,
		CursorRow:      e.mouseRow,
		LastEvent:      e.mouseLast,
	}
	for b := range e.mouseHeld {
		st.Held = append(st.Held, b)
	}
	sort.Strings(st.Held)
	return st
}

// mouseButtons maps wire button names onto the X11 button codes x/vt and
// ansi agree on.
var mouseButtons = map[string]uv.MouseButton{
	"left":       uv.MouseLeft,
	"middle":     uv.MouseMiddle,
	"right":      uv.MouseRight,
	"wheelup":    uv.MouseWheelUp,
	"wheeldown":  uv.MouseWheelDown,
	"wheelleft":  uv.MouseWheelLeft,
	"wheelright": uv.MouseWheelRight,
	"backward":   uv.MouseBackward,
	"forward":    uv.MouseForward,
	"button10":   uv.MouseButton10,
	"button11":   uv.MouseButton11,
}

// MouseButtonNames returns the accepted button names (for error messages).
func MouseButtonNames() []string {
	out := make([]string, 0, len(mouseButtons))
	for name := range mouseButtons {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// mouseButtonFor resolves a button name (empty means "none").
func mouseButtonFor(name string) (uv.MouseButton, bool) {
	if name == "" || name == "none" {
		return uv.MouseNone, true
	}
	b, ok := mouseButtons[strings.ToLower(name)]
	return b, ok
}

// isWheel reports whether a button is a wheel step.
func isWheel(b uv.MouseButton) bool {
	return b == uv.MouseWheelUp || b == uv.MouseWheelDown ||
		b == uv.MouseWheelLeft || b == uv.MouseWheelRight
}

// EncodeMouse turns one mouse request into the bytes to write into the PTY.
//
// The wire form reuses x/ansi's encoders (ansi.EncodeMouseButton + MouseX10 /
// MouseSgr) so the bit layout is never hand-rolled: SGR under mode 1006,
// otherwise the classic X10 form with +32 offsets. The event is rejected with
// ErrMouseDisabled when the application enabled no mouse mode and Force is
// unset. Out-of-range coordinates are rejected with ErrMouseOutOfBounds.
func (e *Emulator) EncodeMouse(ev MouseEvent) ([]byte, error) {
	enabled := e.MouseEnabled()
	if !enabled && !ev.Force {
		return nil, ErrMouseDisabled
	}
	// --force: no mode is on, send SGR anyway (tu --force semantics).
	sgr := e.modes[modeMouseSGR] || (!enabled && ev.Force)

	if err := e.checkBounds(ev); err != nil {
		return nil, err
	}

	btn, ok := mouseButtonFor(ev.Button)
	if !ok {
		return nil, fmt.Errorf("%w: %q (known: %s)", ErrMouseButton, ev.Button, strings.Join(MouseButtonNames(), ", "))
	}

	var out []byte
	switch ev.Action {
	case MouseClickAction:
		if btn == uv.MouseNone {
			return nil, fmt.Errorf("%w: click needs a button", ErrMouseButton)
		}
		clicks := ev.Clicks
		if clicks < 1 {
			clicks = 1
		}
		for i := 0; i < clicks; i++ {
			out = append(out, encodeMouse(ev, btn, false, false, sgr)...)         // press
			out = append(out, encodeMouse(ev, uv.MouseNone, false, true, sgr)...) // release
		}
		e.markHeld(ev.Button, false)

	case MouseDownAction:
		if btn == uv.MouseNone {
			return nil, fmt.Errorf("%w: down needs a button", ErrMouseButton)
		}
		out = append(out, encodeMouse(ev, btn, false, false, sgr)...)
		e.markHeld(ev.Button, true)

	case MouseUpAction:
		up := btn
		if up == uv.MouseNone {
			// A bare "up" releases whatever is held.
			up = e.heldButton()
		}
		if up == uv.MouseNone {
			up = uv.MouseLeft
		}
		out = append(out, encodeMouse(ev, uv.MouseNone, false, true, sgr)...)
		e.markHeld(buttonNameOf(up), false)

	case MouseMoveAction:
		out = append(out, encodeMouse(ev, uv.MouseNone, true, false, sgr)...)

	case MouseDragAction:
		dragBtn := btn
		if dragBtn == uv.MouseNone {
			dragBtn = e.heldButton()
		}
		if dragBtn == uv.MouseNone {
			dragBtn = uv.MouseLeft
		}
		// Interpolate the intermediate motion events along the path — a
		// drag-aware control only sees the endpoints otherwise (tu's
		// handle_mouse_glided).
		for _, p := range interpolate(ev.Col, ev.Row, ev.ToCol, ev.ToRow) {
			step := ev
			step.Col, step.Row = p.col, p.row
			out = append(out, encodeMouse(step, dragBtn, true, false, sgr)...)
		}
		e.markHeld(buttonNameOf(dragBtn), true)

	case MouseScrollAction:
		if !isWheel(btn) {
			return nil, fmt.Errorf("%w: scroll needs a wheel button (wheelup/wheeldown/wheelleft/wheelright), got %q", ErrMouseButton, ev.Button)
		}
		n := ev.Amount
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, encodeMouse(ev, btn, false, false, sgr)...)
		}

	default:
		return nil, fmt.Errorf("%w: %q (known: click, down, up, move, drag, scroll)", ErrMouseAction, ev.Action)
	}

	e.mouseCol, e.mouseRow = ev.Col, ev.Row
	e.mouseLast = describeMouse(ev)
	return out, nil
}

// checkBounds rejects cells outside the current screen.
func (e *Emulator) checkBounds(ev MouseEvent) error {
	cols, rows := e.vt.Width(), e.vt.Height()
	if ev.Col < 0 || ev.Col >= cols || ev.Row < 0 || ev.Row >= rows {
		return fmt.Errorf("%w: (%d,%d) outside %dx%d", ErrMouseOutOfBounds, ev.Col, ev.Row, cols, rows)
	}
	if ev.Action == MouseDragAction {
		if ev.ToCol < 0 || ev.ToCol >= cols || ev.ToRow < 0 || ev.ToRow >= rows {
			return fmt.Errorf("%w: drag target (%d,%d) outside %dx%d", ErrMouseOutOfBounds, ev.ToCol, ev.ToRow, cols, rows)
		}
	}
	return nil
}

// encodeMouse encodes one event with the given button.
func encodeMouse(ev MouseEvent, button uv.MouseButton, motion, release, sgr bool) []byte {
	b := ansi.EncodeMouseButton(button, motion, ev.Mods.Shift, ev.Mods.Alt, ev.Mods.Ctrl)
	if sgr {
		return []byte(ansi.MouseSgr(b, ev.Col, ev.Row, release))
	}
	return []byte(ansi.MouseX10(b, ev.Col, ev.Row))
}

// point is one interpolated drag step.
type point struct{ col, row int }

// absInt is the integer absolute value (Go has no abs builtin for ints).
func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// interpolate returns the motion path from (c0,r0) to (c1,r1) inclusive of
// both ends, capped so a long drag cannot emit an unbounded byte burst.
func interpolate(c0, r0, c1, r1 int) []point {
	const maxSteps = 256
	dc, dr := c1-c0, r1-r0
	steps := max(absInt(dc), absInt(dr))
	if steps == 0 {
		return []point{{c0, r0}}
	}
	if steps > maxSteps {
		steps = maxSteps
	}
	out := make([]point, 0, steps+1)
	for i := 0; i <= steps; i++ {
		out = append(out, point{
			col: c0 + dc*i/steps,
			row: r0 + dr*i/steps,
		})
	}
	return out
}

// markHeld updates the synthetic held-button set.
func (e *Emulator) markHeld(button string, held bool) {
	if button == "" || button == "none" {
		return
	}
	if held {
		e.mouseHeld[button] = true
		return
	}
	delete(e.mouseHeld, button)
}

// heldButton returns any currently held button (the last-resort drag button).
func (e *Emulator) heldButton() uv.MouseButton {
	for name := range e.mouseHeld {
		if b, ok := mouseButtons[name]; ok {
			return b
		}
	}
	return uv.MouseNone
}

// buttonNameOf maps a button back to its wire name.
func buttonNameOf(b uv.MouseButton) string {
	for name, bb := range mouseButtons {
		if bb == b {
			return name
		}
	}
	return ""
}

// describeMouse renders "left@12,3" style summaries for `mouse state`.
func describeMouse(ev MouseEvent) string {
	var sb strings.Builder
	sb.WriteString(string(ev.Action))
	if ev.Button != "" {
		sb.WriteString(" " + ev.Button)
	}
	fmt.Fprintf(&sb, "@%d,%d", ev.Col, ev.Row)
	if ev.Action == MouseDragAction {
		fmt.Fprintf(&sb, "->%d,%d", ev.ToCol, ev.ToRow)
	}
	return sb.String()
}
