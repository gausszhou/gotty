package capture

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"

	"github.com/mattn/go-sixel"
	_ "golang.org/x/image/webp" // 让 image.Decode 支持 WebP
)

// ImageProtocol identifies the graphics protocol a captured image came from.
type ImageProtocol string

const (
	// ProtoKitty marks images transmitted via the kitty graphics protocol.
	ProtoKitty ImageProtocol = "kitty"
	// ProtoSixel marks sixel-decoded images.
	ProtoSixel ImageProtocol = "sixel"
	// ProtoITerm2 marks iTerm2 inline images (OSC 1337).
	ProtoITerm2 ImageProtocol = "iterm2"

	// kittyPlaceholder is the kitty graphics unicode placeholder cell.
	kittyPlaceholder = rune(0x10EEEE)
)

// ImageAsset is one image extracted from the output stream: decoded pixels
// plus its placement on the screen grid.
type ImageAsset struct {
	Protocol ImageProtocol
	Row      int // placement origin (grid cell)
	Col      int
	CellCols int // occupied columns (grid cells)
	CellRows int // occupied rows (grid cells)
	Width    int // source/display pixel width
	Height   int // source/display pixel height
	MIME     string
	DataURI  string

	img image.Image // decoded pixels, used by the PNG renderer
}

// kittyPending accumulates one kitty transmission across chunks (m=1…0).
// Only the first chunk carries the full parameter set.
type kittyPending struct {
	keys map[string]string
	data []byte
}

// ---------------------------------------------------------------------------
// emulator-side extraction: the writer feeds raw bytes to both the VT state
// machine and these protocol recognizers.

// handleAPC processes a complete APC payload (ESC _ … ESC \) — the kitty
// graphics protocol transport.
func (e *Emulator) handleAPC(payload []byte) {
	if len(payload) == 0 || payload[0] != 'G' {
		return // non-graphics APC: ignore
	}
	body := payload[1:]
	head, data, _ := strings.Cut(string(body), ";")
	keys := parseKittyKeys(head)

	switch keys["a"] {
	case "q", "r", "D", "c", "f": // query/respond/delete/combine/frame: ignore
		return
	case "p": // display previously transmitted image by id: not supported yet
		return
	}

	if keys["m"] == "1" {
		// 分片续传:首块参数被记住,数据累积,直到 m=0 才渲染。
		if len(e.kittyPending.data) == 0 && e.kittyPending.keys == nil {
			e.kittyPending.keys = keys
		}
		e.kittyPending.data = append(e.kittyPending.data, data...)
		return
	}

	// 单块传输,或分片的最后一块(m=0)
	if len(e.kittyPending.data) > 0 {
		e.kittyPending.data = append(e.kittyPending.data, data...)
		firstKeys := e.kittyPending.keys // 参数以首块为准
		dataBytes := e.kittyPending.data
		e.kittyPending = kittyPending{}
		e.finishKitty(firstKeys, dataBytes)
		return
	}
	e.finishKitty(keys, []byte(data))
	e.kittyPending = kittyPending{}
}

