// Package capture implements the gotty capture engine: a minimal VT
// terminal emulator that renders a PTY byte stream into a screen grid,
// output renderers (text / json / html, PNG in M2) and the driver that
// runs a command and snapshots the rendered result.
package capture

import (
	"strconv"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// ColorMode describes how a cell color is interpreted.
type ColorMode uint8

const (
	// ColorDefault means "unset": use the terminal default color.
	ColorDefault ColorMode = iota
	// ColorIndex is an index into the 0-255 ANSI palette.
	ColorIndex
	// ColorRGB is a direct 24-bit color (0xRRGGBB).
	ColorRGB
)

// Color is the color of one cell.
type Color struct {
	Mode  ColorMode
	Index uint8  // valid when Mode == ColorIndex
	RGB   uint32 // valid when Mode == ColorRGB, 0xRRGGBB
}

// DefaultColor returns the "unset" color.
func DefaultColor() Color { return Color{Mode: ColorDefault} }

// Cell is a single screen cell.
type Cell struct {
	Rune rune
	Fg   Color
	Bg   Color

	Bold          bool
	Dim           bool
	Italic        bool
	Underline     bool
	Blink         bool
	Reverse       bool
	Invisible     bool
	Strikethrough bool
}

// blankCell is the cell an erased/new line gets: the current background is
// inherited (xterm erase semantics), the foreground defaults to unset.
func blankCell(bg Color) Cell { return Cell{Rune: ' ', Bg: bg} }

// style is the SGR state applied to newly written cells.
type style struct {
	fg, bg Color

	bold          bool
	dim           bool
	italic        bool
	underline     bool
	blink         bool
	reverse       bool
	invisible     bool
	strikethrough bool
}

func (s *style) reset() { *s = style{fg: DefaultColor(), bg: DefaultColor()} }

func (s style) applyTo(c *Cell) {
	c.Fg = s.fg
	c.Bg = s.bg
	c.Bold = s.bold
	c.Dim = s.dim
	c.Italic = s.italic
	c.Underline = s.underline
	c.Blink = s.blink
	c.Reverse = s.reverse
	c.Invisible = s.invisible
	c.Strikethrough = s.strikethrough
}

// Grid is a rectangular cell buffer (one screen).
type Grid struct {
	rows, cols int
	cells      [][]Cell
}

func newGrid(rows, cols int) *Grid {
	g := &Grid{rows: rows, cols: cols, cells: make([][]Cell, rows)}
	for r := 0; r < rows; r++ {
		g.cells[r] = make([]Cell, cols)
		for c := 0; c < cols; c++ {
			g.cells[r][c] = blankCell(DefaultColor())
		}
	}
	return g
}

// Rows returns the number of screen rows.
func (g *Grid) Rows() int { return g.rows }

// Cols returns the number of screen columns.
func (g *Grid) Cols() int { return g.cols }

// Cell returns the cell at (row, col); out-of-bounds reads yield a blank cell.
func (g *Grid) Cell(r, c int) Cell {
	if r < 0 || r >= g.rows || c < 0 || c >= g.cols {
		return blankCell(DefaultColor())
	}
	return g.cells[r][c]
}

func (g *Grid) set(r, c int, cell Cell) {
	if r < 0 || r >= g.rows || c < 0 || c >= g.cols {
		return
	}
	g.cells[r][c] = cell
}

// fill sets the inclusive rectangle (r1,c1)-(r2,c2) to blank cells with bg.
func (g *Grid) fill(r1, c1, r2, c2 int, bg Color) {
	for r := r1; r <= r2; r++ {
		if r < 0 || r >= g.rows {
			continue
		}
		for c := c1; c <= c2; c++ {
			if c < 0 || c >= g.cols {
				continue
			}
			g.cells[r][c] = blankCell(bg)
		}
	}
}

func (g *Grid) clear(bg Color) { g.fill(0, 0, g.rows-1, g.cols-1, bg) }

// scrollUp shifts the region [top, bottom] up by one, filling the last line.
// Cells are copied element-wise: assigning slice references would alias rows
// and a later fill would clear the moved content as well.
func (g *Grid) scrollUp(top, bottom int, bg Color) {
	for r := top; r < bottom; r++ {
		copy(g.cells[r], g.cells[r+1])
	}
	for c := 0; c < g.cols; c++ {
		g.cells[bottom][c] = blankCell(bg)
	}
}

// scrollDown shifts the region [top, bottom] down by one, filling the first line.
func (g *Grid) scrollDown(top, bottom int, bg Color) {
	for r := bottom; r > top; r-- {
		copy(g.cells[r], g.cells[r-1])
	}
	for c := 0; c < g.cols; c++ {
		g.cells[top][c] = blankCell(bg)
	}
}

// parseState is the terminal parser's state machine node.
type parseState uint8

const (
	stGround    parseState = iota
	stEsc                  // ESC received, one byte dispatch
	stEscSelect            // ESC ( or ESC ): consume one charset byte, ignore
	stCSI                  // ESC [ ... collected until a final byte
	stOSC                  // ESC ] ... collected until BEL or ESC \
	stOSCEnd               // within OSC, ESC seen: expect '\'
	stDCS                  // ESC P ... collected until ESC \
	stDCSEnd               // within DCS, ESC seen: expect '\'
	stAPC                  // ESC _ ... collected until ESC \ (kitty graphics)
	stAPCEnd               // within APC, ESC seen: expect '\'
)

// gfxBufLimit caps the per-sequence buffers for OSC/DCS/APC payloads, so a
// hostile or broken stream cannot balloon memory. Anything beyond the cap
// is dropped as a whole sequence.
const gfxBufLimit = 16 << 20

// csiData accumulates one CSI sequence.
type csiData struct {
	priv     bool   // '?' private prefix seen
	paramStr []byte // digits, ';' and ':' between priv/params and final
	final    byte
}

// widthCond 固定 EastAsianWidth=false:ambiguous 字符(如带重音的拉丁字母)
// 始终按 1 格处理,不随服务端 locale 漂移,与浏览端 xterm.js 的默认语义
// 一致(中文全角字符不受影响,仍算 2 格)。
var widthCond = &runewidth.Condition{EastAsianWidth: false}

// Emulator is a minimal VT terminal emulator: feed it a PTY byte stream
// and snapshot the screen grid. It implements the subset needed by the
// capture feature: printable UTF-8 text, cursor movement, erasure,
// scrolling with a scroll region, SGR (16/256/24-bit colors), the
// alternate screen and window-size queries are consumed and ignored.
//
// Two screen buffers (main/alternate) are maintained; ?1049 switches
// between them. No scrollback is kept — the grid is exactly cols×rows.
type Emulator struct {
	cols, rows int
	main, alt  *Grid
	useAlt     bool

	style style
	row   int
	col   int
	// savedRow/Col back ESC 7/8, CSI s/u and the ?1049 entry/exit pair.
	savedRow, savedCol int
	cursorVisible      bool

	// scrollTop/scrollBottom bound the scrolling region (inclusive).
	scrollTop, scrollBottom int

	pendingWrap bool // a full-width write happened at the last column

	// cellW/cellH are the pixel size of one grid cell, used to convert
	// graphics-protocol placements (pixels) into grid cells.
	cellW, cellH int

	// images holds every picture extracted from the output stream so far.
	images []ImageAsset

	// kittyPending accumulates a kitty transmission across APC chunks.
	kittyPending kittyPending

	// per-sequence payload buffers for graphics extraction.
	oscBuf, dcsBuf, apcBuf    []byte
	oscFull, dcsFull, apcFull bool

	state    parseState
	csi      csiData
	utf8Buf  [utf8.UTFMax]byte
	utf8Got  int
	utf8Need int
}

// NewEmulator creates an emulator with a cols×rows screen.
func NewEmulator(cols, rows int) *Emulator {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	e := &Emulator{
		cols:          cols,
		rows:          rows,
		main:          newGrid(rows, cols),
		alt:           newGrid(rows, cols),
		cursorVisible: true,
		scrollBottom:  rows - 1,
		cellW:         9,
		cellH:         18,
	}
	return e
}

// SetCellSize sets the pixel size of one grid cell (default 9×18). It is
// used to convert graphics-protocol placements into grid cells and to
// rasterize PNG output.
func (e *Emulator) SetCellSize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	e.cellW, e.cellH = w, h
}

