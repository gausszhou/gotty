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
	return &Snapshot{
		Cols:          e.cols,
		Rows:          e.rows,
		CursorRow:     e.row,
		CursorCol:     e.col,
		CursorVisible: e.cursorVisible,
		Images:        append([]ImageAsset(nil), e.images...),
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

// Text renders the snapshot grid as plain text.
func (s *Snapshot) Text() string { return Text(s.grid) }

// CellsJSON returns the styled cell list of the snapshot grid.
func (s *Snapshot) CellsJSON() []CellJSON { return CellsJSON(s.grid) }

// PNG rasterizes the snapshot grid into PNG bytes.
func (s *Snapshot) PNG() ([]byte, error) { return PNG(s.grid, s.Images, s.CellW, s.CellH) }
