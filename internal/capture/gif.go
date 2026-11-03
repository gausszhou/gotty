package capture

// GIF 动画截图（browser 引擎）：
//
// 与静态 --format png 共享同一判定流程，只是把"停止后截一帧"换成"判定循环内
// 按帧率定时截图 + 停止后补一帧收尾"，再用标准库 image/gif 封装成动画。帧来源
// 是真实渲染的页面截图（browser 引擎），因此本文件不含 native 光栅化采样路径。
//
// 量化：headless 截图是真实渲染（字体反锯齿 → 颜色远超 256），每帧用
// PopularQuantize 做流行色量化（保留最常用的颜色、其余就近映射），生成 ≤256
// 色的局部调色板；GIF 编码器会按需写全局/局部色表。
//
// 记忆占用保护：采样时立即量化成 image.Paletted（1 字节/像素）再入缓冲，且
// --gif-optimize 开启时像素不变的帧直接丢弃（时间并入前一帧的 delay），配合
// --gif-max-frames 上限，200 帧的缓冲只有几 MB 级。

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"io"
	"sort"
	"sync"
	"time"
)

// GifOptions 控制 GIF 录制的采样策略（两个引擎共用同一套语义）。
type GifOptions struct {
	// FPS 目标帧率（默认 10）。browser 引擎受截图速度限制，
	// 实际约 2–5 fps，超出时会自然降速。
	FPS int

	// Duration 录制时长上限；0 = 一直录到停止条件触发。
	// 这只是采样上限，不改变停止条件本身。
	Duration time.Duration

	// MaxFrames 帧数上限，超出的采样被丢弃（防体积/内存爆炸）。
	// 0 = 不限。
	MaxFrames int

	// Optimize 丢弃与上一帧像素完全相同的帧（其时间并入前一帧的 delay）。
	// 默认开启。
	Optimize bool
}

// period 返回相邻采样的最小间隔。
func (o GifOptions) period() time.Duration {
	fps := o.FPS
	if fps < 1 {
		fps = 10
	}
	return time.Second / time.Duration(fps)
}

// 编码错误：与完成度相关的错误在 EncodeGIF 时统一报告。
var (
	// ErrGifEmpty 表示没有任何可编码的帧。
	ErrGifEmpty = errors.New("gif: no frames captured")
	// ErrGifStatic 表示内容静止（只有 ≤1 个不同画面），
	// 建议改用 --format png 输出静态快照。
	ErrGifStatic = errors.New("gif: content is static (single frame); use --format png for a still image")
)

// GifFrame 是录到的帧：已经量化为 Paletted 的图像 + 采样时刻
// （delay 由相邻帧时刻差在编码时换算，而非采样时固定）。
type GifFrame struct {
	Image *image.Paletted
	At    time.Time
}

// GifSampler 收集 GIF 帧。trySample 在判定循环/读取循环里按策略采样，
// force 在停止条件触发后补一帧收尾；内部加锁，可从多个 goroutine 调用。
type GifSampler struct {
	opts     GifOptions
	period   time.Duration
	deadline time.Time // zero = 不限时

	mu     sync.Mutex
	frames []GifFrame
	nextAt time.Time // 下一次允许采样的最早时刻
	last   []byte    // 上一帧原始 RGBA Pix（optimize 去重用，量化前对比）
	end    time.Time // 停止时刻（force 时记录；delay 总和的锚点）
}

// NewGifSampler 创建一个采样器；start 是录制起点（duration 上限的基准）。
func NewGifSampler(opts GifOptions, start time.Time) *GifSampler {
	s := &GifSampler{
		opts:   opts,
		period: opts.period(),
	}
	if opts.Duration > 0 {
		s.deadline = start.Add(opts.Duration)
	}
	return s
}

// trySample 按采样策略尝试记录一帧：间隔未到 / 超过时长上限 / 帧数已满时
// 跳过；makeFrame 仅在确实要记录时被调用（避免无谓的截图/渲染）。
// 返回是否录入了新帧。
func (s *GifSampler) TrySample(now time.Time, makeFrame func() image.Image) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.deadline.IsZero() && !now.Before(s.deadline) {
		return false
	}
	if !s.nextAt.IsZero() && now.Before(s.nextAt) {
		return false
	}
	if s.maxFramesReached() {
		return false
	}
	return s.addLocked(now, makeFrame())
}

// force 记录停止条件触发后的收尾帧（不受采样间隔限制，仍受时长/帧数上限
// 约束）；与上一帧像素相同时并入 delay 而不新增帧。now 即停止时刻。
func (s *GifSampler) Force(now time.Time, img image.Image) {
	if img == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.end = now
	if !s.deadline.IsZero() && !now.Before(s.deadline) {
		return
	}
	if s.maxFramesReached() {
		return
	}
	s.addLocked(now, img)
}

