package capture

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"strings"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Document is the `--format json` rendering result.
type Document struct {
	Version    int         `json:"version"`
	Engine     string      `json:"engine"`
	Command    string      `json:"command"`
	Args       []string    `json:"args"`
	Cols       int         `json:"cols"`
	Rows       int         `json:"rows"`
	CellWidth  int         `json:"cell_width,omitempty"`
	CellHeight int         `json:"cell_height,omitempty"`
	ExitCode   *int        `json:"exit_code,omitempty"`
	TimedOut   bool        `json:"timed_out"`
	DurationMS int64       `json:"duration_ms"`
	Text       string      `json:"text"`
	Cells      []CellJSON  `json:"cells,omitempty"`
	Images     []ImageJSON `json:"images,omitempty"`
	Cursor     CursorJSON  `json:"cursor"`
	CapturedAt string      `json:"captured_at"`
}

// CursorJSON describes the cursor position at capture time.
type CursorJSON struct {
	Row     int  `json:"row"`
	Col     int  `json:"col"`
	Visible bool `json:"visible"`
}

// CellJSON is the per-cell styled form used by --format json.
type CellJSON struct {
	R  int     `json:"r"`
	C  int     `json:"c"`
	Ch string  `json:"ch"`
	Fg *string `json:"fg,omitempty"`
	Bg *string `json:"bg,omitempty"`

	Bold          bool `json:"bold,omitempty"`
	Dim           bool `json:"dim,omitempty"`
	Italic        bool `json:"italic,omitempty"`
	Underline     bool `json:"underline,omitempty"`
	Blink         bool `json:"blink,omitempty"`
	Reverse       bool `json:"reverse,omitempty"`
	Invisible     bool `json:"invisible,omitempty"`
	Strikethrough bool `json:"strikethrough,omitempty"`
}

