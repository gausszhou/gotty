// Package capture implements the gotty capture engine: it feeds a PTY byte
// stream through a VT terminal emulator and renders the resulting screen
// grid (text / json / html, PNG) plus graphics-protocol images (kitty /
// sixel / iTerm2).
//
// The VT emulation itself is provided by charmbracelet/x/vt (the same
// engine family that powers modern Charm terminals): a complete emulator
// with built-in query answers (DA/DSR/DECRQM/OSC colors), screen buffers
// with Resize and no scrollback exposure. This package wraps it with:
//
//   - the public snapshot API (Grid/Cell/Snapshot) used by the renderers;
//   - a query-answer queue (DrainAnswers) the capture driver and the
//     session mirror write back into the PTY;
//   - the graphics-protocol extractor (graphics.go), which stays in-house:
//     a byte-frame scanner runs in parallel with the emulator and decodes
//     kitty APC / sixel DCS / iTerm2 OSC payloads into ImageAssets;
//   - a few state bits x/vt does not expose (cursor visibility, RIS
//     handler) tracked from the raw stream.
package capture

import (
	"bytes"
	"image/color"
	"strconv"
	"time"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
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

// Grid is a rectangular cell buffer (one snapshot of a screen). It is
// materialized from the emulator, never mutated in place.
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

// widthCond 固定 EastAsianWidth=false:ambiguous 字符(如带重音的拉丁字母)
// 始终按 1 格处理,不随服务端 locale 漂移,与浏览端 xterm.js 的默认语义
// 一致(中文全角字符不受影响,仍算 2 格)。x/vt 的 WcWidth 对这两类字符
// 与 runewidth 判定一致(已由测试锁定),此处仍保留给 PNG 渲染的宽度判断。
var widthCond = &runewidth.Condition{EastAsianWidth: false}

// kittyPlaceholderUTF8 is the UTF-8 encoding of the kitty graphics unicode
// placeholder (U+10EEEE = F4 8E BB AE). The wrapper strips it from the
// stream before it reaches the emulator: the placeholder is a rendering
// hint for terminals with virtual-placeholder support, and would otherwise
// show as a junk glyph.
var kittyPlaceholderUTF8 = []byte("\U0010EEEE")

// ---------------------------------------------------------------------------
// 字节帧扫描器:parallel-to-emulator 的图形提取与模式跟踪

// gfxEvt is the outcome of feeding one byte to gfxScanner.
type gfxEvt uint8

const (
	gfxNone gfxEvt = iota
	// gfxRune:一条完整的可打印 rune 被识别(scanner.rn/r w/runeBuf/runeLen 有效)。
	// 这是宽字符行尾换行与 IRM 插入补偿的触发点。
	gfxRune
	gfxOSCPayload // 一条完整的 OSC 载荷(终止字节已消费)
	gfxDCSPayload // 一条完整的 DCS 载荷
	gfxAPCPayload // 一条完整的 APC 载荷
	// gfxCSIReplaced:一条需要整段替换的 CSI 完成(scanner.replacement)。
	// 用于 x/vt 未实现的 CSI s/u → 翻译为 DECSC/DECRC(ESC 7/8)。
	gfxCSIReplaced
)

// gfxScanState is the scanner's framing state machine node.
type gfxScanState uint8

const (
	gsScan   gfxScanState = iota
	gsEsc                 // ESC received, one byte dispatch
	gsCSI                 // ESC [ … collected until a final byte
	gsUTF8                // 收集中一个多字节 UTF-8 字符(续字节)
	gsOSC                 // ESC ] … collected until BEL or ESC \
	gsOSCEnd              // within OSC, ESC seen: expect '\'
	gsDCS                 // ESC P … collected until ESC \
	gsDCSEnd              // within DCS, ESC seen: expect '\'
	gsAPC                 // ESC _ … collected until ESC \ (kitty graphics)
	gsAPCEnd              // within APC, ESC seen: expect '\'
)

// gfxBufLimit caps the per-sequence payload buffers, so a hostile or broken
// stream cannot balloon memory. Anything beyond the cap is dropped as a
// whole sequence (mirrors the previous emulator-side limit).
const gfxBufLimit = 16 << 20

// repMax caps how many repetitions a single CSI b may inject, so a hostile
// "CSI 9999999 b" cannot balloon memory.
const repMax = 4096

// gfxScanner frames the byte stream into:
//
//   - OSC/DCS/APC payloads (for the graphics extractor);
//   - complete printable runes (to compensate x/vt gaps: a wide rune that
//     does not fit at the right edge must wrap to the next line instead of
//     being dropped, and IRM insert mode needs an ICH before each char);
//   - CSI sequences (mode bits the wrapper tracks itself: ?25, IRM-4,
//     and the s/u → DECSC/DECRC translation).
//
// It deliberately ignores the content of everything else: the emulator is
// the authority on rendering.
type gfxScanner struct {
	state gfxScanState
	buf   []byte
	full  bool

	// CSI collection state.
	priv     bool
	paramStr []byte
	// csiLen counts the bytes of the CSI sequence since its ESC (inclusive
	// of ESC and the final byte) — used to splice the s/u replacement.
	csiLen int

	// UTF-8 framing state (only in gsUTF8). The complete rune is emitted
	// as one gfxRune event; runeBuf holds its raw encoding.
	utf8Buf  [utf8.UTFMax]byte
	utf8Got  int
	utf8Need int
	runeLen  int
	rn       rune
	w        int
	runeBuf  [utf8.UTFMax]byte

	// insertMode (IRM, CSI 4 h/l) is tracked here so Write can inject an
	// ICH before printable runes while insert mode is active (x/vt does
	// not implement IRM in its print path).
	insertMode bool

	// lastRune remembers the most recently seen printable rune (all
	// widths) so CSI b (REP) can be translated — x/vt only records
	// single-width characters, so repeating a wide character would
	// silently no-op.
	lastRuneBuf [utf8.UTFMax]byte
	lastRuneLen int

	// queryHits counts terminal queries seen since the last drain
	// (DSR/DA/DECRQM finals + OSC color queries). DrainAnswers uses it to
	// decide whether a short wait for the answer pump is worthwhile —
	// chunks without queries must not pay the wait.
	queryHits int
	sawDollar bool // '$' intermediate seen in the current CSI

	// payload holds the completed OSC/DCS/APC payload; valid only right
	// after the corresponding advance() event.
	payload []byte

	// replacement holds the bytes to splice in place of a consumed CSI
	// sequence; valid only right after a gfxCSIReplaced event.
	replacement []byte

	// onMode fires for CSI … h/l with the parsed parameters.
	onMode func(priv bool, params []int, set bool)
	// onRIS fires when a RIS (ESC c) is seen; the wrapper resets its
	// own tracked state.
	onRIS func()
}

// startPayload begins collecting a new OSC/DCS/APC payload.
func (s *gfxScanner) startPayload() {
	s.buf = s.buf[:0]
	s.full = false
}

// appendPayload accumulates one payload byte, dropping the whole sequence
// once it exceeds gfxBufLimit.
func (s *gfxScanner) appendPayload(b byte) {
	if s.full {
		return
	}
	if len(s.buf) >= gfxBufLimit {
		s.full = true
		s.buf = nil
		return
	}
	s.buf = append(s.buf, b)
}

// reset returns the scanner to its initial state (used on RIS and Resize).
func (s *gfxScanner) reset() {
	onMode, onRIS := s.onMode, s.onRIS
	*s = gfxScanner{state: gsScan, onMode: onMode, onRIS: onRIS}
}

// bufferingRune reports whether the scanner is mid-way through a multi-byte
// UTF-8 character (Write must not forward the partial bytes directly; the
// whole rune is emitted on the gfxRune event).
func (s *gfxScanner) bufferingRune() bool { return s.state == gsUTF8 }

// countOSCQuery marks OSC 10/11/12 color queries ("Ps;?" / "Ps?") as
// answerable, so DrainAnswers waits briefly for x/vt's reply.
func (s *gfxScanner) countOSCQuery() {
	p := s.payload
	if len(p) < 2 || p[0] != '1' {
		return
	}
	i := 0
	for i < len(p) && p[i] >= '0' && p[i] <= '9' {
		i++
	}
	if i < 1 || i > 2 || i == len(p) {
		return // 仅 10|11|12
	}
	if p[i] == ';' {
		i++
	}
	if i < len(p) && p[i] == '?' {
		s.queryHits++
	}
}

// scanByte handles a byte seen in ground state (printable text): ASCII is
// emitted as an immediate rune; a UTF-8 lead byte starts rune collection.
func (s *gfxScanner) scanByte(b byte) gfxEvt {
	switch {
	case b == 0x1b:
		s.state = gsEsc
		return gfxNone
	case b < 0x80:
		s.rn, s.w = rune(b), 1
		s.runeBuf[0], s.runeLen = b, 1
		s.lastRuneBuf, s.lastRuneLen = s.runeBuf, 1
		return gfxRune
	case b >= 0xc2 && b <= 0xf4:
		s.utf8Buf[0] = b
		s.utf8Got = 1
		switch {
		case b <= 0xdf:
			s.utf8Need = 2
		case b <= 0xef:
			s.utf8Need = 3
		default:
			s.utf8Need = 4
		}
		s.runeLen = s.utf8Need
		s.state = gsUTF8
		return gfxNone
	default:
		// 孤立续字节/控制:忽略
		return gfxNone
	}
}

// advance feeds one byte and reports the event it completed. Bytes inside
// a multi-byte UTF-8 character are consumed silently; the gfxRune event
// fires when the last byte completes the rune.
func (s *gfxScanner) advance(b byte) gfxEvt {
	switch s.state {
	case gsScan:
		return s.scanByte(b)
	case gsUTF8:
		if b&0xc0 != 0x80 {
			// 续字节不连续:丢弃已缓冲,把当前字节按普通字节重新处理
			s.utf8Got, s.utf8Need = 0, 0
			s.state = gsScan
			return s.scanByte(b)
		}
		s.utf8Buf[s.utf8Got] = b
		s.utf8Got++
		if s.utf8Got == s.utf8Need {
			r, _ := utf8.DecodeRune(s.utf8Buf[:s.utf8Got])
			s.utf8Got, s.utf8Need = 0, 0
			s.state = gsScan
			s.rn = r
			s.w = widthCond.RuneWidth(r)
			copy(s.runeBuf[:], s.utf8Buf[:s.runeLen])
			copy(s.lastRuneBuf[:], s.runeBuf[:s.runeLen])
			s.lastRuneLen = s.runeLen
			return gfxRune
		}
		return gfxNone
	case gsEsc:
		switch b {
		case '[':
			s.state = gsCSI
			s.priv = false
			s.paramStr = s.paramStr[:0]
			s.csiLen = 2 // ESC + '['
		case ']':
			s.state = gsOSC
			s.startPayload()
		case 'P':
			s.state = gsDCS
			s.startPayload()
		case '_':
			s.state = gsAPC
			s.startPayload()
		case 'c': // RIS: emulator resets itself; the wrapper resets its own bits
			if s.onRIS != nil {
				s.onRIS()
			}
			s.state = gsScan
		default:
			s.state = gsScan
		}
	case gsCSI:
		s.csiLen++
		switch {
		case b == '?':
			s.priv = true
		case b >= 0x30 && b <= 0x39 || b == 0x3b || b == 0x3a:
			s.paramStr = append(s.paramStr, b)
		case b >= 0x20 && b <= 0x3e:
			// 中间字节:仅计数(csiLen);'$' 标记 DECRQM
			if b == '$' {
				s.sawDollar = true
			}
		case b >= 0x40 && b <= 0x7e: // final byte
			params := parseParams(s.paramStr)
			// 查询计数:DSR(n)/DA(c)/DECRQM($p)——用于 DrainAnswers 等待
			if b == 'n' || b == 'c' || (b == 'p' && s.sawDollar) {
				s.queryHits++
			}
			s.sawDollar = false
			switch {
			case b == 'h' || b == 'l':
				if s.onMode != nil {
					s.onMode(s.priv, params, b == 'h')
				}
				if !s.priv {
					for _, p := range params {
						if p == 4 { // IRM — 插入/替换模式
							s.insertMode = b == 'h'
						}
					}
				}
			case b == 's' && !s.priv: // SCP: 保存光标 → DECSC
				s.replacement = []byte{0x1b, '7'}
				s.state = gsScan
				return gfxCSIReplaced
			case b == 'u' && !s.priv: // RCP: 恢复光标 → DECRC
				s.replacement = []byte{0x1b, '8'}
				s.state = gsScan
				return gfxCSIReplaced
			case b == 'b': // REP: 重复前一字符(x/vt 只记录单宽,统一翻译)
				n := 1
				if len(params) > 0 && params[0] > 0 {
					n = params[0]
				}
				if n > repMax {
					n = repMax
				}
				if s.lastRuneLen > 0 {
					s.replacement = bytes.Repeat(s.lastRuneBuf[:s.lastRuneLen], n)
				}
				s.state = gsScan
				return gfxCSIReplaced
			}
			s.state = gsScan
		}
	case gsOSC:
		if b == 0x07 { // BEL 结束 OSC
			s.state = gsScan
			s.payload, s.buf = s.buf, nil
			s.full = false
			s.countOSCQuery()
			return gfxOSCPayload
		}
		if b == 0x1b {
			s.state = gsOSCEnd
			return gfxNone
		}
		s.appendPayload(b)
	case gsOSCEnd:
		if b == '\\' {
			s.state = gsScan
			s.payload, s.buf = s.buf, nil
			s.full = false
			s.countOSCQuery()
			return gfxOSCPayload
		}
		// 其他字节:吞掉,回到收集中(裸 ESC 内容本就非法)
		s.state = gsOSC
	case gsDCS:
		if b == 0x1b {
			s.state = gsDCSEnd
			return gfxNone
		}
		s.appendPayload(b)
	case gsDCSEnd:
		if b == '\\' {
			s.state = gsScan
			s.payload, s.buf = s.buf, nil
			s.full = false
			return gfxDCSPayload
		}
		s.state = gsDCS
	case gsAPC:
		if b == 0x1b {
			s.state = gsAPCEnd
			return gfxNone
		}
		s.appendPayload(b)
	case gsAPCEnd:
		if b == '\\' {
			s.state = gsScan
			s.payload, s.buf = s.buf, nil
			s.full = false
			return gfxAPCPayload
		}
		s.state = gsAPC
	}
	return gfxNone
}

// ---------------------------------------------------------------------------
// Emulator

// Emulator is the capture engine facade: it owns a charmbracelet x/vt
// emulator, an answer queue (terminal query responses the caller must write
// back into the PTY) and the graphics extractor. It is not thread-safe;
// callers serialize access (capture driver: single reader goroutine;
// session mirror: one outputPump goroutine under its mirror lock).
type Emulator struct {
	vt *vt.Emulator

	cellW, cellH int // pixel size of one grid cell (default 9×18)

	// cursorVisible is tracked from CSI ? 25 h/l because x/vt does not
	// expose mode state; it is reported in snapshots.
	cursorVisible bool

	// images holds every picture extracted from the output stream so far.
	images []ImageAsset

	// kittyPending accumulates a kitty transmission across APC chunks.
	kittyPending kittyPending

	// scanner frames the raw stream for graphics payloads and the mode
	// bits the wrapper tracks itself.
	scanner gfxScanner

	// answers holds responses to terminal queries (DSR/DA/DECRQM/OSC
	// colors) until the caller drains them with DrainAnswers and writes
	// them back into the PTY. Queries must be answered or full-screen
	// programs (vim, htop, mc) hang on startup waiting for a reply.
	answersCap int
	answerCh   chan []byte
}

// answerWait bounds how long DrainAnswers waits for the answer pump to
// deliver: the pump is woken by the same io.Pipe write that x/vt's query
// handlers complete, so delivery is one scheduling hop away, typically
// microseconds. The wait only happens when the queue is empty, so drains
// of answer-free chunks cost at most one short delay.
const answerWait = 10 * time.Millisecond

// NewEmulator creates an emulator with a cols×rows screen.
func NewEmulator(cols, rows int) *Emulator {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	e := &Emulator{
		vt:            vt.NewEmulator(cols, rows),
		cursorVisible: true,
		cellW:         9,
		cellH:         18,
		answersCap:    64 * 1024,
		answerCh:      make(chan []byte, 64),
	}
	e.scanner.onMode = e.trackMode
	e.scanner.onRIS = e.onRIS
	// x/vt 把查询应答写入内部 pw 管道;由专用 goroutine 泵进应答队列,
	// DrainAnswers 在任何时刻都能取到已产生的应答。
	go e.answerPump()
	return e
}

// answerPump continuously reads the emulator's answer pipe into the queue.
func (e *Emulator) answerPump() {
	buf := make([]byte, 4096)
	for {
		n, err := e.vt.Read(buf)
		if n > 0 {
			e.answer(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// answer appends a response byte sequence, bounded by answersCap.
func (e *Emulator) answer(b []byte) {
	if len(b) > e.answersCap {
		b = b[:e.answersCap]
	}
	select {
	case e.answerCh <- append([]byte(nil), b...):
	default:
		// 队列满(查询风暴):丢弃最旧?——直接丢弃本条,泵每块都排空。
	}
}

// DrainAnswers returns and clears the accumulated query responses
// (DA/DSR/DECRQM/OSC colors), to be written back into the PTY by the caller.
// When the queue is empty it waits up to answerWait for the answer pump to
// deliver a reply that x/vt just generated (the pump goroutine may not have
// been scheduled yet); chunks with no queries pay at most one short delay.
func (e *Emulator) DrainAnswers() []byte {
	var out []byte
	// 先非阻塞取走所有已送达的应答。
	for {
		select {
		case b := <-e.answerCh:
			out = append(out, b...)
			if len(out) >= e.answersCap {
				return out[:e.answersCap]
			}
			continue
		default:
		}
		break
	}
	// 队列为空且刚才有过查询:给应答泵一个调度机会(泵与 vt 的应答
	// 写入隔着一次 goroutine 调度)。无查询的块不加等待,保持吞吐。
	if e.scanner.queryHits > 0 {
		e.scanner.queryHits = 0
		timer := time.NewTimer(answerWait)
		for {
			select {
			case b := <-e.answerCh:
				out = append(out, b...)
				if len(out) >= e.answersCap {
					timer.Stop()
					return out[:e.answersCap]
				}
				continue
			case <-timer.C:
				timer.Stop()
				return out
			}
		}
	}
	return out
}

// trackMode handles CSI … h/l from the byte stream for the mode bits the
// wrapper tracks; the emulator applies all modes itself.
func (e *Emulator) trackMode(priv bool, params []int, set bool) {
	if !priv {
		return
	}
	for _, p := range params {
		if p == 25 {
			e.cursorVisible = set
		}
	}
}

// onRIS resets the wrapper-tracked state on RIS (the emulator resets its
// own screen and modes internally).
func (e *Emulator) onRIS() {
	e.cursorVisible = true
	e.images = nil
	e.kittyPending = kittyPending{}
	e.scanner.reset()
	e.clearAnswers()
}

// clearAnswers drops any queued query responses (used on RIS/Resize, where
// pending replies would be stale).
func (e *Emulator) clearAnswers() {
	for {
		select {
		case <-e.answerCh:
		default:
			return
		}
	}
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

// Resize rebuilds the screen buffers at a new size. x/vt keeps content from
// the top-left corner (truncated or padded) and clamps cursor, saved cursor
// and scroll region. Graphics state is dropped: a size change invalidates
// pixel placements, and the foreground program redraws anyway on the
// SIGWINCH that follows a real resize.
func (e *Emulator) Resize(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if cols == e.vt.Width() && rows == e.vt.Height() {
		return
	}
	e.vt.Resize(cols, rows)
	e.images = nil
	e.kittyPending = kittyPending{}
	// 尺寸突变使图形放置坐标失效:清暂存与未排空的应答。
	e.scanner.reset()
	e.clearAnswers()
}

// Cols returns the terminal width.
func (e *Emulator) Cols() int { return e.vt.Width() }

// Rows returns the terminal height.
func (e *Emulator) Rows() int { return e.vt.Height() }

// Cursor returns the current cursor position (row, col).
func (e *Emulator) Cursor() (row, col int) {
	p := e.vt.CursorPosition()
	return p.Y, p.X
}

// CursorVisible reports whether the cursor is shown (DECSET/DECRST 25).
func (e *Emulator) CursorVisible() bool { return e.cursorVisible }

// Images returns every graphics-protocol picture extracted so far.
func (e *Emulator) Images() []ImageAsset { return e.images }

// cursorXY returns the current cursor as (col, row) — the x/y order used
// by x/vt and by graphics placements.
func (e *Emulator) cursorXY() (x, y int) {
	p := e.vt.CursorPosition()
	return p.X, p.Y
}

// moveCursorTo moves the emulator cursor to (row, col), 0-based. It is used
// after a graphics placement to advance the cursor by the picture size
// (x/vt has no direct cursor setter, so the wrapper emits CUP).
func (e *Emulator) moveCursorTo(row, col int) {
	row = min(max(0, row), e.vt.Height()-1)
	col = min(max(0, col), e.vt.Width()-1)
	e.vt.WriteString("\x1b[" + strconv.Itoa(row+1) + ";" + strconv.Itoa(col+1) + "H")
}

// Write feeds raw terminal output into the emulator; it implements io.Writer.
//
// The stream is processed in runs: bytes are batched and handed to the
// emulator as contiguous blocks (so grapheme clusters composed across
// several code points stay intact), but flushed at every point where the
// wrapper must interject:
//
//   - graphics-sequence boundaries, so image placements read the cursor at
//     the exact moment the sequence terminated;
//   - complete printable runes, for two x/vt gap compensations:
//     1. a wide rune that does not fit at the right edge gets a CR LF
//     before it (x/vt drops it instead of wrapping, unlike xterm);
//     2. while IRM (insert mode) is set, an ICH inserts the rune's width
//     of blank cells first (x/vt does not implement IRM).
//
// CSI s/u (ANSI Save/Restore Cursor) is translated to DECSC/DECRC
// (ESC 7/8), which x/vt does implement.
func (e *Emulator) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// kitty 占位字符不渲染进网格:剔除后 x/vt 不会看到它。
	p = bytes.ReplaceAll(p, kittyPlaceholderUTF8, nil)

	run := make([]byte, 0, len(p))
	flush := func() {
		if len(run) > 0 {
			_, _ = e.vt.Write(run)
			run = run[:0]
		}
	}
	for _, b := range p {
		ev := e.scanner.advance(b)
		switch ev {
		case gfxRune:
			sc := &e.scanner
			if sc.w > 1 {
				flush()
				x, _ := e.cursorXY()
				if x+sc.w > e.vt.Width() {
					// 行尾放不下的宽字符:先换行(等价于旧引擎的 wrap)。
					e.vt.WriteString("\r\n")
				}
			}
			if sc.insertMode && sc.w > 0 {
				flush()
				e.vt.WriteString("\x1b[" + strconv.Itoa(sc.w) + "@")
			}
			run = append(run, sc.runeBuf[:sc.runeLen]...)
		case gfxCSIReplaced:
			// CSI s/u → ESC 7/8(DECSC/DECRC)。
			cut := e.scanner.csiLen - 1
			if cut > len(run) {
				cut = len(run)
			}
			run = run[:len(run)-cut]
			run = append(run, e.scanner.replacement...)
		case gfxOSCPayload:
			run = append(run, b)
			flush()
			e.handleOSC(e.scanner.payload)
			e.scanner.payload = nil
		case gfxDCSPayload:
			run = append(run, b)
			flush()
			e.handleDCS(e.scanner.payload)
			e.scanner.payload = nil
		case gfxAPCPayload:
			run = append(run, b)
			flush()
			e.handleAPC(e.scanner.payload)
			e.scanner.payload = nil
		default:
			// 多字节 UTF-8 的中间字节暂存在扫描器里,完整 rune 随
			// gfxRune 事件整体送出;其余字节直接进 run。
			if !e.scanner.bufferingRune() {
				run = append(run, b)
			}
		}
	}
	flush()
	return len(p), nil
}

// Screen materializes the active (main or alternate) screen into a Grid.
// Wide characters occupy two columns: the leading cell carries the rune,
// the continuation cell is a zero cell (rendered as nothing).
func (e *Emulator) Screen() *Grid {
	cols, rows := e.vt.Width(), e.vt.Height()
	g := newGrid(rows, cols)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			vc := e.vt.CellAt(x, y)
			if vc == nil {
				continue
			}
			if w := vc.Width; w > 1 && x+1 < cols {
				g.cells[y][x] = vtCellToGrid(vc)
				g.cells[y][x+1] = Cell{} // 宽字符续列:占位格(Rune==0)
				x++
				continue
			}
			g.cells[y][x] = vtCellToGrid(vc)
		}
	}
	return g
}

// vtCellToGrid converts one x/vt cell into the capture cell model.
func vtCellToGrid(vc *uv.Cell) Cell {
	if vc.Width == 0 {
		// 宽字符续列/空单元:占位格。
		return Cell{}
	}
	var cell Cell
	if rs := []rune(vc.Content); len(rs) > 0 {
		cell.Rune = rs[0]
	}
	cell.Fg = colorFromVT(vc.Style.Fg)
	cell.Bg = colorFromVT(vc.Style.Bg)
	att := vc.Style.Attrs
	cell.Bold = att&uv.AttrBold != 0
	cell.Dim = att&uv.AttrFaint != 0
	cell.Italic = att&uv.AttrItalic != 0
	cell.Underline = vc.Style.Underline != uv.UnderlineNone
	cell.Blink = att&uv.AttrBlink != 0 || att&uv.AttrRapidBlink != 0
	cell.Reverse = att&uv.AttrReverse != 0
	cell.Invisible = att&uv.AttrConceal != 0
	cell.Strikethrough = att&uv.AttrStrikethrough != 0
	return cell
}

// colorFromVT converts an x/vt cell color into the capture color model.
func colorFromVT(c color.Color) Color {
	switch v := c.(type) {
	case nil:
		return DefaultColor()
	case ansi.BasicColor:
		return Color{Mode: ColorIndex, Index: uint8(v)}
	case ansi.IndexedColor:
		return Color{Mode: ColorIndex, Index: uint8(v)}
	case color.RGBA:
		return Color{Mode: ColorRGB, RGB: uint32(v.R)<<16 | uint32(v.G)<<8 | uint32(v.B)}
	default:
		// 理论兜底(命名色等):按 RGBA 近似为 24-bit。
		r, g, b, _ := v.RGBA()
		return Color{Mode: ColorRGB, RGB: uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(b>>8)}
	}
}

// ---------------------------------------------------------------------------
// helpers

// parseParams splits a CSI parameter string on ';' and ':' (':'-separated
// extensions like SGR 4:3 degrade gracefully). Empty segments and empty
// sequences yield 0.
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
