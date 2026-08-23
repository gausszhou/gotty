package capture

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/mattn/go-sixel"
)

// testPNGBytes builds a PNG of the given size and color.
func testPNGBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func kittyAPC(params, data string) string {
	return "\x1b_G" + params + ";" + data + "\x1b\\"
}

func TestKittySingleFrame(t *testing.T) {
	e := NewEmulator(20, 10)
	raw := testPNGBytes(t, 2, 2, color.RGBA{R: 255, A: 255})
	write(t, e, kittyAPC("a=T,f=100,m=0", base64.StdEncoding.EncodeToString(raw)))

	images := e.Images()
	if len(images) != 1 {
		t.Fatalf("images = %d, want 1", len(images))
	}
	a := images[0]
	if a.Protocol != ProtoKitty {
		t.Errorf("protocol = %s, want kitty", a.Protocol)
	}
	if a.Row != 0 || a.Col != 0 {
		t.Errorf("placement = (%d,%d), want (0,0)", a.Row, a.Col)
	}
	if a.Width != 2 || a.Height != 2 {
		t.Errorf("size = %dx%d, want 2x2", a.Width, a.Height)
	}
	if a.CellCols != 1 || a.CellRows != 1 { // 2px / 9px cell → 1 cell
		t.Errorf("cells = %dx%d, want 1x1", a.CellCols, a.CellRows)
	}
	if !strings.HasPrefix(a.DataURI, "data:image/png;base64,") {
		t.Errorf("data URI = %q", a.DataURI)
	}
	if a.img == nil {
		t.Error("decoded image missing")
	}
}

func TestKittyChunked(t *testing.T) {
	e := NewEmulator(20, 10)
	raw := testPNGBytes(t, 2, 2, color.RGBA{B: 255, A: 255})
	b64 := base64.StdEncoding.EncodeToString(raw)
	// 第一块:完整参数 + m=1;后续块:仅 m=0
	split := len(b64) / 2
	write(t, e, kittyAPC("a=T,f=100,m=1", b64[:split]))
	write(t, e, kittyAPC("m=0", b64[split:]))

	if len(e.Images()) != 1 {
		t.Fatalf("chunked kitty images = %d, want 1", len(e.Images()))
	}
	if e.Images()[0].Row != 0 || e.Images()[0].Col != 0 {
		t.Errorf("placement = (%d,%d), want (0,0)", e.Images()[0].Row, e.Images()[0].Col)
	}
}

func TestKittyQueryActionsIgnored(t *testing.T) {
	e := NewEmulator(20, 10)
	write(t, e, kittyAPC("a=q,i=0", ""))
	write(t, e, kittyAPC("a=r,i=0", ""))
	write(t, e, kittyAPC("a=D,i=0", ""))
	if len(e.Images()) != 0 {
		t.Errorf("query/delete actions must not create images: %d", len(e.Images()))
	}
}

func TestKittyRawRGB(t *testing.T) {
	e := NewEmulator(20, 10)
	// f=24: 2x1 像素 RGB:红 + 绿
	data := []byte{255, 0, 0, 0, 255, 0}
	write(t, e, kittyAPC("a=T,f=24,s=2,v=1,m=0", base64.StdEncoding.EncodeToString(data)))
	if len(e.Images()) != 1 {
		t.Fatalf("raw kitty images = %d, want 1", len(e.Images()))
	}
	a := e.Images()[0]
	if a.Width != 2 || a.Height != 1 {
		t.Errorf("raw size = %dx%d, want 2x1", a.Width, a.Height)
	}
	c := a.img.At(0, 0)
	r, g, _, _ := c.RGBA()
	if r>>8 != 255 || g>>8 != 0 {
		t.Errorf("pixel0 = (%d,%d), want red", r>>8, g>>8)
	}
}