// Cols returns the terminal width.
func (e *Emulator) Cols() int { return e.cols }

// Rows returns the terminal height.
func (e *Emulator) Rows() int { return e.rows }

// Screen returns the active (main or alternate) grid.
func (e *Emulator) Screen() *Grid {
	if e.useAlt {
		return e.alt
	}
	return e.main
}

// Cursor returns the current cursor position.
func (e *Emulator) Cursor() (row, col int) { return e.row, e.col }

// CursorVisible reports whether the cursor is shown (DECSET/DECRST 25).
func (e *Emulator) CursorVisible() bool { return e.cursorVisible }

// Images returns every graphics-protocol picture extracted so far.
func (e *Emulator) Images() []ImageAsset { return e.images }

// Write feeds raw terminal output into the emulator; it implements io.Writer.
func (e *Emulator) Write(p []byte) (int, error) {
	for _, b := range p {
		e.writeByte(b)
	}
	return len(p), nil
}

func (e *Emulator) grid() *Grid { return e.Screen() }

func (e *Emulator) writeByte(b byte) {
	switch e.state {
	case stGround:
		e.ground(b)
	case stEsc:
		e.esc(b)
	case stEscSelect:
		// 字符集选择(ESC ( B 等):消费即忽略
		e.state = stGround
	case stCSI:
		e.csiByte(b)
	case stOSC:
		if b == 0x07 { // BEL 结束 OSC
			e.handleOSC(e.oscBuf)
			e.state = stGround
		} else if b == 0x1b {
			e.state = stOSCEnd
		} else {
			e.oscAppend(b)
		}
	case stOSCEnd:
		// ESC \ 结束 OSC(BEL 之外的标准终结符);其他字节被吞掉,
		// 当作 OSC 内容继续收集(不 append,裸 ESC 内容本就非法)
		if b == '\\' {
			e.handleOSC(e.oscBuf)
			e.state = stGround
		} else {
			e.state = stOSC
		}
	case stDCS:
		if b == 0x1b {
			e.state = stDCSEnd
		} else {
			e.dcsAppend(b)
		}
	case stDCSEnd:
		if b == '\\' {
			e.handleDCS(e.dcsBuf)
			e.state = stGround
		} else {
			e.state = stDCS
		}
	case stAPC:
		if b == 0x1b {
			e.state = stAPCEnd
		} else {
			e.apcAppend(b)
		}
	case stAPCEnd:
		if b == '\\' {
			e.handleAPC(e.apcBuf)
			e.state = stGround
		} else {
			e.state = stAPC
		}
	}
}

