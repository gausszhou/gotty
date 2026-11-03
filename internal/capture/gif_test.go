package capture

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"testing"
	"time"
)

// testFrameRGBA 构造一个 w×h 的纯色 RGBA 帧。
func testFrameRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// ---- GifSampler 采样策略 ------------------------------------------------

func TestGifSamplerInterval(t *testing.T) {
	s := NewGifSampler(GifOptions{FPS: 10}, time.Now()) // period 100ms
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	img := testFrameRGBA(2, 2, color.RGBA{R: 255, A: 255})

	if !s.TrySample(t0, func() image.Image { return img }) {
		t.Fatal("first sample should be recorded")
	}
	if s.TrySample(t0.Add(50*time.Millisecond), func() image.Image { return img }) {
		t.Error("sample inside the frame interval must be skipped")
	}
	if !s.TrySample(t0.Add(100*time.Millisecond), func() image.Image { return img }) {
		t.Error("sample at the next interval must be recorded")
	}
	if s.TrySample(t0.Add(150*time.Millisecond), func() image.Image { return img }) {
		t.Error("sample inside the frame interval must be skipped")
	}
	if got := s.Count(); got != 2 {
		t.Errorf("frames = %d, want 2", got)
	}
}

func TestGifSamplerMaxFrames(t *testing.T) {
	s := NewGifSampler(GifOptions{FPS: 100, MaxFrames: 2}, time.Now())
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var hues []color.RGBA
	for i := 0; i < 4; i++ {
		hues = append(hues, color.RGBA{R: uint8(i * 30), A: 255})
	}
	for i, c := range hues {
		img := testFrameRGBA(2, 2, color.RGBA{R: c.R, A: 255})
		s.TrySample(t0.Add(time.Duration(i)*20*time.Millisecond), func() image.Image { return img })
	}
	if got := s.Count(); got != 2 {
		t.Errorf("frames = %d, want cap 2", got)
	}
}

func TestGifSamplerDuration(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := NewGifSampler(GifOptions{FPS: 100, Duration: 150 * time.Millisecond}, start)
	var shades []color.RGBA
	for i := 0; i < 5; i++ {
		shades = append(shades, color.RGBA{G: uint8(i * 30), A: 255})
	}
	for i, c := range shades {
		img := testFrameRGBA(2, 2, color.RGBA{G: c.G, A: 255})
		s.TrySample(start.Add(time.Duration(i)*50*time.Millisecond), func() image.Image { return img })
	}
	// start, +50ms 在期限内;+100ms 在期限内;+150ms/+200ms 超出
	if got := s.Count(); got != 3 {
		t.Errorf("frames = %d, want 3 (duration cap)", got)
	}
	// force 收尾帧在期限外也跳过
	s.Force(start.Add(200*time.Millisecond), testFrameRGBA(2, 2, color.RGBA{}))
	if got := s.Count(); got != 3 {
		t.Errorf("frames after force = %d, want 3 (deadline honored)", got)
	}
}

func TestGifSamplerOptimizeDedup(t *testing.T) {
	s := NewGifSampler(GifOptions{FPS: 100, Optimize: true}, time.Now())
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := testFrameRGBA(2, 2, color.RGBA{R: 200, A: 255})
	b := testFrameRGBA(2, 2, color.RGBA{R: 210, A: 255})
	s.TrySample(t0, func() image.Image { return a })
	s.TrySample(t0.Add(10*time.Millisecond), func() image.Image { return a }) // 相同 → 丢弃
	s.TrySample(t0.Add(20*time.Millisecond), func() image.Image { return b })
	if got := s.Count(); got != 2 {
		t.Fatalf("frames = %d, want 2 (identical frame deduped)", got)
	}
	frames, end := s.Snapshot()
	if len(frames) != 2 {
		t.Fatalf("snapshot frames = %d, want 2", len(frames))
	}
	if frames[1].At.Sub(frames[0].At) != 20*time.Millisecond {
		t.Errorf("second frame At should keep the deduped frame's time gap")
	}
	// force 与末帧相同 → 不新增帧,但 end 锚点记录停止时刻
	s.Force(t0.Add(30*time.Millisecond), b)
	if got := s.Count(); got != 2 {
		t.Errorf("frames after identical force = %d, want 2", got)
	}
	if end.IsZero() {
		t.Error("end anchor should be set")
	}
}

func TestGifSamplerForceExtendsTimer(t *testing.T) {
	// force 不受采样间隔约束:即使间隔未到也记录(收尾帧)
	s := NewGifSampler(GifOptions{FPS: 1, Optimize: false}, time.Now())
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.TrySample(t0, func() image.Image { return testFrameRGBA(2, 2, color.RGBA{R: 1, A: 255}) })
	s.Force(t0.Add(time.Millisecond), testFrameRGBA(2, 2, color.RGBA{R: 2, A: 255}))
	if got := s.Count(); got != 2 {
		t.Errorf("frames = %d, want 2 (force bypasses interval)", got)
	}
}

