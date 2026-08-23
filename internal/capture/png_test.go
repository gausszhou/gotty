package capture

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// decodePNG helper for assertions on rendered output.
func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode rendered png: %v", err)
	}
	return img
}

func pixelAt(t *testing.T, img image.Image, x, y int) color.RGBA {
	t.Helper()
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func TestPNGCanvasSize(t *testing.T) {
	e := NewEmulator(5, 3)
	write(t, e, "hi")
	data, err := PNG(e.Screen(), nil, 9, 18)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	if img.Bounds().Dx() != 5*9 || img.Bounds().Dy() != 3*18 {
		t.Errorf("canvas = %dx%d, want %dx%d", img.Bounds().Dx(), img.Bounds().Dy(), 45, 54)
	}
}

func TestPNGBackgroundColor(t *testing.T) {
	e := NewEmulator(4, 1)
	write(t, e, "\x1b[41mX") // 红底
	data, err := PNG(e.Screen(), nil, 9, 18)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	// 红色单元格内,远离字形中心采样:应为 #cd0000
	c := pixelAt(t, img, 4, 4)
	red := color.RGBA{R: 0xcd, A: 0xff}
	if c.R != red.R || c.G != 0 || c.B != 0 {
		t.Errorf("bg pixel = %+v, want %+v", c, red)
	}
	// 默认背景单元格:纯黑
	c = pixelAt(t, img, 9+4, 4)
	if c.R != 0 || c.G != 0 || c.B != 0 {
		t.Errorf("default bg pixel = %+v, want black", c)
	}
}

func TestPNGGlyphPixels(t *testing.T) {
	e := NewEmulator(4, 1)
	write(t, e, "X")
	data, err := PNG(e.Screen(), nil, 9, 18)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	// 字形中心附近应有非背景(非黑)像素
	found := false
	for y := 2; y < 16; y++ {
		for x := 1; x < 8; x++ {
			if c := pixelAt(t, img, x, y); c.R > 0x40 || c.G > 0x40 || c.B > 0x40 {
				found = true
			}
		}
	}
	if !found {
		t.Error("no glyph pixel rendered for 'X'")
	}
}

func TestPNGImageComposite(t *testing.T) {
	e := NewEmulator(10, 3)
	raw := testPNGBytes(t, 18, 18, color.RGBA{R: 255, G: 0, B: 255, A: 255})
	b64 := base64.StdEncoding.EncodeToString(raw)
	write(t, e, kittyAPC("a=T,f=100,m=0", b64))

	data, err := PNG(e.Screen(), e.Images(), 9, 18)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	// 图片占 (0,0) 1x1 格(9x18px),中心应为洋红
	c := pixelAt(t, img, 4, 9)
	want := color.RGBA{R: 255, B: 255, A: 255}
	if c.R != want.R || c.G != 0 || c.B != want.B {
		t.Errorf("composited image pixel = %+v, want %+v", c, want)
	}
}

func TestPNGEmptyScreen(t *testing.T) {
	e := NewEmulator(2, 2)
	data, err := PNG(e.Screen(), nil, 9, 18)
	if err != nil {
		t.Fatal(err)
	}
	img := decodePNG(t, data)
	if c := pixelAt(t, img, 0, 0); c.R != 0 || c.G != 0 || c.B != 0 {
		t.Errorf("empty screen pixel = %+v, want black", c)
	}
}
