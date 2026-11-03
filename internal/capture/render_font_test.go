package capture

// 本文件覆盖 0005 §4 的字体验收:
//   - `--font` 指向的 TTF/OTF 能渲染出非 tofu 字形(CJK 字符);
//   - `--font` 指向不存在/不可解析的文件时报错,不静默回退到内嵌 Go Mono;
//   - `--font-size` 生效(字形像素尺寸随字号变化,画布仍由单元格尺寸决定)。
//
// 测试保持 hermetic:不下载任何字体,只探测系统已有的字体文件;若 CI 上
// 没有可用字体则 Skip —— 缺字体是环境限制,不应把 CI 判红。

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font/opentype"
)

const (
	// ftFontCellW/ftFontCellH 是本文件统一的单元格尺寸。CJK 是双宽字符,
	// 渲染器会占 2 格(36px),比仓库默认的 9px 宽更能装下完整字形。
	ftFontCellW = 18
	ftFontCellH = 18
	// ftFontSizedCellH 给字号用例留出足够高度:24pt 字形的行高约 30px,
	// 用 18px 高的格子会被裁切,无法稳定比较字形尺寸。
	ftFontSizedCellH = 40
)

// ftFontCJKHints / ftFontLatinHints 是按文件名筛选系统字体的线索。系统
// 字体目录通常有几百个文件,先按名字挑候选可以避免每次测试都解析全部字体;
// 如果线索全部落空,ftFindFont 还会退回全量扫描,以免漏掉命名不常见的字体。
var (
	ftFontCJKHints = []string{
		"cjk", "droid", "fallback", "symbola", "wqy", "zenhei", "sarasa",
		"sourcehan", "source-han", "uming", "ukai", "unifont", "noto sans sc",
		"noto sans tc", "simsun", "simhei", "msyh", "msjh", "pingfang",
	}
	ftFontLatinHints = []string{
		"dejavu", "liberation", "freesans", "freemono", "freeserif",
		"notosans", "notoserif", "nimbus", "arial", "helvetica", "courier",
		"consola", "menlo", "roboto", "ubuntu",
	}
)

// ftFontProbeDirs 返回常见的系统字体目录。目录不存在时 WalkDir 会返回
// 错误,调用方直接忽略,因此同一份用例可以在 Linux/macOS/Windows 上跑。
func ftFontProbeDirs() []string {
	dirs := []string{
		"/usr/share/fonts",
		"/usr/local/share/fonts",
		"/System/Library/Fonts",
		"/Library/Fonts",
	}
	if root := os.Getenv("WINDIR"); root != "" {
		dirs = append(dirs, filepath.Join(root, "Fonts"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "fonts"))
		dirs = append(dirs, filepath.Join(home, ".fonts"))
	}
	return dirs
}

// ftFontNameHasAny 判断字体文件名是否命中任一检测线索(不区分大小写)。
func ftFontNameHasAny(path string, hints []string) bool {
	name := strings.ToLower(filepath.Base(path))
	for _, h := range hints {
		if strings.Contains(name, h) {
			return true
		}
	}
	return false
}

// ftFontCandidates 收集系统字体目录下的候选文件。hints 非空时只返回文件名
// 命中的字体;hints 为空表示全量扫描(耗时兜底)。
//
// 只收 .ttf/.otf:生产代码 loadFace 用 opentype.Parse 解析单个 SFNT,
// 字体集合(.ttc/.otc,如 NotoSansCJK-Regular.ttc)不在支持范围内,把它们
// 当候选只会白读几十 MB 数据。
func ftFontCandidates(t *testing.T, hints []string) []string {
	t.Helper()
	var out []string
	for _, root := range ftFontProbeDirs() {
		// 回调里返回 nil 吞掉目录不可读的错误:那是"该目录没有字体",
		// 而不是测试失败;真正的"没有可用字体"由 ftFindFont 返回 "" 表达。
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ttf", ".otf":
			default:
				return nil
			}
			if len(hints) > 0 && !ftFontNameHasAny(path, hints) {
				return nil
			}
			out = append(out, path)
			return nil
		})
	}
	return out
}

// ftFontFileHasRune 判断字体文件能否被生产链路解析且覆盖 r。先走和
// loadFace 相同的 opentype.Parse,保证被测文件不会在 --font 上解析失败。
func ftFontFileHasRune(t *testing.T, path string, r rune) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	f, err := opentype.Parse(data)
	if err != nil {
		return false
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 16, DPI: 72})
	if err != nil {
		return false
	}
	_, ok := face.GlyphAdvance(r)
	return ok
}

// ftFindFont 在系统字体里找一个覆盖 want 的 TTF/OTF,找不到返回 ""。
// 调用方据此 t.Skip,这样没有字体的 CI 依然全绿(0005 §4 允许跳过)。
func ftFindFont(t *testing.T, want rune, hints []string) string {
	t.Helper()
	for _, path := range ftFontCandidates(t, hints) {
		if ftFontFileHasRune(t, path, want) {
			return path
		}
	}
	// 线索可能漏掉自定义命名(例如发行版打包的字体),全量兜底一次。
	for _, path := range ftFontCandidates(t, nil) {
		if ftFontFileHasRune(t, path, want) {
			return path
		}
	}
	return ""
}