func TestKittyPlacementAfterText(t *testing.T) {
	e := NewEmulator(20, 10)
	write(t, e, "ab")                                          // 光标到 (0,2)
	raw := testPNGBytes(t, 18, 18, color.RGBA{R: 255, A: 255}) // 1x1 cell (9x18 out)
	write(t, e, kittyAPC("a=T,f=100,m=0", base64.StdEncoding.EncodeToString(raw)))
	a := e.Images()[0]
	if a.Row != 0 || a.Col != 2 {
		t.Errorf("placement = (%d,%d), want (0,2)", a.Row, a.Col)
	}
	// 协议规定放置后光标右移图片宽度、必要时下行
	if r, c := e.Cursor(); r != 0 || c != 4 {
		t.Errorf("cursor after placement = (%d,%d), want (0,4)", r, c)
	}
}

func TestITerm2Inline(t *testing.T) {
	e := NewEmulator(20, 10)
	raw := testPNGBytes(t, 18, 18, color.RGBA{R: 255, G: 255, A: 255})
	b64 := base64.StdEncoding.EncodeToString(raw)
	osc := "\x1b]1337;File=name=px.png;inline=1;size=18:" + b64 + "\x07"
	write(t, e, osc)

	if len(e.Images()) != 1 {
		t.Fatalf("iterm2 images = %d, want 1", len(e.Images()))
	}
	a := e.Images()[0]
	if a.Protocol != ProtoITerm2 {
		t.Errorf("protocol = %s, want iterm2", a.Protocol)
	}
	if a.CellRows != 1 {
		t.Errorf("cell_rows = %d, want 1 (one line tall)", a.CellRows)
	}
}

func TestOSC1337NonImageIgnored(t *testing.T) {
	e := NewEmulator(20, 10)
	write(t, e, "\x1b]1337;File=name=x.txt;size=4;inline=0:"+base64.StdEncoding.EncodeToString([]byte("nope"))+"\x07")
	write(t, e, "\x1b]0;window title\x07")
	if len(e.Images()) != 0 {
		t.Errorf("non-inline OSC must not create images: %d", len(e.Images()))
	}
}

func TestSixel(t *testing.T) {
	e := NewEmulator(20, 10)

	// 用 go-sixel 的编码器生成一个 4x4 红色 sixel 数据,再喂回仿真器
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var enc bytes.Buffer
	if err := sixel.NewEncoder(&enc).Encode(src); err != nil {
		t.Fatal(err)
	}
	out := enc.Bytes()
	t.Logf("sixel bytes: %d", len(out))

	// encoder 输出自带 ESC P<params>q 头;仿真器收到的完整 DCS 内容是
	// 「params q data」,提取 q 之后的数据(含 q)重新构造 DCS。
	qi := bytes.IndexByte(out, 'q')
	if qi < 0 {
		t.Fatalf("encoder output missing 'q': %q", out)
	}
	write(t, e, "\x1bP"+string(out[qi:]))
	if len(e.Images()) != 1 {
		t.Fatalf("sixel images = %d, want 1", len(e.Images()))
	}
	a := e.Images()[0]
	if a.Protocol != ProtoSixel {
		t.Errorf("protocol = %s, want sixel", a.Protocol)
	}
	if a.Width != 4 || a.Height != 4 {
		t.Errorf("size = %dx%d, want 4x4", a.Width, a.Height)
	}
}

func TestDCSNonSixelIgnored(t *testing.T) {
	e := NewEmulator(20, 10)
	write(t, e, "\x1bP1;2;3\x1b\\ok")
	if len(e.Images()) != 0 {
		t.Errorf("non-sixel DCS created images: %d", len(e.Images()))
	}
	if Text(e.Screen()) != "ok" {
		t.Errorf("text after DCS = %q", Text(e.Screen()))
	}
}

func TestBrokenBase64Ignored(t *testing.T) {
	e := NewEmulator(20, 10)
	write(t, e, kittyAPC("a=T,f=100,m=0", "!!!not-base64!!!"))
	write(t, e, "\x1b]1337;File=name=x;inline=1:%%%not-base64\x07")
	if len(e.Images()) != 0 {
		t.Errorf("broken payloads must be ignored, got %d images", len(e.Images()))
	}
}

func TestKittyPlaceholderSkipped(t *testing.T) {
	e := NewEmulator(20, 10)
	write(t, e, "a\U0010EEEeb") // kitty unicode placeholder 不渲染进文本
	if Text(e.Screen()) != "ab" {
		t.Errorf("text = %q, want ab", Text(e.Screen()))
	}
}