// oscAppend/dcsAppend/apcAppend accumulate sequence payloads, dropping the
// whole sequence once it exceeds gfxBufLimit.
func (e *Emulator) oscAppend(b byte) {
	if e.oscFull {
		return
	}
	if len(e.oscBuf) >= gfxBufLimit {
		e.oscFull = true
		e.oscBuf = nil
		return
	}
	e.oscBuf = append(e.oscBuf, b)
}

func (e *Emulator) dcsAppend(b byte) {
	if e.dcsFull {
		return
	}
	if len(e.dcsBuf) >= gfxBufLimit {
		e.dcsFull = true
		e.dcsBuf = nil
		return
	}
	e.dcsBuf = append(e.dcsBuf, b)
}

func (e *Emulator) apcAppend(b byte) {
	if e.apcFull {
		return
	}
	if len(e.apcBuf) >= gfxBufLimit {
		e.apcFull = true
		e.apcBuf = nil
		return
	}
	e.apcBuf = append(e.apcBuf, b)
}

func (e *Emulator) abortUTF8() {
	e.utf8Got, e.utf8Need = 0, 0
}

func (e *Emulator) ground(b byte) {
	switch {
	case b == 0x1b:
		e.abortUTF8()
		e.state = stEsc
	case b == 0x08: // BS
		if e.col > 0 {
			e.col--
		}
	case b == 0x09: // TAB
		e.col = min(e.cols-1, (e.col/8+1)*8)
	case b == 0x0a || b == 0x0b || b == 0x0c: // LF/VT/FF
		e.lineFeed()
	case b == 0x0d: // CR
		e.col = 0
	case b == 0x07 || b == 0x0e || b == 0x0f: // BEL/SO/SI: ignore
	case b < 0x20 || b == 0x7f: // 其他控制字符:忽略
	case b >= 0x80:
		e.utf8Byte(b)
	default:
		e.putRune(rune(b))
	}
}