// ftFontRender 光栅化并解码 PNG:验收关注的是"像素里到底画了什么",
// 而不是 PNG 编码结果,所以断言统一落在解码后的图像上。
func ftFontRender(t *testing.T, e *Emulator, cellW, cellH int, opts ...RenderOption) image.Image {
	t.Helper()
	raw, err := PNG(e.Screen(), nil, cellW, cellH, opts...)
	if err != nil {
		t.Fatalf("PNG(cell %dx%d): %v", cellW, cellH, err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode rendered PNG: %v", err)
	}
	return img
}

// ftFontPixel 取一个像素的 8 位 RGBA。
func ftFontPixel(img image.Image, x, y int) color.RGBA {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

// ftFontInk 判断像素是否是字形墨迹:明显亮于默认黑色背景即可,包含反锯齿边缘。
func ftFontInk(c color.RGBA) bool {
	return c.R > 0x40 || c.G > 0x40 || c.B > 0x40
}

// ftFontInkStats 统计 rect 内的墨迹像素数与墨迹包围盒。包围盒为空表示
// 该区域什么都没画。
func ftFontInkStats(img image.Image, rect image.Rectangle) (count int, box image.Rectangle) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			if !ftFontInk(ftFontPixel(img, x, y)) {
				continue
			}
			count++
			if box.Empty() {
				box = image.Rect(x, y, x+1, y+1)
			} else {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return count, box
}

// ftFontLooksLikeTofu 判断 rect 的四条边是否全部着墨 —— 这正是 drawTofu
// 对缺字画出的空心框。真实字形由字体轮廓决定,不可能恰好铺满整圈边框。
func ftFontLooksLikeTofu(img image.Image, rect image.Rectangle) bool {
	if rect.Empty() {
		return false
	}
	for x := rect.Min.X; x < rect.Max.X; x++ {
		if !ftFontInk(ftFontPixel(img, x, rect.Min.Y)) ||
			!ftFontInk(ftFontPixel(img, x, rect.Max.Y-1)) {
			return false
		}
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		if !ftFontInk(ftFontPixel(img, rect.Min.X, y)) ||
			!ftFontInk(ftFontPixel(img, rect.Max.X-1, y)) {
			return false
		}
	}
	return true
}

// ftFontSamePixels 逐像素比较两张图像。用像素而非 PNG 字节比较,避免
// 编码器细节影响结论。
func ftFontSamePixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if ftFontPixel(a, x, y) != ftFontPixel(b, x, y) {
				return false
			}
		}
	}
	return true
}

// TestRenderFontCJKNotTofu 是 §4 的核心正例:用系统里的 CJK 字体渲染「中」,
// 断言输出既不是内嵌字体的 tofu 空心框,也不与内嵌字体渲染逐像素相同
// (后者能抓住"传了 --font 却被静默忽略"的实现回归)。
func TestRenderFontCJKNotTofu(t *testing.T) {
	fontPath := ftFindFont(t, '中', ftFontCJKHints)
	if fontPath == "" {
		t.Skip("系统字体中没有含 CJK 的 TTF/OTF(需要 Noto Sans CJK / Droid Sans Fallback 等),跳过非 tofu 用例")
	}
	t.Logf("使用 CJK 字体: %s", fontPath)

	e := NewEmulator(2, 1)
	write(t, e, "中")
	area := image.Rect(0, 0, 2*ftFontCellW, ftFontCellH) // 宽字符占两格

	// 基线:内嵌 Go Mono 只覆盖 Latin,「中」必然走 drawTofu。先确认检测器
	// 能识别它,再用同一检测器否定外部字体的输出;若将来内嵌字体扩了 CJK,
	// 这里只记录日志,不让环境假设变成失败。
	embedded := ftFontRender(t, e, ftFontCellW, ftFontCellH)
	if ftFontLooksLikeTofu(embedded, area) {
		if _, box := ftFontInkStats(embedded, area); box.Dy() != ftFontCellH {
			t.Errorf("tofu 基线异常:墨迹高度 %d,期望整格 %d", box.Dy(), ftFontCellH)
		}
	} else {
		t.Logf("内嵌字体对「中」没有画出空心框(可能已扩充 CJK 覆盖),跳过 tofu 形状对照")
	}

	rendered := ftFontRender(t, e, ftFontCellW, ftFontCellH, WithFont(fontPath, 0))
	if ftFontSamePixels(embedded, rendered) {
		t.Fatalf("--font %s 的渲染结果与内嵌字体逐像素相同,--font 未生效", fontPath)
	}
	if ftFontLooksLikeTofu(rendered, area) {
		t.Errorf("--font %s 仍把「中」渲染成 tofu 空心框", fontPath)
	}

	ink, box := ftFontInkStats(rendered, area)
	if ink == 0 || box.Empty() {
		t.Fatalf("--font %s 没有渲染出任何字形像素", fontPath)
	}
	// 真实 CJK 字形在格子内部也有笔画;tofu 只有一圈边框。
	innerInk, _ := ftFontInkStats(rendered,
		image.Rect(box.Min.X+1, box.Min.Y+1, box.Max.X-1, box.Max.Y-1))
	if innerInk == 0 {
		t.Errorf("--font %s 的字形只有边框像素,疑似 tofu 兜底", fontPath)
	}
}

