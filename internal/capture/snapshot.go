package capture

import "time"

// Snapshot is a point-in-time view of an emulator screen. The grid is
// deep-copied so callers can render it (text/json/png) without holding
// the emulator's lock; Images shares the decoded pixel data, which is
// immutable after extraction.
type Snapshot struct {
	Cols, Rows    int
	CursorRow     int
	CursorCol     int
	CursorVisible bool
	Images        []ImageAsset
	CellW, CellH  int
	TakenAt       time.Time

	grid *Grid
}

// Snapshot returns a deep copy of the current screen state.
func (e *Emulator) Snapshot() *Snapshot {
	row, col := e.Cursor()
	return &Snapshot{
		Cols:          e.Cols(),
		Rows:          e.Rows(),
		CursorRow:     row,
		CursorCol:     col,
		CursorVisible: e.CursorVisible(),
		Images:        append([]ImageAsset(nil), e.Images()...),
		CellW:         e.cellW,
		CellH:         e.cellH,
		TakenAt:       time.Now(),
		grid:          cloneGrid(e.Screen()),
	}
}

// cloneGrid deep-copies a grid (element-wise, so rows never alias).
func cloneGrid(g *Grid) *Grid {
	cp := newGrid(g.Rows(), g.Cols())
	for r := 0; r < g.Rows(); r++ {
		copy(cp.cells[r], g.cells[r])
	}
	return cp
}

// Grid returns the snapshot's private grid copy, for callers that need
// per-cell access (text location, monitor row diffing).
func (s *Snapshot) Grid() *Grid { return s.grid }

// Text renders the snapshot grid as plain text.
func (s *Snapshot) Text() string { return Text(s.grid) }

// RowTexts returns the plain text of every row, trailing spaces trimmed.
// Row indices line up with the grid, so a text-level diff yields the rows a
// monitor must repaint.
func (s *Snapshot) RowTexts() []string {
	out := make([]string, s.grid.Rows())
	for r := range out {
		out[r] = RowText(s.grid, r)
	}
	return out
}

// RowCells returns the styled cells of one row.
func (s *Snapshot) RowCells(r int) []CellJSON { return RowCellsJSON(s.grid, r) }

// CellsJSON returns the styled cell list of the snapshot grid.
func (s *Snapshot) CellsJSON() []CellJSON { return CellsJSON(s.grid) }

// PNG rasterizes the snapshot grid into PNG bytes; opts select an external
// font (--font) and the virtual mouse-cursor overlay.
func (s *Snapshot) PNG(opts ...RenderOption) ([]byte, error) {
	return PNG(s.grid, s.Images, s.CellW, s.CellH, opts...)
}