func (e *Emulator) esc(b byte) {
	switch {
	case b == '[':
		e.state = stCSI
		e.csi = csiData{}
	case b == ']':
		e.state = stOSC
		e.oscBuf = e.oscBuf[:0]
		e.oscFull = false
	case b == 'P':
		e.state = stDCS
		e.dcsBuf = e.dcsBuf[:0]
		e.dcsFull = false
	case b == '_':
		e.state = stAPC
		e.apcBuf = e.apcBuf[:0]
		e.apcFull = false
	case b == '(' || b == ')':
		e.state = stEscSelect
	case b == '7': // DECSC: 保存光标
		e.savedRow, e.savedCol = e.row, e.col
		e.state = stGround
	case b == '8': // DECRC: 恢复光标
		e.row, e.col = e.savedRow, e.savedCol
		e.pendingWrap = false
		e.state = stGround
	case b == 'D': // IND
		e.lineFeed()
		e.state = stGround
	case b == 'M': // RI
		e.reverseIndex()
		e.state = stGround
	case b == 'E': // NEL
		e.lineFeed()
		e.col = 0
		e.state = stGround
	case b == 'c': // RIS
		e.reset()
		e.state = stGround
	default: // 未知 ESC 序列:按两字节消费忽略
		e.state = stGround
	}
}

func (e *Emulator) csiByte(b byte) {
	switch {
	case b == 0x1b:
		// ESC 取消进行中的 CSI 序列,自身作为新转义的开头
		e.csi = csiData{}
		e.state = stEsc
	case b >= 0x30 && b <= 0x39 || b == 0x3b || b == 0x3a:
		e.csi.paramStr = append(e.csi.paramStr, b)
	case b == '?':
		e.csi.priv = true
	case b >= 0x40 && b <= 0x7e: // final byte
		e.csi.final = b
		e.dispatchCSI()
		e.state = stGround
		// 0x20-0x2f intermediate 及其他:吞掉,等 final
	}
}

func (e *Emulator) utf8Byte(b byte) {
	if e.utf8Got == 0 {
		switch {
		case b&0xe0 == 0xc0:
			e.utf8Need = 2
		case b&0xf0 == 0xe0:
			e.utf8Need = 3
		case b&0xf8 == 0xf0:
			e.utf8Need = 4
		default:
			return // 无效首字节:忽略
		}
		e.utf8Buf[0] = b
		e.utf8Got = 1
		return
	}
	if b&0xc0 != 0x80 {
		// 续字节不连续:丢弃已缓冲,把当前字节当新序列开头
		e.utf8Need, e.utf8Got = 0, 0
		e.utf8Byte(b)
		return
	}
	e.utf8Buf[e.utf8Got] = b
	e.utf8Got++
	if e.utf8Got == e.utf8Need {
		r, _ := utf8.DecodeRune(e.utf8Buf[:e.utf8Got])
		e.utf8Need, e.utf8Got = 0, 0
		e.putRune(r)
	}
}

