package capture

import (
	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"
)

// ScrollbackView is a snapshot of the main screen's history (0005 §2.1).
type ScrollbackView struct {
	// Disabled is true when the session was configured with no scrollback
	// budget (`--scrollback=0`); the API answers 503, like --mirror=false.
	Disabled bool
	// Alternate is true when the terminal is on the alternate screen. A
	// full-screen program owns the screen and its own paging, so the main
	// screen's history is not what "scrollback" means right now; the API
	// returns an empty result with alternate_screen: true rather than an
	// error. (x/vt's own Scrollback() keeps returning the main buffer even
	// in alt mode, so this decision is made here.)
	Alternate bool
	// Total is the number of history lines currently retained.
	Total int
	// Grid holds the returned lines, oldest first, padded to the screen width.
	Grid *Grid
	// CellW, CellH are the configured cell pixel sizes (PNG rendering).
	CellW, CellH int
}

// ScrollbackDisabled reports whether the session has no scrollback budget.
func (e *Emulator) ScrollbackDisabled() bool { return e.scrollbackDisabled }

// ScrollbackLines returns the configured capacity (0 = disabled).
func (e *Emulator) ScrollbackLines() int {
	if e.scrollbackDisabled {
		return 0
	}
	return e.scrollbackLines
}

// SetScrollbackLines retunes the history capacity after construction, used by
// the per-session `scrollback` creation parameter. maxLines semantics match
// NewEmulatorWithScrollback.
func (e *Emulator) SetScrollbackLines(maxLines int) {
	e.scrollbackLines = maxLines
	switch {
	case maxLines > 0:
		e.scrollbackDisabled = false
		e.vt.SetScrollbackSize(maxLines)
	case maxLines == 0:
		e.scrollbackDisabled = true
		e.vt.ClearScrollback()
		e.vt.SetScrollbackSize(1) // see NewEmulatorWithScrollback: no real "off"
	default:
		e.scrollbackDisabled = false
		e.vt.SetScrollbackSize(vt.DefaultScrollbackSize)
	}
}

// Scrollback returns the retained history as a grid.
//
// limit <= 0 returns everything; limit > 0 returns the most recent limit
// lines (tail-first — an agent almost always wants recent context, and a
// 1000-line buffer read whole is a lot of tokens).
func (e *Emulator) Scrollback(limit int) ScrollbackView {
	view := ScrollbackView{
		Disabled:  e.scrollbackDisabled,
		Alternate: e.vt.IsAltScreen(),
		CellW:     e.cellW,
		CellH:     e.cellH,
	}
	if view.Disabled {
		return view
	}
	sb := e.vt.Scrollback()
	if sb == nil {
		return view
	}
	lines := sb.Lines()
	view.Total = len(lines)
	if view.Alternate {
		// History exists but is not the current screen; report the total
		// without materializing a grid the caller should not show.
		return view
	}
	if limit > 0 && len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	cols := e.vt.Width()
	rows := make([][]Cell, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, scrollbackLineToCells(line, cols))
	}
	view.Grid = gridFromRows(rows, cols)
	return view
}

// scrollbackLineToCells converts one history line into a full-width cell row.
// A uv line is stored with trailing empty cells trimmed and wide characters
// followed by a zero-width continuation cell, mirroring Screen's materialization.
func scrollbackLineToCells(line uv.Line, cols int) []Cell {
	row := make([]Cell, cols)
	for i := range row {
		row[i] = blankCell(DefaultColor())
	}
	for x := 0; x < len(line) && x < cols; x++ {
		c := line[x]
		if c.Width == 0 {
			continue // 宽字符续列
		}
		row[x] = vtCellToGrid(&c)
		if c.Width > 1 && x+1 < cols {
			row[x+1] = Cell{}
			x++
		}
	}
	return row
}

// gridFromRows builds a Grid from pre-materialized rows.
func gridFromRows(rows [][]Cell, cols int) *Grid {
	g := newGrid(len(rows), cols)
	for r, row := range rows {
		copy(g.cells[r], row)
	}
	return g
}