// finishKitty decodes a complete kitty transmission and places it at the
// current cursor position. The accumulated payload is base64; f=24/32 raw
// pixel modes additionally honor the s/v dimensions from the control keys.
func (e *Emulator) finishKitty(keys map[string]string, payload []byte) {
	if len(payload) == 0 {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(string(payload))
	if err != nil {
		return
	}
	if keys["o"] == "z" {
		zr, zerr := zlib.NewReader(bytes.NewReader(raw))
		if zerr != nil {
			return
		}
		defer zr.Close()
		raw, err = io.ReadAll(zr)
		if err != nil {
			return
		}
	}

	var img image.Image
	var mime string
	switch keys["f"] {
	case "24", "32":
		w := atoi(keys["s"])
		h := atoi(keys["v"])
		if w <= 0 || h <= 0 {
			return
		}
		img, mime = rawToImage(raw, w, h, keys["f"] == "32")
	default: // 100 (PNG), 101 (JPEG), 102 (WebP), anything image.Decode knows
		decoded, derr := decodeImage(raw)
		if derr != nil {
			return
		}
		img, mime = decoded, mimeDetect(raw)
	}
	if img == nil {
		return
	}
	e.addImage(ProtoKitty, img, mime, raw, keys)
}

// handleOSC inspects a completed OSC string (… BEL or … ESC \). Only the
// iTerm2 inline-image form (OSC 1337;File=…;inline=1:<base64>) is handled;
// other OSC payloads (titles, clipboard, color queries) are ignored.
func (e *Emulator) handleOSC(osc []byte) {
	const prefix = "1337;File="
	if !bytes.HasPrefix(osc, []byte(prefix)) {
		return
	}
	rest := osc[len(prefix):]
	head, b64, ok := bytes.Cut(rest, []byte(":"))
	if !ok || !bytes.Contains(head, []byte("inline=1")) {
		return // only inline=1 images render on-screen
	}
	data, err := base64.StdEncoding.DecodeString(string(b64))
	if err != nil {
		return
	}
	img, err := decodeImage(data)
	if err != nil {
		return
	}
	// iTerm2: image height = one cell row, width keeps the pixel aspect.
	mime := mimeDetect(data)
	cellCols := 1
	if img.Bounds().Dy() > 0 {
		cellCols = max(1, int(float64(img.Bounds().Dx())*float64(e.cellH)/
			float64(img.Bounds().Dy())/float64(e.cellW)+0.5))
	}
	a := ImageAsset{
		Protocol: ProtoITerm2,
		Row:      e.row,
		Col:      e.col,
		CellCols: cellCols,
		CellRows: 1,
		Width:    img.Bounds().Dx(),
		Height:   img.Bounds().Dy(),
		MIME:     mime,
		DataURI:  dataURI(mime, data),
		img:      img,
	}
	e.images = append(e.images, a)
}

// handleDCS inspects a completed DCS payload (ESC P … ESC \). Sixel data
// starts with 'q'; everything else is ignored.
func (e *Emulator) handleDCS(dcs []byte) {
	// sixel introducer 'q' 前可以有参数(如 ESC P 0;0;8 q …),数据在 q 之后
	qi := bytes.IndexByte(dcs, 'q')
	if qi < 0 {
		return
	}
	var img image.Image
	// go-sixel 的 Decoder 需要完整 DCS 序列(ESC P … q … ST);emulator 收集
	// 的内容不含定界的 ESC \,这里重建为 ESC P q + 数据 + ST。
	feed := make([]byte, 0, len(dcs)-qi+5)
	feed = append(feed, 0x1b, 'P', 'q')
	feed = append(feed, dcs[qi+1:]...)
	feed = append(feed, 0x1b, '\\')
	dec := sixel.NewDecoder(bytes.NewReader(feed))
	if err := dec.Decode(&img); err != nil {
		return
	}
	if img == nil {
		return
	}
	b, err := encodePNG(img)
	if err != nil {
		return
	}
	a := ImageAsset{
		Protocol: ProtoSixel,
		Row:      e.row,
		Col:      e.col,
		CellCols: min(e.cols-e.col, max(1, intDivCeil(img.Bounds().Dx(), e.cellW))),
		CellRows: min(e.rows-e.row, max(1, intDivCeil(img.Bounds().Dy(), e.cellH))),
		Width:    img.Bounds().Dx(),
		Height:   img.Bounds().Dy(),
		MIME:     "image/png",
		DataURI:  dataURI("image/png", b),
		img:      img,
	}
	e.images = append(e.images, a)
}

// addImage finishes a kitty transmission: placement at the cursor, optional
// c/r cell-rectangle, cursor movement per the protocol spec.
func (e *Emulator) addImage(proto ImageProtocol, img image.Image, mime string, raw []byte, keys map[string]string) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	// c/r specify the display rectangle in cells; otherwise scale by pixels.
	cellCols, cellRows := atoi(keys["c"]), atoi(keys["r"])
	if cellCols <= 0 && cellRows <= 0 {
		cellCols = max(1, intDivCeil(w, e.cellW))
		cellRows = max(1, intDivCeil(h, e.cellH))
	} else if cellCols <= 0 {
		cellCols = max(1, intDivCeil(w*cellRows, h))
	} else if cellRows <= 0 {
		cellRows = max(1, intDivCeil(h*cellCols, w))
	}
	cellCols = min(e.cols-e.col, cellCols)
	cellRows = min(e.rows-e.row, cellRows)
	if cellCols <= 0 || cellRows <= 0 {
		return
	}

	a := ImageAsset{
		Protocol: proto,
		Row:      e.row,
		Col:      e.col,
		CellCols: cellCols,
		CellRows: cellRows,
		Width:    w,
		Height:   h,
		MIME:     mime,
		DataURI:  dataURI(mime, raw),
		img:      img,
	}
	e.images = append(e.images, a)

	// Per the spec the cursor moves right by the placement width and down by
	// its height (wrapping at the right edge).
	e.col += cellCols
	if e.col >= e.cols {
		e.row = min(e.rows-1, e.row+1)
		e.col = 0
	}
	e.row = min(e.rows-1, e.row+max(0, cellRows-1))
}

// ---------------------------------------------------------------------------
// helpers

func parseKittyKeys(head string) map[string]string {
	keys := make(map[string]string)
	for _, part := range strings.Split(head, ",") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			keys[k] = v
		}
	}
	return keys
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0
		}
	}
	return n
}

func intDivCeil(a, b int) int {
	if b <= 0 {
		return 1
	}
	return (a + b - 1) / b
}

// rawToImage builds an image from f=24/f=32 raw pixel data.
func rawToImage(data []byte, w, h int, alpha bool) (image.Image, string) {
	bpp := 3
	if alpha {
		bpp = 4
	}
	need := w * h * bpp
	if w <= 0 || h <= 0 || len(data) < need {
		return nil, ""
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := (y*w + x) * bpp
			if alpha {
				img.SetNRGBA(x, y, color.NRGBA{
					R: data[off], G: data[off+1], B: data[off+2], A: data[off+3],
				})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{
					R: data[off], G: data[off+1], B: data[off+2], A: 0xff,
				})
			}
		}
	}
	return img, "image/png"
}

func decodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func dataURI(mime string, data []byte) string {
	if mime == "" {
		mime = "application/octet-stream"
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data))
}

// mimeDetect sniffs PNG/JPEG/GIF/WebP from magic bytes.
func mimeDetect(b []byte) string {
	switch {
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		return "image/png"
	case len(b) >= 3 && bytes.Equal(b[:3], []byte{0xff, 0xd8, 0xff}):
		return "image/jpeg"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && string(b[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}