func (e *Emulator) putRune(r rune) {
	if r == kittyPlaceholder {
		// kitty 协议的可视占位字符(U+10EEEE):本仿真器不支持虚拟占位,
		// 直接跳过,避免在文本里显示奇怪字符。
		return
	}
	w := widthCond.RuneWidth(r)
	if w <= 0 {
		// 组合/零宽字符:M1 忽略
		return
	}
	if e.pendingWrap {
		e.lineFeed()
		e.col = 0
		e.pendingWrap = false
	}
	// 宽字符放不进最后一格:先换行再写
	if w == 2 && e.col >= e.cols-1 {
		e.lineFeed()
		e.col = 0
	}

	cell := blankCell(e.style.bg)
	cell.Rune = r
	e.style.applyTo(&cell)
	e.grid().set(e.row, e.col, cell)
	e.col++
	if w == 2 {
		// 宽字符的第二格是占位格(Rune==0),渲染时跳过
		e.grid().set(e.row, e.col, Cell{})
		e.col++
	}
	if e.col >= e.cols {
		e.col = e.cols - 1
		e.pendingWrap = true
	}
}

// lineFeed moves the cursor down one line, scrolling the region at its
// bottom edge (xterm behavior: a cursor outside the scroll region just
// moves without scrolling).
func (e *Emulator) lineFeed() {
	if e.row == e.scrollBottom {
		e.grid().scrollUp(e.scrollTop, e.scrollBottom, e.style.bg)
	} else if e.row < e.rows-1 {
		e.row++
	}
}

func (e *Emulator) reverseIndex() {
	if e.row == e.scrollTop {
		e.grid().scrollDown(e.scrollTop, e.scrollBottom, e.style.bg)
	} else if e.row > 0 {
		e.row--
	}
}

// reset implements RIS: the whole terminal state returns to its defaults.
func (e *Emulator) reset() {
	e.main.clear(DefaultColor())
	e.alt.clear(DefaultColor())
	e.useAlt = false
	e.style.reset()
	e.row, e.col = 0, 0
	e.savedRow, e.savedCol = 0, 0
	e.scrollTop, e.scrollBottom = 0, e.rows-1
	e.pendingWrap = false
	e.cursorVisible = true
	e.images = nil
	e.kittyPending = kittyPending{}
	e.oscBuf, e.dcsBuf, e.apcBuf = nil, nil, nil
	e.oscFull, e.dcsFull, e.apcFull = false, false, false
}

// parseParams splits a CSI parameter string on ';' and ':' (':'-separated
// extensions like SGR 4:3 degrade gracefully). Empty segments and empty
// sequences yield 0, which dispatch treats as the per-instruction default.
func parseParams(b []byte) []int {
	params := []int{}
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == ';' || b[i] == ':' {
			seg := b[start:i]
			start = i + 1
			if len(seg) == 0 {
				params = append(params, 0)
				continue
			}
			n, err := strconv.Atoi(string(seg))
			if err != nil {
				n = 0
			}
			params = append(params, n)
		}
	}
	if len(params) == 0 {
		params = append(params, 0)
	}
	return params
}

// oneOr returns params[i] when present and non-zero, otherwise dflt.
func oneOr(params []int, i, dflt int) int {
	if i < len(params) && params[i] > 0 {
		return params[i]
	}
	return dflt
}

