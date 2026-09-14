package capture

import (
	"regexp"
	"unicode/utf8"
)

// LocateText finds the center cell of the index-th occurrence of a literal
// needle on the grid (index 0 = first). Matching walks the cell grid row by
// row, left to right, top to bottom — not the whitespace-trimmed text rows —
// so column arithmetic stays exact and a match never crosses a row break.
//
// The returned cell is the middle of the matched span (a wide character
// resolves to its leading cell), which is what a click on a label wants:
// "press the button that says OK" without guessing coordinates.
func (g *Grid) LocateText(needle string, index int) (row, col int, ok bool) {
	if needle == "" {
		return 0, 0, false
	}
	re, err := regexp.Compile(regexp.QuoteMeta(needle))
	if err != nil {
		return 0, 0, false
	}
	return g.LocateRegex(re, index)
}

// LocateRegex is LocateText for a compiled pattern. A zero-length match is
// skipped (there is no cell to click). A nil pattern finds nothing.
func (g *Grid) LocateRegex(re *regexp.Regexp, index int) (row, col int, ok bool) {
	if re == nil {
		return 0, 0, false
	}
	if index < 0 {
		index = 0
	}
	seen := 0
	for r := 0; r < g.rows; r++ {
		text, runeCol, runeWidth := g.rowRunes(r)
		if text == "" {
			continue
		}
		for _, m := range re.FindAllStringIndex(text, -1) {
			if m[0] == m[1] {
				continue
			}
			startRune := utf8.RuneCountInString(text[:m[0]])
			endRune := utf8.RuneCountInString(text[:m[1]])
			if endRune > len(runeCol) {
				endRune = len(runeCol)
			}
			if startRune >= endRune {
				continue
			}
			if seen != index {
				seen++
				continue
			}
			// The span occupies cells [runeCol[start], lastCell].
			startCol := runeCol[startRune]
			lastIdx := endRune - 1
			endCol := runeCol[lastIdx] + max(1, runeWidth[lastIdx]) - 1
			return r, (startCol + endCol) / 2, true
		}
	}
	return 0, 0, false
}

// rowRunes materializes one row as a string of its printable runes plus the
// column and display width of each rune. Wide-character continuation cells
// (Rune == 0) are skipped, so the column of each rune is exact.
func (g *Grid) rowRunes(r int) (text string, cols []int, widths []int) {
	runes := make([]rune, 0, g.cols)
	cols = make([]int, 0, g.cols)
	widths = make([]int, 0, g.cols)
	for c := 0; c < g.cols; c++ {
		cell := g.Cell(r, c)
		if cell.Rune == 0 {
			continue
		}
		runes = append(runes, cell.Rune)
		cols = append(cols, c)
		widths = append(widths, widthCond.RuneWidth(cell.Rune))
	}
	return string(runes), cols, widths
}