func (s *GifSampler) maxFramesReached() bool {
	return s.opts.MaxFrames > 0 && len(s.frames) >= s.opts.MaxFrames
}

// addLocked 量化并存入一帧；optimize 开启时与上一帧原始像素相同则丢弃
// （其时间差自然并入上一帧的 delay）。量化前做去重，避免调色板索引相同、
// 实际颜色不同的帧被误判。
func (s *GifSampler) addLocked(now time.Time, img image.Image) bool {
	if img == nil {
		return false
	}
	rgba := toRGBA(img)
	if s.opts.Optimize && len(s.frames) > 0 && bytes.Equal(s.last, rgba.Pix) {
		s.nextAt = now.Add(s.period)
		return false
	}
	pd := PopularQuantize(rgba, 256)
	s.frames = append(s.frames, GifFrame{Image: pd, At: now})
	s.last = rgba.Pix
	s.nextAt = now.Add(s.period)
	return true
}

// count 返回当前帧数（仅日志/测试用）。
func (s *GifSampler) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

// snapshot 返回录到的帧与停止时刻（force 未调用时以最后一帧时刻为锚点）。
func (s *GifSampler) Snapshot() ([]GifFrame, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]GifFrame, len(s.frames))
	copy(out, s.frames)
	end := s.end
	if end.IsZero() && len(s.frames) > 0 {
		end = s.frames[len(s.frames)-1].At
	}
	return out, end
}

// toRGBA 把任意图像规范化为不透明 RGBA（PNG 解码常得到 NRGBA；统一后
// Pix 字节可直接 bytes.Equal 对比）。
func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, img, b.Min, draw.Src)
	return dst
}

// palettedFrameEqual 按"颜色"比较两帧（调色板 + 索引），而非只比索引序列：
// 两个帧可能调色板不同但索引相同（纯色画面换色），像素颜色其实不同。
func palettedFrameEqual(a, b *image.Paletted) bool {
	if len(a.Pix) != len(b.Pix) || len(a.Palette) == 0 || len(b.Palette) == 0 {
		return false
	}
	ap, bp := a.Palette, b.Palette
	for i, idx := range a.Pix {
		ca := ap[idx]
		if len(bp) <= int(b.Pix[i]) {
			return false
		}
		cb := bp[b.Pix[i]]
		ar, ag, ab, _ := ca.RGBA()
		br, bg, bb, _ := cb.RGBA()
		if ar != br || ag != bg || ab != bb {
			return false
		}
	}
	return true
}

// PopularQuantize 把任意图像量化为 ≤maxColors 色的 Paletted：统计像素直方图，
// 取出现最多的颜色为调色板，其余像素就近映射（RGB 距离）。浏览器渲染的
// 终端画面颜色多为少量纯色（背景黑、前景灰、ANSI 色 + 反锯齿过渡），
// 流行色量化能精确保留主要颜色，比固定 ANSI 色表保真更好。
func PopularQuantize(img image.Image, maxColors int) *image.Paletted {
	if maxColors < 2 {
		maxColors = 2
	}
	if maxColors > 256 {
		maxColors = 256
	}
	b := img.Bounds()

	// 1) 直方图（RGBA/NRGBA 走 Pix 快路径，其余走 At()）
	counts := make(map[uint32]uint32, 1024)
	switch src := img.(type) {
	case *image.RGBA:
		for i := 0; i+3 < len(src.Pix); i += 4 {
			key := uint32(src.Pix[i])<<16 | uint32(src.Pix[i+1])<<8 | uint32(src.Pix[i+2])
			counts[key]++
		}
	case *image.NRGBA:
		for i := 0; i+3 < len(src.Pix); i += 4 {
			key := uint32(src.Pix[i])<<16 | uint32(src.Pix[i+1])<<8 | uint32(src.Pix[i+2])
			counts[key]++
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				key := uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(bl>>8)
				counts[key]++
			}
		}
	}

	// 2) 按出现次数取前 maxColors 种颜色
	type entry struct {
		key   uint32
		count uint32
	}
	entries := make([]entry, 0, len(counts))
	for k, c := range counts {
		entries = append(entries, entry{k, c})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
	if len(entries) > maxColors {
		entries = entries[:maxColors]
	}

	pal := make(color.Palette, 0, len(entries))
	idxOf := make(map[uint32]uint8, len(entries))
	for i, e := range entries {
		pal = append(pal, color.RGBA{
			R: uint8(e.key >> 16), G: uint8(e.key >> 8), B: uint8(e.key), A: 0xff,
		})
		idxOf[e.key] = uint8(i)
	}
	// GIF 色表最少 2 项；只有 1 种颜色时补一个同色项。
	if len(pal) < 2 {
		dup := color.RGBA{R: uint8(entries[0].key >> 16), G: uint8(entries[0].key >> 8), B: uint8(entries[0].key), A: 0xff}
		pal = append(pal, dup)
	}

	// 3) 逐像素就近映射
	pd := image.NewPaletted(b, pal)
	cache := make(map[uint32]uint8, len(idxOf)*2)
	switch src := img.(type) {
	case *image.RGBA:
		fillPalettedRGBA(pd, src.Pix, b, pal, idxOf, cache)
	case *image.NRGBA:
		fillPalettedNRGBA(pd, src.Pix, b, pal, idxOf, cache)
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, _ := img.At(x, y).RGBA()
				key := uint32(r>>8)<<16 | uint32(g>>8)<<8 | uint32(bl>>8)
				pd.SetColorIndex(x, y, nearestIndex(key, pal, idxOf, cache))
			}
		}
	}
	return pd
}