// ---- PopularQuantize -----------------------------------------------------

func TestPopularQuantizePalette(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 20, 10))
	// 背景黑,中央红块,右下白块
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			c := color.RGBA{}
			if x >= 8 && x < 12 && y >= 3 && y < 7 {
				c = color.RGBA{R: 255, A: 255}
			}
			if x >= 16 && y >= 8 {
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	pd := PopularQuantize(img, 256)
	if pd.Bounds() != img.Bounds() {
		t.Fatalf("bounds = %v, want %v", pd.Bounds(), img.Bounds())
	}
	if len(pd.Palette) > 256 {
		t.Fatalf("palette size = %d, want <= 256", len(pd.Palette))
	}
	// 黑/红/白都被精确保留(流行色直接入调色板)
	have := map[color.RGBA]bool{}
	for _, c := range pd.Palette {
		have[c.(color.RGBA)] = true
	}
	for _, want := range []color.RGBA{
		{A: 255}, {R: 255, A: 255}, {R: 255, G: 255, B: 255, A: 255},
	} {
		if !have[want] {
			t.Errorf("palette missing exact color %+v", want)
		}
	}
	// 红块中心像素映射回红
	if c := pd.Palette[pd.ColorIndexAt(10, 5)].(color.RGBA); c.R != 255 || c.G != 0 || c.B != 0 {
		t.Errorf("red pixel mapped to %+v", c)
	}
}

func TestPopularQuantizeSmallPalette(t *testing.T) {
	// 单色图:直方图只有 1 种颜色,调色板应补到 ≥2 项且不报错
	img := testFrameRGBA(4, 4, color.RGBA{A: 255})
	pd := PopularQuantize(img, 256)
	if len(pd.Palette) < 2 {
		t.Errorf("palette size = %d, want >= 2", len(pd.Palette))
	}
	if c := pd.Palette[pd.ColorIndexAt(0, 0)].(color.RGBA); c != (color.RGBA{A: 255}) {
		t.Errorf("black pixel mapped to %+v", c)
	}
}

// ---- EncodeGIF ------------------------------------------------------------

func TestEncodeGIFDelaysAndLoop(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	frames := []GifFrame{
		{Image: PopularQuantize(testFrameRGBA(4, 4, color.RGBA{R: 255, A: 255}), 256), At: t0},
		{Image: PopularQuantize(testFrameRGBA(4, 4, color.RGBA{G: 255, A: 255}), 256), At: t0.Add(100 * time.Millisecond)},
		{Image: PopularQuantize(testFrameRGBA(4, 4, color.RGBA{B: 255, A: 255}), 256), At: t0.Add(200 * time.Millisecond)},
	}
	var buf bytes.Buffer
	if err := EncodeGIF(&buf, frames, t0.Add(300*time.Millisecond), false); err != nil {
		t.Fatal(err)
	}
	dec, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Image) != 3 {
		t.Fatalf("decoded frames = %d, want 3", len(dec.Image))
	}
	wantDelay := []int{10, 10, 10}
	for i, d := range dec.Delay {
		if d != wantDelay[i] {
			t.Errorf("frame %d delay = %d cs, want %d", i, d, wantDelay[i])
		}
	}
	// 播放总时长 = delay 总和 = 录制时长(停止时刻 − 首帧时刻)
	if sum := dec.Delay[0] + dec.Delay[1] + dec.Delay[2]; sum != 30 {
		t.Errorf("delay sum = %d cs, want 30 (300ms)", sum)
	}
	if dec.LoopCount != 0 {
		t.Errorf("loop count = %d, want 0 (infinite)", dec.LoopCount)
	}
	if dec.Config.Width != 4 || dec.Config.Height != 4 {
		t.Errorf("config = %dx%d, want 4x4", dec.Config.Width, dec.Config.Height)
	}
	for i, im := range dec.Image {
		if len(im.Palette) > 256 {
			t.Errorf("decoded frame %d palette = %d, want <= 256", i, len(im.Palette))
		}
	}
}