// ImageJSON is the wire form of one captured image (--format json).
type ImageJSON struct {
	ID       int    `json:"id"`
	Protocol string `json:"protocol"`
	Row      int    `json:"row"`
	Col      int    `json:"col"`
	CellCols int    `json:"cell_cols"`
	CellRows int    `json:"cell_rows"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	MIME     string `json:"mime,omitempty"`
	DataURI  string `json:"data_uri"`
}

// NewDocument assembles the JSON rendering result for a captured screen.
func NewDocument(emu *Emulator, command string, args []string, exitCode *int,
	timedOut bool, duration time.Duration, reason StopReason,
	images []ImageAsset, cellW, cellH int, withCells bool) Document {
	row, col := emu.Cursor()
	doc := Document{
		Version:    1,
		Engine:     "native",
		Command:    command,
		Args:       append([]string{}, args...),
		Cols:       emu.Cols(),
		Rows:       emu.Rows(),
		CellWidth:  cellW,
		CellHeight: cellH,
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		DurationMS: duration.Milliseconds(),
		Text:       Text(emu.Screen()),
		Cursor:     CursorJSON{Row: row, Col: col, Visible: emu.CursorVisible()},
		CapturedAt: time.Now().Format(time.RFC3339),
	}
	if withCells {
		doc.Cells = CellsJSON(emu.Screen())
	}
	doc.Images = ImagesJSON(images)
	_ = reason
	return doc
}

// ImagesJSON converts image assets into their wire form.
func ImagesJSON(images []ImageAsset) []ImageJSON {
	out := make([]ImageJSON, 0, len(images))
	for i, a := range images {
		out = append(out, ImageJSON{
			ID:       i,
			Protocol: string(a.Protocol),
			Row:      a.Row,
			Col:      a.Col,
			CellCols: a.CellCols,
			CellRows: a.CellRows,
			Width:    a.Width,
			Height:   a.Height,
			MIME:     a.MIME,
			DataURI:  a.DataURI,
		})
	}
	return out
}

// Text renders the screen grid as plain text: per-line trailing spaces are
// trimmed, so are fully empty trailing lines; wide-character placeholders
// are skipped, keeping the printed width faithful to the terminal.
func Text(g *Grid) string {
	rows := make([]string, 0, g.Rows())
	for r := 0; r < g.Rows(); r++ {
		var sb strings.Builder
		for c := 0; c < g.Cols(); c++ {
			if rn := g.Cell(r, c).Rune; rn != 0 {
				sb.WriteRune(rn)
			}
		}
		rows = append(rows, strings.TrimRight(sb.String(), " "))
	}
	end := len(rows)
	for end > 0 && rows[end-1] == "" {
		end--
	}
	return strings.Join(rows[:end], "\n")
}

// HTML renders the screen grid as styled HTML: one <span> per styled cell,
// plain text for default cells, inside a <pre> terminal box. Trailing empty
// lines are dropped so the output stays readable.
func HTML(g *Grid) string {
	last := g.Rows() - 1
	for last >= 0 && !rowHasContent(g, last) {
		last--
	}

	var sb strings.Builder
	sb.WriteString("<pre class=\"gotty-capture\">\n")
	for r := 0; r <= last; r++ {
		for c := 0; c < g.Cols(); c++ {
			cell := g.Cell(r, c)
			ch := " "
			if cell.Rune != 0 {
				ch = string(cell.Rune)
			}
			if sty := cellInlineStyle(cell); sty != "" {
				sb.WriteString("<span style=\"")
				sb.WriteString(sty)
				sb.WriteString("\">")
				sb.WriteString(html.EscapeString(ch))
				sb.WriteString("</span>")
			} else {
				sb.WriteString(html.EscapeString(ch))
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString("</pre>")
	return sb.String()
}

// rowHasContent reports whether the row carries any cell (or styling).
func rowHasContent(g *Grid, r int) bool {
	for c := 0; c < g.Cols(); c++ {
		if cell := g.Cell(r, c); cell.Rune != 0 || cellHasStyle(cell) {
			return true
		}
	}
	return false
}

// CellsJSON returns the styled cell list, skipping blank default cells.
func CellsJSON(g *Grid) []CellJSON {
	var out []CellJSON
	for r := 0; r < g.Rows(); r++ {
		for c := 0; c < g.Cols(); c++ {
			cell := g.Cell(r, c)
			if cell.Rune == 0 {
				continue
			}
			if cell.Rune == ' ' && !cellHasStyle(cell) {
				continue
			}
			cj := CellJSON{R: r, C: c, Ch: string(cell.Rune)}
			if cell.Reverse {
				cj.Reverse = true
			}
			fg, bg, _ := renderedColors(cell)
			if s := cssColor(fg); s != "" {
				cj.Fg = &s
			}
			if s := cssColor(bg); s != "" {
				cj.Bg = &s
			}
			cj.Bold = cell.Bold
			cj.Dim = cell.Dim
			cj.Italic = cell.Italic
			cj.Underline = cell.Underline
			cj.Blink = cell.Blink
			cj.Invisible = cell.Invisible
			cj.Strikethrough = cell.Strikethrough
			out = append(out, cj)
		}
	}
	return out
}

// cellHasStyle reports whether the cell carries any non-default styling.
func cellHasStyle(c Cell) bool {
	return c.Fg.Mode != ColorDefault || c.Bg.Mode != ColorDefault ||
		c.Bold || c.Dim || c.Italic || c.Underline || c.Blink ||
		c.Reverse || c.Invisible || c.Strikethrough
}

// renderedColors resolves the reverse-video swap (fg↔bg) for rendering.
func renderedColors(c Cell) (fg, bg Color, reversed bool) {
	if c.Reverse {
		return c.Bg, c.Fg, true
	}
	return c.Fg, c.Bg, false
}

// cellInlineStyle builds the CSS text for one cell.
func cellInlineStyle(c Cell) string {
	fg, bg, _ := renderedColors(c)
	var parts []string
	if s := cssColor(fg); s != "" {
		parts = append(parts, "color:"+s)
	}
	if s := cssColor(bg); s != "" {
		parts = append(parts, "background-color:"+s)
	}
	if c.Bold {
		parts = append(parts, "font-weight:bold")
	}
	if c.Dim {
		parts = append(parts, "opacity:0.6")
	}
	if c.Italic {
		parts = append(parts, "font-style:italic")
	}
	if c.Underline {
		parts = append(parts, "text-decoration:underline")
	}
	if c.Blink {
		parts = append(parts, "text-decoration:blink")
	}
	if c.Invisible {
		parts = append(parts, "visibility:hidden")
	}
	if c.Strikethrough {
		parts = append(parts, "text-decoration:line-through")
	}
	return strings.Join(parts, ";")
}

// cssColor converts a cell color to a CSS color string ("" for default).
func cssColor(c Color) string {
	switch c.Mode {
	case ColorIndex:
		r, g, b := ansiRGBValues(int(c.Index))
		return fmt.Sprintf("#%02x%02x%02x", r, g, b)
	case ColorRGB:
		return fmt.Sprintf("#%06x", c.RGB)
	default:
		return ""
	}
}

// colorFromColor converts a cell color to an RGBA value (ok=false for default).
func colorFromColor(c Color) (color.RGBA, bool) {
	switch c.Mode {
	case ColorIndex:
		r, g, b := ansiRGBValues(int(c.Index))
		return color.RGBA{R: r, G: g, B: b, A: 0xff}, true
	case ColorRGB:
		return color.RGBA{R: uint8(c.RGB >> 16), G: uint8(c.RGB >> 8), B: uint8(c.RGB), A: 0xff}, true
	default:
		return color.RGBA{}, false
	}
}

// ansiRGBValues maps a 0-255 palette index to RGB: 0-15 the classic ANSI
// colors, 16-231 the 6×6×6 color cube, 232-255 the grayscale ramp.
func ansiRGBValues(idx int) (uint8, uint8, uint8) {
	if idx < 16 {
		table := [16][3]uint8{
			{0x00, 0x00, 0x00}, {0xcd, 0x00, 0x00}, {0x00, 0xcd, 0x00}, {0xcd, 0xcd, 0x00},
			{0x00, 0x00, 0xee}, {0xcd, 0x00, 0xcd}, {0x00, 0xcd, 0xcd}, {0xe5, 0xe5, 0xe5},
			{0x7f, 0x7f, 0x7f}, {0xff, 0x00, 0x00}, {0x00, 0xff, 0x00}, {0xff, 0xff, 0x00},
			{0x5c, 0x5c, 0xff}, {0xff, 0x00, 0xff}, {0x00, 0xff, 0xff}, {0xff, 0xff, 0xff},
		}
		c := table[idx]
		return c[0], c[1], c[2]
	}
	if idx <= 231 {
		vals := [6]uint8{0, 95, 135, 175, 215, 255}
		i := idx - 16
		return vals[i/36], vals[(i%36)/6], vals[i%6]
	}
	g := uint8(8 + 10*(idx-232))
	return g, g, g
}

// ---- PNG rasterization ---------------------------------------------------

var (
	defaultFG = color.RGBA{R: 0xcc, G: 0xcc, B: 0xcc, A: 0xff}
	defaultBG = color.RGBA{R: 0, G: 0, B: 0, A: 0xff}
)

// PNG rasterizes the screen grid into PNG bytes: one cellW×cellH block per
// grid cell, glyphs drawn with an embedded monospace font (Latin coverage;
// CJK/emoji render as tofu boxes), then graphics-protocol images are
// composited at their placements. Pixel-perfect text needs the browser
// engine (M3); this renderer is a faithful-enough bitmap snapshot.
func PNG(g *Grid, images []ImageAsset, cellW, cellH int) ([]byte, error) {
	face, err := monoFace(cellH)
	if err != nil {
		return nil, err
	}
	metrics := face.Metrics()
	ascent := metrics.Ascent.Ceil()
	lineHeight := metrics.Ascent.Ceil() + metrics.Descent.Ceil()

	canvas := image.NewRGBA(image.Rect(0, 0, g.Cols()*cellW, g.Rows()*cellH))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(defaultBG), image.Point{}, draw.Src)

	for r := 0; r < g.Rows(); r++ {
		for c := 0; c < g.Cols(); c++ {
			cell := g.Cell(r, c)
			x0, y0 := c*cellW, r*cellH

			// 背景覆盖(任何格子,含空格)
			if bgc, ok := colorFromColor(cell.Bg); ok {
				draw.Draw(canvas, image.Rect(x0, y0, x0+cellW, y0+cellH),
					image.NewUniform(bgc), image.Point{}, draw.Src)
			}

			if cell.Invisible || cell.Rune == 0 || cell.Rune == ' ' {
				continue
			}

			fg, _, _ := renderedColors(cell)
			fgc, ok := colorFromColor(fg)
			if !ok {
				fgc = defaultFG
			}
			if cell.Dim {
				bgc := defaultBG
				if b, ok := colorFromColor(cell.Bg); ok {
					bgc = b
				}
				fgc = blendColor(fgc, bgc, 0.55)
			}

			// 字形宽度(含宽字符跨两格)
			rw := 1
			if widthCond.RuneWidth(cell.Rune) == 2 {
				rw = 2
			}
			if _, ok := face.GlyphAdvance(cell.Rune); !ok {
				drawTofu(canvas, x0, y0, cellW*rw, cellH, fgc)
				continue
			}

			// 垂直居中基线,水平按字形宽度居中
			adv := font.MeasureString(face, string(cell.Rune)).Ceil()
			dot := fixed.P(x0+(cellW*rw-adv)/2, y0+(cellH-lineHeight)/2+ascent)
			d := &font.Drawer{Dst: canvas, Src: image.NewUniform(fgc), Face: face, Dot: dot}
			d.DrawString(string(cell.Rune))
			if cell.Bold {
				d.Dot.X += fixed.I(1)
				d.DrawString(string(cell.Rune)) // 粗略加粗:向右补 1px
			}
			if cell.Underline {
				drawLine(canvas, x0, y0+cellH-2, x0+cellW*rw-1, y0+cellH-2, fgc)
			}
			if cell.Strikethrough {
				drawLine(canvas, x0, y0+lineHeight/2+1, x0+cellW*rw-1, y0+lineHeight/2+1, fgc)
			}
		}
	}

	// 图形协议图片合成(覆盖文本与底色)
	for _, a := range images {
		if a.img == nil {
			continue
		}
		dst := image.Rect(
			a.Col*cellW, a.Row*cellH,
			(a.Col+a.CellCols)*cellW, (a.Row+a.CellRows)*cellH,
		)
		draw.ApproxBiLinear.Scale(canvas, dst, a.img, a.img.Bounds(), draw.Over, nil)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// monoFace loads the embedded Go Mono font at a size derived from cellH.
func monoFace(cellH int) (font.Face, error) {
	f, err := opentype.Parse(gomono.TTF)
	if err != nil {
		return nil, err
	}
	size := float64(cellH - 4)
	if size < 6 {
		size = 6
	}
	return opentype.NewFace(f, &opentype.FaceOptions{
		Size:    size,
		DPI:     72,
		Hinting: font.HintingFull,
	})
}

func blendColor(fg, bg color.RGBA, alpha float64) color.RGBA {
	w := uint8(alpha * 255)
	return color.RGBA{
		R: uint8((int(fg.R)*int(w) + int(bg.R)*(255-int(w))) / 255),
		G: uint8((int(fg.G)*int(w) + int(bg.G)*(255-int(w))) / 255),
		B: uint8((int(fg.B)*int(w) + int(bg.B)*(255-int(w))) / 255),
		A: 0xff,
	}
}

// drawTofu draws a hollow rectangle — the stand-in for a missing glyph.
func drawTofu(img *image.RGBA, x0, y0, w, h int, fgc color.RGBA) {
	for x := x0; x < x0+w; x++ {
		img.Set(x, y0, fgc)
		img.Set(x, y0+h-1, fgc)
	}
	for y := y0; y < y0+h; y++ {
		img.Set(x0, y, fgc)
		img.Set(x0+w-1, y, fgc)
	}
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	if y0 != y1 {
		return
	}
	for x := x0; x <= x1; x++ {
		img.Set(x, y0, c)
	}
}

// ansi16CSS maps a 16-color palette index to the xterm default CSS color.
func ansi16CSS(idx int) string {
	r, g, b := ansiRGBValues(idx)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// ansi256CSS maps a 0-255 palette index to a CSS color.
func ansi256CSS(idx int) string {
	r, g, b := ansiRGBValues(idx)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}