// TestRenderFontSizeScalesGlyph 是 §4 的字号验收:同一字体、同一字符,
// 24pt 的字形必须比 8pt 更高、更多墨迹;同时画布尺寸只能由单元格尺寸
// 决定,不能被字号改变。
func TestRenderFontSizeScalesGlyph(t *testing.T) {
	fontPath := ftFindFont(t, 'A', ftFontLatinHints)
	if fontPath == "" {
		t.Skip("系统字体中没有可解析的 TTF/OTF,跳过字号用例")
	}
	t.Logf("使用字体: %s", fontPath)

	e := NewEmulator(1, 1)
	write(t, e, "A")
	drawArea := image.Rect(0, 0, ftFontCellW, ftFontSizedCellH)

	small := ftFontRender(t, e, ftFontCellW, ftFontSizedCellH, WithFont(fontPath, 8))
	large := ftFontRender(t, e, ftFontCellW, ftFontSizedCellH, WithFont(fontPath, 24))

	smallInk, smallBox := ftFontInkStats(small, drawArea)
	largeInk, largeBox := ftFontInkStats(large, drawArea)
	if smallBox.Empty() {
		t.Fatalf("8pt 没有渲染出字形像素")
	}
	if largeBox.Empty() {
		t.Fatalf("24pt 没有渲染出字形像素")
	}
	if largeBox.Dy() <= smallBox.Dy() {
		t.Errorf("font-size 未放大字形高度: 8pt=%dpx, 24pt=%dpx", smallBox.Dy(), largeBox.Dy())
	}
	if largeInk <= smallInk {
		t.Errorf("font-size 未增加字形墨迹: 8pt=%dpx, 24pt=%dpx", smallInk, largeInk)
	}
	// 画布只由 cols/rows × cell 尺寸决定,字号不应参与。
	if small.Bounds() != large.Bounds() {
		t.Errorf("font-size 改变了画布尺寸: 8pt=%v, 24pt=%v", small.Bounds(), large.Bounds())
	}

	// FontSize=0 的约定是"从 cell 高度推导":用 face metrics 核对,比像素
	// 更能直接表达"字号来源",也不受具体字形轮廓影响。
	autoFace, err := loadFace(ftFontSizedCellH, RenderOptions{FontPath: fontPath})
	if err != nil {
		t.Fatalf("loadFace(auto): %v", err)
	}
	smallFace, err := loadFace(ftFontSizedCellH, RenderOptions{FontPath: fontPath, FontSize: 8})
	if err != nil {
		t.Fatalf("loadFace(8pt): %v", err)
	}
	if autoH, smallH := autoFace.Metrics().Height.Ceil(), smallFace.Metrics().Height.Ceil(); autoH <= smallH {
		t.Errorf("FontSize=0 未从 cell 高度推导字号: auto 行高 %d, 8pt 行高 %d", autoH, smallH)
	}
}

// TestFontMissingFileErrors 验证 --font 指向不存在的文件时明确报错。
// 静默回退内嵌字体会把"路径打错"伪装成"CJK 渲染正常",正是 §4 要求排除的。
func TestFontMissingFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-absent.ttf")
	e := NewEmulator(2, 1)
	write(t, e, "中")

	raw, err := PNG(e.Screen(), nil, ftFontCellW, ftFontCellH, WithFont(missing, 0))
	if err == nil {
		t.Fatalf("不存在的字体返回成功(疑似静默回退),输出 %d 字节", len(raw))
	}
	if len(raw) != 0 {
		t.Errorf("出错时不应返回 PNG 数据,实际 %d 字节", len(raw))
	}
	for _, want := range []string{"failed to read font", missing} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺少 %q: %v", want, err)
		}
	}
}

// TestFontUnparsableFileErrors 验证 --font 指向损坏文件时报错而非回退。
// 只测"文件不存在"会漏掉"文件存在但不是字体"这种更隐蔽的静默回退。
func TestFontUnparsableFileErrors(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "broken.ttf")
	if err := os.WriteFile(broken, []byte("this is not a TTF/OTF font"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := NewEmulator(2, 1)
	write(t, e, "中")

	raw, err := PNG(e.Screen(), nil, ftFontCellW, ftFontCellH, WithFont(broken, 0))
	if err == nil {
		t.Fatalf("损坏的字体返回成功(疑似静默回退),输出 %d 字节", len(raw))
	}
	if len(raw) != 0 {
		t.Errorf("出错时不应返回 PNG 数据,实际 %d 字节", len(raw))
	}
	for _, want := range []string{"failed to parse font", broken} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺少 %q: %v", want, err)
		}
	}
}