func TestEncodeGIFOptimizeMerges(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := PopularQuantize(testFrameRGBA(4, 4, color.RGBA{R: 200, A: 255}), 256)
	b := PopularQuantize(testFrameRGBA(4, 4, color.RGBA{G: 200, A: 255}), 256)
	c := PopularQuantize(testFrameRGBA(4, 4, color.RGBA{B: 200, A: 255}), 256)
	frames := []GifFrame{
		{Image: a, At: t0},
		{Image: a, At: t0.Add(50 * time.Millisecond)}, // 与上一帧相同
		{Image: b, At: t0.Add(100 * time.Millisecond)},
		{Image: c, At: t0.Add(300 * time.Millisecond)},
	}
	stop := t0.Add(400 * time.Millisecond)

	plain := &bytes.Buffer{}
	if err := EncodeGIF(plain, frames, stop, false); err != nil {
		t.Fatal(err)
	}
	opt := &bytes.Buffer{}
	if err := EncodeGIF(opt, frames, stop, true); err != nil {
		t.Fatal(err)
	}

	plainDec, _ := gif.DecodeAll(bytes.NewReader(plain.Bytes()))
	optDec, _ := gif.DecodeAll(bytes.NewReader(opt.Bytes()))
	if len(plainDec.Image) != 4 {
		t.Errorf("plain frames = %d, want 4", len(plainDec.Image))
	}
	if len(optDec.Image) != 3 {
		t.Errorf("optimized frames = %d, want 3 (identical pair merged)", len(optDec.Image))
	}
	sum := func(d []int) int {
		n := 0
		for _, v := range d {
			n += v
		}
		return n
	}
	// 播放时长 = delay 总和,优化前后不变(≈录制时长 400ms)
	if p, o := sum(plainDec.Delay), sum(optDec.Delay); p != o {
		t.Errorf("delay sum differs: plain=%dcs optimized=%dcs", p, o)
	}
}

func TestEncodeGIFErrors(t *testing.T) {
	stop := time.Now()
	if err := EncodeGIF(&bytes.Buffer{}, nil, stop, true); err != ErrGifEmpty {
		t.Errorf("empty frames error = %v, want ErrGifEmpty", err)
	}
	single := []GifFrame{{Image: PopularQuantize(testFrameRGBA(2, 2, color.RGBA{A: 255}), 256), At: stop}}
	if err := EncodeGIF(&bytes.Buffer{}, single, stop, true); err != ErrGifStatic {
		t.Errorf("single frame error = %v, want ErrGifStatic", err)
	}
	dup := []GifFrame{
		{Image: PopularQuantize(testFrameRGBA(2, 2, color.RGBA{R: 9, A: 255}), 256), At: stop.Add(-time.Second)},
		{Image: PopularQuantize(testFrameRGBA(2, 2, color.RGBA{R: 9, A: 255}), 256), At: stop},
	}
	if err := EncodeGIF(&bytes.Buffer{}, dup, stop, true); err != ErrGifStatic {
		t.Errorf("two identical frames error = %v, want ErrGifStatic", err)
	}
	// 两帧不同 → 正常编码
	dif := []GifFrame{
		{Image: PopularQuantize(testFrameRGBA(2, 2, color.RGBA{R: 9, A: 255}), 256), At: stop.Add(-time.Second)},
		{Image: PopularQuantize(testFrameRGBA(2, 2, color.RGBA{B: 9, A: 255}), 256), At: stop},
	}
	if err := EncodeGIF(&bytes.Buffer{}, dif, stop, true); err != nil {
		t.Errorf("two different frames should encode, got %v", err)
	}
}

func TestEncodeGIFDecodeRoundtrip(t *testing.T) {
	// 端到端:时间序列(3 组不同画面)→ 解码读回:帧数、尺寸、首末帧像素不同
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var frames []GifFrame
	for i := 0; i < 3; i++ {
		img := testFrameRGBA(6, 4, color.RGBA{R: uint8(80 + i*50), A: 255})
		frames = append(frames, GifFrame{
			Image: PopularQuantize(img, 256),
			At:    t0.Add(time.Duration(i) * 100 * time.Millisecond),
		})
	}
	var buf bytes.Buffer
	if err := EncodeGIF(&buf, frames, t0.Add(300*time.Millisecond), true); err != nil {
		t.Fatal(err)
	}
	dec, err := gif.DecodeAll(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(dec.Image) < 2 {
		t.Fatalf("frames = %d, want >= 2", len(dec.Image))
	}
	for _, im := range dec.Image {
		if im.Bounds().Dx() != 6 || im.Bounds().Dy() != 4 {
			t.Errorf("frame size = %v, want 6x4", im.Bounds())
		}
		if len(im.Palette) > 256 {
			t.Errorf("palette = %d, want <= 256", len(im.Palette))
		}
	}
	// 解码帧可能各有局部调色板:比较实际颜色(调色板还原),而非索引序列
	first := decodeFrameRGBA(t, dec.Image[0])
	last := decodeFrameRGBA(t, dec.Image[len(dec.Image)-1])
	if bytes.Equal(first.Pix, last.Pix) {
		t.Error("first and last frames should differ")
	}
}

// decodeFrameRGBA 用帧自己的调色板还原为 RGBA,便于跨调色板比较像素。
func decodeFrameRGBA(t *testing.T, pm *image.Paletted) *image.RGBA {
	t.Helper()
	dst := image.NewRGBA(pm.Bounds())
	draw.Draw(dst, pm.Bounds(), pm, image.Point{}, draw.Src)
	return dst
}