func (e *Emulator) dispatchCSI() {
	params := parseParams(e.csi.paramStr)
	if e.csi.priv {
		e.decPrivate(params, e.csi.final == 'h')
		return
	}
	switch e.csi.final {
	case 'A':
		e.row = max(0, e.row-oneOr(params, 0, 1))
	case 'B':
		e.row = min(e.rows-1, e.row+oneOr(params, 0, 1))
	case 'C':
		e.col = min(e.cols-1, e.col+oneOr(params, 0, 1))
	case 'D':
		e.col = max(0, e.col-oneOr(params, 0, 1))
	case 'E':
		e.row = min(e.rows-1, e.row+oneOr(params, 0, 1))
		e.col = 0
	case 'F':
		e.row = max(0, e.row-oneOr(params, 0, 1))
		e.col = 0
	case 'G':
		e.col = min(e.cols-1, max(0, oneOr(params, 0, 1)-1))
	case 'd':
		e.row = min(e.rows-1, max(0, oneOr(params, 0, 1)-1))
	case 'H', 'f':
		e.row = min(e.rows-1, max(0, oneOr(params, 0, 1)-1))
		e.col = min(e.cols-1, max(0, oneOr(params, 1, 1)-1))
		e.pendingWrap = false
	case 'J':
		e.eraseInDisplay(params[0])
	case 'K':
		e.eraseInLine(params[0])
	case 'm':
		e.applySGR(params)
	case 'r':
		e.setScrollRegion(params)
	case 's':
		e.savedRow, e.savedCol = e.row, e.col
	case 'u':
		e.row, e.col = e.savedRow, e.savedCol
		e.pendingWrap = false
	case 'L':
		e.insertLines(oneOr(params, 0, 1))
	case 'M':
		e.deleteLines(oneOr(params, 0, 1))
	case 'S':
		e.scrollUpLines(oneOr(params, 0, 1))
	case 'T':
		e.scrollDownLines(oneOr(params, 0, 1))
		// 其余(DEC private 之外、DSR 'n'、insert/delete char 等):忽略
	}
}

func (e *Emulator) decPrivate(params []int, set bool) {
	for _, p := range params {
		switch p {
		case 25:
			e.cursorVisible = set
		case 47, 1047, 1049:
			// 备用屏:1049 保存/恢复光标;47/1047 只切换不清屏的细微
			// 语义差异此处简化(进入一律清屏)。
			if set && !e.useAlt {
				e.savedRow, e.savedCol = e.row, e.col
				e.row, e.col = 0, 0
				e.pendingWrap = false
				e.useAlt = true
				e.alt.clear(e.style.bg)
			} else if !set && e.useAlt {
				e.useAlt = false
				e.row, e.col = e.savedRow, e.savedCol
				e.pendingWrap = false
			}
		}
	}
}

// eraseInDisplay implements CSI Ps J.
func (e *Emulator) eraseInDisplay(mode int) {
	switch mode {
	case 0: // 光标 → 屏尾,含光标格
		e.grid().fill(e.row, e.col, e.rows-1, e.cols-1, e.style.bg)
	case 1: // 屏首 → 光标:光标前的行整行擦除,最后一行擦到光标列
		e.grid().fill(0, 0, e.row-1, e.cols-1, e.style.bg)
		e.grid().fill(e.row, 0, e.row, e.col, e.style.bg)
	case 2, 3: // 全屏
		e.grid().clear(e.style.bg)
	}
}

// eraseInLine implements CSI Ps K.
func (e *Emulator) eraseInLine(mode int) {
	switch mode {
	case 0: // 光标 → 行尾,含光标格
		e.grid().fill(e.row, e.col, e.row, e.cols-1, e.style.bg)
	case 1: // 行首 → 光标,含光标格
		e.grid().fill(e.row, 0, e.row, e.col, e.style.bg)
	case 2: // 整行
		e.grid().fill(e.row, 0, e.row, e.cols-1, e.style.bg)
	}
}

// setScrollRegion implements CSI Ps;Ps r (DECSTBM). The cursor is homed
// afterwards, as DEC requires.
func (e *Emulator) setScrollRegion(params []int) {
	top := max(0, min(e.rows-1, oneOr(params, 0, 1)-1))
	bottom := e.rows - 1
	if len(params) > 1 && params[1] > 0 {
		bottom = min(e.rows-1, params[1]-1)
	}
	if top >= bottom {
		return
	}
	e.scrollTop, e.scrollBottom = top, bottom
	e.row, e.col = 0, 0
	e.pendingWrap = false
}

func (e *Emulator) scrollUpLines(n int) {
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		e.grid().scrollUp(e.scrollTop, e.scrollBottom, e.style.bg)
	}
}

func (e *Emulator) scrollDownLines(n int) {
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		e.grid().scrollDown(e.scrollTop, e.scrollBottom, e.style.bg)
	}
}