func nearestIndex(key uint32, pal color.Palette, idxOf map[uint32]uint8, cache map[uint32]uint8) uint8 {
	if i, ok := idxOf[key]; ok {
		return i
	}
	if i, ok := cache[key]; ok {
		return i
	}
	// 就近映射：到调色板各色的平方距离最小者
	best, bestD := uint8(0), int64(1<<62)
	r0, g0, b0 := int(key>>16), int(key>>8&0xff), int(key&0xff)
	for i, c := range pal {
		cc := c.(color.RGBA)
		dr, dg, db := r0-int(cc.R), g0-int(cc.G), b0-int(cc.B)
		d := int64(dr*dr + dg*dg + db*db)
		if d < bestD {
			best, bestD = uint8(i), d
		}
	}
	cache[key] = best
	return best
}

func fillPalettedRGBA(pd *image.Paletted, pix []byte, b image.Rectangle, pal color.Palette, idxOf, cache map[uint32]uint8) {
	stride := pd.Stride
	off := 0
	for y := 0; y < b.Dy(); y++ {
		row := y * stride
		for x := 0; x < b.Dx(); x++ {
			i := off + x*4
			key := uint32(pix[i])<<16 | uint32(pix[i+1])<<8 | uint32(pix[i+2])
			pd.Pix[row+x] = nearestIndex(key, pal, idxOf, cache)
		}
		off += 4 * b.Dx()
	}
}

func fillPalettedNRGBA(pd *image.Paletted, pix []byte, b image.Rectangle, pal color.Palette, idxOf, cache map[uint32]uint8) {
	stride := pd.Stride
	off := 0
	for y := 0; y < b.Dy(); y++ {
		row := y * stride
		for x := 0; x < b.Dx(); x++ {
			i := off + x*4
			key := uint32(pix[i])<<16 | uint32(pix[i+1])<<8 | uint32(pix[i+2])
			pd.Pix[row+x] = nearestIndex(key, pal, idxOf, cache)
		}
		off += 4 * b.Dx()
	}
}

// EncodeGIF 把帧序列写成 GIF 动画：delay 由相邻帧采样时刻差换算（毫秒→
// 百分之一秒，四舍五入，最小 1）——第 i 帧的 delay 覆盖到第 i+1 帧的采样
// 时刻，末帧延迟到 stopAt（这样播放总时长 = stopAt−首帧时刻 = 录制时长，
// 且 --gif-optimize 合并相同帧后总和不变：被丢弃帧的时间并入相邻 delay）。
// LoopCount=0（无限循环）。
//
// optimize 开启时把仍然相邻的相同帧（例如 force 收尾帧与采样帧重合）合并。
// 只有 1 个不同画面时返回 ErrGifStatic。
func EncodeGIF(w io.Writer, frames []GifFrame, stopAt time.Time, optimize bool) error {
	if len(frames) == 0 {
		return ErrGifEmpty
	}

	kept := frames
	if optimize {
		out := frames[:0]
		for _, f := range frames {
			if n := len(out); n > 0 && palettedFrameEqual(out[n-1].Image, f.Image) {
				continue // 与前帧颜色相同：丢弃，时间并入 delay
			}
			out = append(out, f)
		}
		kept = out
	}
	if len(kept) <= 1 {
		return ErrGifStatic
	}

	anim := &gif.GIF{
		Image:     make([]*image.Paletted, len(kept)),
		Delay:     make([]int, len(kept)),
		LoopCount: 0, // 无限循环
	}
	for i, f := range kept {
		anim.Image[i] = f.Image
		var d time.Duration
		if i == len(kept)-1 {
			d = stopAt.Sub(f.At) // 末帧覆盖到停止时刻
		} else {
			d = kept[i+1].At.Sub(f.At)
		}
		if d < 0 {
			d = 0
		}
		cs := (d.Milliseconds() + 5) / 10 // 毫秒 → 百分之一秒（四舍五入）
		if cs < 1 {
			cs = 1
		}
		anim.Delay[i] = int(cs)
	}

	if err := gif.EncodeAll(w, anim); err != nil {
		return fmt.Errorf("gif encode: %w", err)
	}
	return nil
}