// insertLines implements CSI Ps L: blank lines push the cursor line down,
// the region's bottom lines are lost.
func (e *Emulator) insertLines(n int) {
	if e.row < e.scrollTop || e.row > e.scrollBottom || n <= 0 {
		return
	}
	g := e.grid()
	for r := e.scrollBottom; r >= e.row+n; r-- {
		copy(g.cells[r], g.cells[r-n])
	}
	for r := e.row; r < e.row+n && r <= e.scrollBottom; r++ {
		e.clearRow(r)
	}
}

// deleteLines implements CSI Ps M: the cursor line and the following ones
// move up, the region's bottom lines become blank.
func (e *Emulator) deleteLines(n int) {
	if e.row < e.scrollTop || e.row > e.scrollBottom || n <= 0 {
		return
	}
	g := e.grid()
	for r := e.row; r <= e.scrollBottom-n; r++ {
		copy(g.cells[r], g.cells[r+n])
	}
	for r := e.scrollBottom - n + 1; r <= e.scrollBottom; r++ {
		e.clearRow(r)
	}
}

func (e *Emulator) clearRow(r int) {
	e.grid().fill(r, 0, r, e.cols-1, e.style.bg)
}

// applySGR implements CSI Ps m (only the subset meaningful for capture).
func (e *Emulator) applySGR(params []int) {
	for i := 0; i < len(params); i++ {
		p := params[i]
		switch {
		case p == 0:
			e.style.reset()
		case p == 1:
			e.style.bold = true
		case p == 2:
			e.style.dim = true
		case p == 3:
			e.style.italic = true
		case p == 4 || p == 21:
			e.style.underline = true
		case p == 5:
			e.style.blink = true
		case p == 7:
			e.style.reverse = true
		case p == 8:
			e.style.invisible = true
		case p == 9:
			e.style.strikethrough = true
		case p == 22:
			e.style.bold, e.style.dim = false, false
		case p == 23:
			e.style.italic = false
		case p == 24:
			e.style.underline = false
		case p == 25:
			e.style.blink = false
		case p == 27:
			e.style.reverse = false
		case p == 28:
			e.style.invisible = false
		case p == 29:
			e.style.strikethrough = false
		case p >= 30 && p <= 37:
			e.style.fg = Color{Mode: ColorIndex, Index: uint8(p - 30)}
		case p == 38:
			if i+1 < len(params) && params[i+1] == 5 && i+2 < len(params) {
				e.style.fg = Color{Mode: ColorIndex, Index: uint8(params[i+2])}
				i += 2
			} else if i+1 < len(params) && params[i+1] == 2 && i+4 < len(params) {
				e.style.fg = Color{Mode: ColorRGB, RGB: rgb24(params[i+2], params[i+3], params[i+4])}
				i += 4
			}
		case p == 39:
			e.style.fg = DefaultColor()
		case p >= 40 && p <= 47:
			e.style.bg = Color{Mode: ColorIndex, Index: uint8(p - 40)}
		case p == 48:
			if i+1 < len(params) && params[i+1] == 5 && i+2 < len(params) {
				e.style.bg = Color{Mode: ColorIndex, Index: uint8(params[i+2])}
				i += 2
			} else if i+1 < len(params) && params[i+1] == 2 && i+4 < len(params) {
				e.style.bg = Color{Mode: ColorRGB, RGB: rgb24(params[i+2], params[i+3], params[i+4])}
				i += 4
			}
		case p == 49:
			e.style.bg = DefaultColor()
		case p >= 90 && p <= 97:
			e.style.fg = Color{Mode: ColorIndex, Index: uint8(p - 90 + 8)}
		case p >= 100 && p <= 107:
			e.style.bg = Color{Mode: ColorIndex, Index: uint8(p - 100 + 8)}
		}
	}
}

func rgb24(r, g, b int) uint32 {
	return uint32(clamp255(r))<<16 | uint32(clamp255(g))<<8 | uint32(clamp255(b))
}

func clamp255(n int) int {
	if n < 0 {
		return 0
	}
	if n > 255 {
		return 255
	}
	return n
}
