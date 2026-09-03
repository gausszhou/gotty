package capture

import (
	"strings"
	"testing"
	"time"
)

// helpers ----------------------------------------------------------------

func write(t *testing.T, e *Emulator, s string) {
	t.Helper()
	if _, err := e.Write([]byte(s)); err != nil {
		t.Fatalf("Write(%q): %v", s, err)
	}
}

func assertText(t *testing.T, e *Emulator, want string) {
	t.Helper()
	got := Text(e.Screen())
	if got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func assertCursor(t *testing.T, e *Emulator, row, col int) {
	t.Helper()
	if r, c := e.Cursor(); r != row || c != col {
		t.Errorf("cursor = (%d,%d), want (%d,%d)", r, c, row, col)
	}
}

// basic text / line discipline ------------------------------------------

func TestBasicText(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "hello")
	assertText(t, e, "hello")
	assertCursor(t, e, 0, 5)
}

func TestLineFeedCarriageReturn(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "a\r\nb")
	assertText(t, e, "a\nb")

	// 单独 CR 归位覆盖同一行的字符(b 之后光标在第 1 列,X 覆盖 b)
	write(t, e, "\rX")
	assertText(t, e, "a\nX")
}

func TestBackspace(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "abc\b\bX")
	assertText(t, e, "aXc")
	assertCursor(t, e, 0, 2)
}

func TestTab(t *testing.T) {
	e := NewEmulator(20, 3)
	write(t, e, "a\tb")
	assertText(t, e, "a       b") // tab stop 8
	assertCursor(t, e, 0, 9)
}

func TestControlCharsIgnored(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "\x01\x02\x1a\x7fok")
	assertText(t, e, "ok")
}

func TestLFOnlyDiscipline(t *testing.T) {
	// 裸 LF 只下移、不归位(xterm LF 语义;真实带 ONLCR 的 PTY 输出
	// 是 CR+LF,归位由 CR 完成——见各 \r\n 用例)
	e := NewEmulator(10, 3)
	write(t, e, "a\nb")
	assertText(t, e, "a\n b") // b 写在与 a 相同的列位置(第 1 列)
	assertCursor(t, e, 1, 2)
}

// cursor movement --------------------------------------------------------

func TestCursorMovementCSI(t *testing.T) {
	e := NewEmulator(10, 4)
	write(t, e, "abc")
	write(t, e, "\x1b[2D") // 左移 2 → 第 1 列
	write(t, e, "X")
	assertText(t, e, "aXc")

	write(t, e, "\x1b[1B") // 下移(X 之后光标在第 2 列)
	write(t, e, "Y")
	assertText(t, e, "aXc\n  Y") // 行1 = "  "+Y 于第 2 列
	assertCursor(t, e, 1, 3)
}

func TestCursorClamp(t *testing.T) {
	e := NewEmulator(5, 2)
	write(t, e, "\x1b[9A\x1b[9DZ") // 上/左越界后写
	assertText(t, e, "Z")
	assertCursor(t, e, 0, 1) // Z 之后光标右移一位
	write(t, e, "\x1b[9B\x1b[9C")
	assertCursor(t, e, 1, 4)
}

func TestCursorNextPrevLine(t *testing.T) {
	e := NewEmulator(10, 5)
	write(t, e, "ab")
	write(t, e, "\x1b[2E") // next line x2
	assertText(t, e, "ab") // 尾部空行被剪裁
	assertCursor(t, e, 2, 0)
	write(t, e, "\x1b[1F") // prev line x1
	assertCursor(t, e, 1, 0)
}

func TestCursorPositioning(t *testing.T) {
	e := NewEmulator(10, 5)
	write(t, e, "\x1b[2;3HX") // 1-based
	if g := e.Screen().Cell(1, 2); g.Rune != 'X' {
		t.Errorf("cell(1,2) = %q, want X", g.Rune)
	}
	assertCursor(t, e, 1, 3)
	write(t, e, "\x1b[H") // home
	assertCursor(t, e, 0, 0)
	write(t, e, "\x1b[3G") // column 3
	assertCursor(t, e, 0, 2)
	write(t, e, "\x1b[4d") // row 4
	assertCursor(t, e, 3, 2)
}

func TestSaveRestoreCursor(t *testing.T) {
	e := NewEmulator(10, 5)
	write(t, e, "\x1b[3;4H\x1b7\x1b[H\x1b8")
	assertCursor(t, e, 2, 3)
	write(t, e, "\x1b[3;4H\x1b[s\x1b[H\x1b[u")
	assertCursor(t, e, 2, 3)
}

// erasure ----------------------------------------------------------------

func TestEraseLine(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "abcdef")
	write(t, e, "\x1b[3D\x1b[K") // 左移 3 到第 3 列,擦到行尾
	assertText(t, e, "abc")

	e = NewEmulator(10, 3)
	write(t, e, "abcdef")
	write(t, e, "\x1b[3D\x1b[1K") // 行首到光标(含光标格):擦 col0-3
	assertText(t, e, "    ef")

	e = NewEmulator(10, 3)
	write(t, e, "abcdef")
	write(t, e, "\x1b[2K")
	assertText(t, e, "")
}

func TestEraseDisplay(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "aaa\r\nbbb\r\nccc")
	write(t, e, "\x1b[2;1H\x1b[J") // 第二行起向下全擦
	assertText(t, e, "aaa")

	e = NewEmulator(10, 3)
	write(t, e, "aaa\r\nbbb\r\nccc")
	write(t, e, "\x1b[2;1H\x1b[1J")
	// x/vt 的 ED1 会整行清除(含光标所在行);xterm 只清光标行到光标列,
	// 这是底层仿真器的差异,以 x/vt(经 vttest)行为为基准。
	assertText(t, e, "\n\nccc")

	e = NewEmulator(10, 3)
	write(t, e, "aaa\r\nbbb\r\nccc")
	write(t, e, "\x1b[2J")
	assertText(t, e, "")
}

func TestEraseKeepsBackground(t *testing.T) {
	// 擦除后的格继承当前背景色(xterm erase 语义)
	e := NewEmulator(10, 2)
	write(t, e, "\x1b[41mX\x1b[K")
	c := e.Screen().Cell(0, 1)
	if c.Bg.Mode != ColorIndex || c.Bg.Index != 1 {
		t.Errorf("erased cell bg = %+v, want red(index 1)", c.Bg)
	}
}

// SGR --------------------------------------------------------------------

func sgrCheck(t *testing.T, seq, text string, check func(Cell) bool) {
	t.Helper()
	e := NewEmulator(20, 2)
	write(t, e, seq+text)
	c := e.Screen().Cell(0, 0)
	if !check(c) {
		t.Errorf("SGR %q on %q: cell = %+v", seq, text, c)
	}
}

func TestSGRIndexColors(t *testing.T) {
	sgrCheck(t, "\x1b[31m", "R", func(c Cell) bool {
		return c.Fg.Mode == ColorIndex && c.Fg.Index == 1
	})
	sgrCheck(t, "\x1b[44m", " ", func(c Cell) bool {
		return c.Bg.Mode == ColorIndex && c.Bg.Index == 4
	})
	sgrCheck(t, "\x1b[90m", "R", func(c Cell) bool {
		return c.Fg.Mode == ColorIndex && c.Fg.Index == 8
	})
	sgrCheck(t, "\x1b[107m", " ", func(c Cell) bool {
		return c.Bg.Mode == ColorIndex && c.Bg.Index == 15
	})
}

func TestSGR256Colors(t *testing.T) {
	sgrCheck(t, "\x1b[38;5;200m", "R", func(c Cell) bool {
		return c.Fg.Mode == ColorIndex && c.Fg.Index == 200
	})
	sgrCheck(t, "\x1b[48;5;17m", " ", func(c Cell) bool {
		return c.Bg.Mode == ColorIndex && c.Bg.Index == 17
	})
}

func TestSGRTrueColor(t *testing.T) {
	sgrCheck(t, "\x1b[38;2;10;20;30m", "R", func(c Cell) bool {
		return c.Fg.Mode == ColorRGB && c.Fg.RGB == 0x0a141e
	})
	sgrCheck(t, "\x1b[48;2;255;0;128m", " ", func(c Cell) bool {
		return c.Bg.Mode == ColorRGB && c.Bg.RGB == 0xff0080
	})
}

func TestSGRAttributes(t *testing.T) {
	e := NewEmulator(20, 2)
	write(t, e, "\x1b[1;3;4;5;7;8;9mX")
	c := e.Screen().Cell(0, 0)
	if !c.Bold || !c.Italic || !c.Underline || !c.Blink || !c.Reverse || !c.Invisible || !c.Strikethrough {
		t.Errorf("attrs not all set: %+v", c)
	}
	write(t, e, "\x1b[0mY")
	c = e.Screen().Cell(0, 1)
	if c.Bold || c.Italic {
		t.Errorf("reset did not clear attrs: %+v", c)
	}
	write(t, e, "\x1b[22;23;24;25;27;28;29mZ") // 逐个关闭(bold+dim/italic/underline/blink/reverse/invisible/strike)
	c = e.Screen().Cell(0, 2)
	if c.Bold || c.Dim || c.Italic || c.Underline || c.Blink || c.Reverse || c.Invisible || c.Strikethrough {
		t.Errorf("attr-off SGR failed: %+v", c)
	}
}

func TestSGRColonExtensionDegrades(t *testing.T) {
	// 冒号扩展 4:3(下划线:双线)被 x/vt 正确解析为下划线,而不会把
	// 第二段的 3 误读为斜体(旧引擎的降级行为)。
	e := NewEmulator(20, 2)
	write(t, e, "\x1b[4:3mX")
	c := e.Screen().Cell(0, 0)
	if !c.Underline || c.Italic {
		t.Errorf("colon SGR 4:3 parsed wrongly: %+v", c)
	}
}

func TestSGRDefaults(t *testing.T) {
	e := NewEmulator(20, 2)
	write(t, e, "\x1b[31mX\x1b[39mY\x1b[42mZ\x1b[49mW")
	c := e.Screen().Cell(0, 1)
	if c.Fg.Mode != ColorDefault {
		t.Errorf("39 did not restore default fg: %+v", c.Fg)
	}
	c = e.Screen().Cell(0, 3)
	if c.Bg.Mode != ColorDefault {
		t.Errorf("49 did not restore default bg: %+v", c.Bg)
	}
}

// scrolling --------------------------------------------------------------

func TestScrollOnOverflow(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "a\r\nb\r\nc\r\nd\r\n")
	assertText(t, e, "c\nd")
}

func TestScrollRegion(t *testing.T) {
	e := NewEmulator(10, 4)
	write(t, e, "\x1b[2;3r") // 区域 = 行1..2(0-based)
	write(t, e, "X\r\nY\r\nZ\r\n")
	// X 在行0(区域外),滚动不影响它;区域 [1,2] 内 Z 滚到行1
	if g := e.Screen().Cell(0, 0); g.Rune != 'X' {
		t.Errorf("row0 = %q, want X (region must not scroll it)", g.Rune)
	}
	if g := e.Screen().Cell(1, 0); g.Rune != 'Z' {
		t.Errorf("row1 = %q, want Z", g.Rune)
	}
}

func TestScrollUpDown(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "a\r\nb\r\nc")
	write(t, e, "\x1b[1S") // 上滚一行:行0 丢,底部补空
	assertText(t, e, "b\nc")
	write(t, e, "\x1b[1T") // 下滚一行:顶部补空
	assertText(t, e, "\nb\nc")
}

func TestInsertDeleteLines(t *testing.T) {
	e := NewEmulator(10, 4)
	write(t, e, "\x1b[1;3r") // 区域 0..2
	write(t, e, "a\r\nb")
	write(t, e, "\x1b[2;2H") // 光标行1
	write(t, e, "\x1b[1L")   // 插入一行
	if g := e.Screen().Cell(1, 0); g.Rune != ' ' {
		t.Errorf("inserted line not blank: %q", g.Rune)
	}
	if g := e.Screen().Cell(2, 0); g.Rune != 'b' {
		t.Errorf("row2 = %q, want b (pushed down)", g.Rune)
	}

	write(t, e, "\x1b[2;2H\x1b[1M") // 再删一行
	if g := e.Screen().Cell(1, 0); g.Rune != 'b' {
		t.Errorf("after delete row1 = %q, want b", g.Rune)
	}
}

func TestCursorHomeOnDECSTBM(t *testing.T) {
	e := NewEmulator(10, 5)
	write(t, e, "\x1b[2;2H")
	write(t, e, "\x1b[2;4r")
	assertCursor(t, e, 0, 0)
}

// wrap --------------------------------------------------------------------

func TestWrapPending(t *testing.T) {
	e := NewEmulator(5, 2)
	write(t, e, "12345X")
	assertText(t, e, "12345\nX")
	assertCursor(t, e, 1, 1)
}

func TestWideCharAtLastColumnWraps(t *testing.T) {
	e := NewEmulator(4, 2)
	write(t, e, "ab中中")
	// "ab" 占 0-1,第一个"中"占 2-3(放满),第二个"中"换行到行1
	assertText(t, e, "ab中\n中")
}

// wide characters --------------------------------------------------------

func TestWideCharPlaceholder(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "中")
	if g := e.Screen().Cell(0, 0); g.Rune != '中' {
		t.Errorf("cell0 = %q, want 中", g.Rune)
	}
	if g := e.Screen().Cell(0, 1); g.Rune != 0 {
		t.Errorf("cell1 = %q, want placeholder 0", g.Rune)
	}
	assertCursor(t, e, 0, 2)
	assertText(t, e, "中")
}

func TestUTF8Multibyte(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "é😀")
	assertText(t, e, "é😀") // é 宽1, emoji 宽2
	assertCursor(t, e, 0, 3)
}

func TestUTF8SplitAcrossWrites(t *testing.T) {
	e := NewEmulator(10, 3)
	b := []byte("中")
	_, _ = e.Write(b[:1]) // 首字节
	_, _ = e.Write(b[1:]) // 续字节
	assertText(t, e, "中")
}

// alternate screen --------------------------------------------------------

func TestAlternateScreen(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "main")
	write(t, e, "\x1b[?1049h")
	assertText(t, e, "") // 主屏内容被隔离
	write(t, e, "alt")
	assertText(t, e, "alt")
	write(t, e, "\x1b[?1049l")
	assertText(t, e, "main") // 恢复主屏
}

func TestCursorVisibleToggling(t *testing.T) {
	e := NewEmulator(10, 2)
	if !e.CursorVisible() {
		t.Fatal("cursor visible by default")
	}
	write(t, e, "\x1b[?25l")
	if e.CursorVisible() {
		t.Error("?25l did not hide cursor")
	}
	write(t, e, "\x1b[?25h")
	if !e.CursorVisible() {
		t.Error("?25h did not show cursor")
	}
}

// robustness --------------------------------------------------------------

func TestUnknownSequencesIgnored(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "\x1b[999;999Zok") // 未知 final
	assertText(t, e, "ok")
	write(t, e, "\x1b[?999h") // 未知 private
	assertText(t, e, "ok")
	write(t, e, "\x1b\x01\x1bc") // 未知 ESC + RIS
	assertText(t, e, "")
}

func TestOSCDCSIgnored(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "\x1b]0;window title\x07ok")
	assertText(t, e, "ok")
	write(t, e, "\x1b]52;c;////\x1b\\ok2") // ESC\ 结尾
	assertText(t, e, "okok2")
	write(t, e, "\x1bP1;2;3\x1b\\ok3") // DCS
	assertText(t, e, "okok2ok3")
}

func TestDSRIgnored(t *testing.T) {
	e := NewEmulator(10, 2)
	write(t, e, "\x1b[6nok") // 设备状态请求:消费但不应答
	assertText(t, e, "ok")
}

func TestRISReset(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "\x1b[31mX")
	write(t, e, "\x1b[2;2H\x1b[?1049h\x1b[?25l\x1bc")
	assertText(t, e, "")
	assertCursor(t, e, 0, 0)
	if !e.CursorVisible() {
		t.Error("RIS did not restore cursor visibility")
	}
	if g := e.Screen().Cell(0, 0); g.Rune != ' ' {
		t.Errorf("RIS did not clear screen: %q", g.Rune)
	}
}

func TestIncompleteSequenceAtEOF(t *testing.T) {
	// 不完整 CSI 在流结束时不应 panic,也不应影响已渲染内容
	e := NewEmulator(10, 3)
	write(t, e, "ab\x1b[3")
	write(t, e, "c") // 'c' 是 CSI 的 final byte:序列完整但指令未知 → 忽略
	assertText(t, e, "ab")

	e = NewEmulator(10, 3)
	write(t, e, "ab\x1b[3") // 流结束,CSI 未闭合
	assertText(t, e, "ab")
}

func TestEmptyAndNestedWritesDontPanic(t *testing.T) {
	e := NewEmulator(10, 3)
	if _, err := e.Write(nil); err != nil {
		t.Fatal(err)
	}
	write(t, e, "\x1b[")
	write(t, e, "\x1b[;")
	write(t, e, "\x1b[31")
	write(t, e, "mX") // 拼成完整 SGR 后再写字
	if g := e.Screen().Cell(0, 0); g.Fg.Mode != ColorIndex || g.Fg.Index != 1 {
		t.Errorf("split CSI not applied: %+v", g)
	}
}

// resize ------------------------------------------------------------------

func TestResizeGrowsKeepsTopLeft(t *testing.T) {
	e := NewEmulator(5, 2)
	write(t, e, "abc")
	e.Resize(8, 3)
	if e.Cols() != 8 || e.Rows() != 3 {
		t.Fatalf("size = %dx%d, want 8x3", e.Cols(), e.Rows())
	}
	assertText(t, e, "abc") // 内容保留在左上角
}

func TestResizeShrinksTruncates(t *testing.T) {
	e := NewEmulator(10, 4)
	write(t, e, "hello world") // 第 10 列写满后 "d" 换行到第 2 行
	e.Resize(6, 2)
	assertText(t, e, "hello\nd") // 第 7 列起截断,第 3/4 行丢弃
}

func TestResizeClampsCursorAndScrollRegion(t *testing.T) {
	e := NewEmulator(10, 5)
	write(t, e, "12345")
	write(t, e, "\x1b[2;4r\x1b[5;10H") // 滚动区 2-4,光标 (4,9)
	e.Resize(3, 2)
	if r, c := e.Cursor(); r != 1 || c != 2 {
		t.Errorf("cursor = (%d,%d), want clamped (1,2)", r, c)
	}
	// 滚动区被重置为整屏,行末换行正常滚动
	write(t, e, "A\n")
	assertText(t, e, "  A") // A 写在 (1,2),换行后滚到第 0 行,前导空格保留
}

func TestResizeClearsAnswersAndImages(t *testing.T) {
	e := NewEmulator(10, 3)
	e.answer([]byte("\x1b[0n")) // 未排空的应答
	e.Resize(12, 4)
	if got := e.DrainAnswers(); len(got) != 0 {
		t.Errorf("answers after resize = %q, want empty", got)
	}
	if len(e.Images()) != 0 {
		t.Errorf("images after resize = %d, want 0", len(e.Images()))
	}
}

// terminal query answers (DA/DSR/DECRQM) ----------------------------------
//
// 应答由底层 x/vt 仿真器生成(经内部 pw 管道泵入应答队列);DA1 应答
// VT220 而非旧引擎的 VT102,内容以 x/vt 为准——程序只要求"有应答"。

// waitAnswers 轮询直到 DrainAnswers 拿到应答(泵是异步的),最多 1s。
func waitAnswers(t *testing.T, e *Emulator) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if got := string(e.DrainAnswers()); got != "" {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatal("no query answer within 1s")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAnswerDA1(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[c")
	if got := waitAnswers(t, e); got != "\x1b[?62;1;6;22c" {
		t.Errorf("DA1 answer = %q, want %q", got, "\x1b[?62;1;6;22c")
	}
	// 带参数的 DA1(0 才算无参数,同 xterm):非 0 参数不应答
	write(t, e, "\x1b[1c")
	time.Sleep(20 * time.Millisecond)
	if got := string(e.DrainAnswers()); got != "" {
		t.Errorf("DA1 with param must not answer, got %q", got)
	}
}

func TestAnswerDA2(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[>c")
	if got := waitAnswers(t, e); got != "\x1b[>1;10;0c" {
		t.Errorf("DA2 answer = %q, want %q", got, "\x1b[>1;10;0c")
	}
}

func TestAnswerDSR(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "ab\r\n") // 光标 (1,0)
	write(t, e, "\x1b[6n")
	if got := waitAnswers(t, e); got != "\x1b[2;1R" {
		t.Errorf("DSR6 answer = %q, want %q", got, "\x1b[2;1R")
	}
	write(t, e, "\x1b[5n")
	if got := waitAnswers(t, e); got != "\x1b[?0n" {
		t.Errorf("DSR5 answer = %q, want %q", got, "\x1b[?0n")
	}
	write(t, e, "\x1b[?6n") // DECXCPR
	if got := waitAnswers(t, e); got != "\x1b[?2;1R" {
		t.Errorf("DECXCPR answer = %q, want %q", got, "\x1b[?2;1R")
	}
}

func TestAnswerDECRQM(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[?25$p") // 光标可见 → 1
	if got := waitAnswers(t, e); got != "\x1b[?25;1$y" {
		t.Errorf("DECRQM ?25 = %q, want %q", got, "\x1b[?25;1$y")
	}
	write(t, e, "\x1b[?25l\x1b[?25$p") // 隐藏后 → 2
	if got := waitAnswers(t, e); got != "\x1b[?25;2$y" {
		t.Errorf("DECRQM ?25 after hide = %q, want %q", got, "\x1b[?25;2$y")
	}
	write(t, e, "\x1b[?1049h\x1b[?1049$p") // 备用屏 → 1
	if got := waitAnswers(t, e); got != "\x1b[?1049;1$y" {
		t.Errorf("DECRQM ?1049 = %q, want %q", got, "\x1b[?1049;1$y")
	}
	write(t, e, "\x1b[?2026$p") // 未跟踪模式 → 0
	if got := waitAnswers(t, e); got != "\x1b[?2026;0$y" {
		t.Errorf("DECRQM unknown = %q, want %q", got, "\x1b[?2026;0$y")
	}
}

func TestAnswerDECRQMModes(t *testing.T) {
	// 非私有/私有模式按真实状态应答:IRM(4)、DECAWM(7)、LNM(20)
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[?7$p") // DECAWM 默认开 → 1
	if got := waitAnswers(t, e); got != "\x1b[?7;1$y" {
		t.Errorf("DECRQM ?7 default = %q", got)
	}
	write(t, e, "\x1b[?7l\x1b[?7$p") // 关闭 → 2
	if got := waitAnswers(t, e); got != "\x1b[?7;2$y" {
		t.Errorf("DECRQM ?7 off = %q", got)
	}
	write(t, e, "\x1b[4h\x1b[4$p") // IRM 开 → 1
	if got := waitAnswers(t, e); got != "\x1b[4;1$y" {
		t.Errorf("DECRQM 4 set = %q", got)
	}
	write(t, e, "\x1b[20h\x1b[20$p") // LNM 开 → 1
	if got := waitAnswers(t, e); got != "\x1b[20;1$y" {
		t.Errorf("DECRQM 20 set = %q", got)
	}
}

func TestAnswerOSCColorQueries(t *testing.T) {
	// OSC 10;?/11;?/12;?(x/vt 原生应答,写回队列)
	e := NewEmulator(20, 5)
	write(t, e, "\x1b]10;?\x07")
	if got := waitAnswers(t, e); !strings.HasPrefix(got, "\x1b]10;rgb:") {
		t.Errorf("OSC10 query answer = %q", got)
	}
	write(t, e, "\x1b]11;?\x07")
	if got := waitAnswers(t, e); !strings.HasPrefix(got, "\x1b]11;rgb:") {
		t.Errorf("OSC11 query answer = %q", got)
	}
	write(t, e, "\x1b]12;?\x1b\\")
	if got := waitAnswers(t, e); !strings.HasPrefix(got, "\x1b]12;rgb:") {
		t.Errorf("OSC12 query answer = %q", got)
	}
}

func TestAnswersBoundedAndDrainable(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[5n")
	write(t, e, "\x1b[5n")
	// 泵异步送达:DrainAnswers 自带等待,等两条都到再断言。
	got1 := string(e.DrainAnswers())
	got2 := string(e.DrainAnswers())
	if got1+got2 != "\x1b[?0n\x1b[?0n" {
		t.Errorf("answers = %q+%q, want two DSR5 replies", got1, got2)
	}
	if got := e.DrainAnswers(); len(got) != 0 {
		t.Errorf("later drain = %q, want empty", got)
	}
}

func TestAnswersCapBounded(t *testing.T) {
	// 超上限的应答被截断(旧引擎同款防查询风暴语义)
	e := NewEmulator(20, 5)
	e.answersCap = 6
	write(t, e, "\x1b[5n") // \x1b[?0n = 5 字节,放得下
	write(t, e, "\x1b[5n") // 第二条只剩 1 字节空间 → 截断
	if got := string(e.DrainAnswers()); got != "\x1b[?0n\x1b" {
		t.Errorf("capped answers = %q, want %q", got, "\x1b[?0n\x1b")
	}
}

// 新增 CSI:ICH/DCH/ECH/REP(由 x/vt 原生实现,锁行为) ----------------

func TestInsertDeleteEraseChars(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "abcdef")
	write(t, e, "\x1b[3D\x1b[3@") // ICH 3:光标处插 3 空,右侧被推出
	assertText(t, e, "abc   def")
	assertCursor(t, e, 0, 3)

	write(t, e, "\x1b[3P") // DCH 3:删光标处 3 字符,尾部补空
	assertText(t, e, "abcdef")
	assertCursor(t, e, 0, 3)

	write(t, e, "\x1b[2X") // ECH 2:擦除光标处 2 字符,不移动光标
	assertText(t, e, "abc  f")
	assertCursor(t, e, 0, 3)
}

func TestRepeatPreviousChar(t *testing.T) {
	e := NewEmulator(10, 2)
	write(t, e, "a\x1b[3b") // 重复 'a' 3 次
	assertText(t, e, "aaaa")

	// 无前序字符时 REP 为空操作
	e = NewEmulator(10, 2)
	write(t, e, "\x1b[5bX")
	assertText(t, e, "X")

	// 宽字符重复:按 2 格处理(x/vt 的 REP 只记录单宽字符,由包装层翻译)
	e = NewEmulator(10, 2)
	write(t, e, "中\x1b[2b") // 再复制 2 个 → 共 3 个
	assertText(t, e, "中中中")
	assertCursor(t, e, 0, 6)
}

func TestDECAWMOffOverwritesInPlace(t *testing.T) {
	// DECAWM ?7l:行尾不再换行,原地覆盖
	e := NewEmulator(5, 2)
	write(t, e, "12345\x1b[1D\x1b[?7lX")
	assertText(t, e, "123X5")
	assertCursor(t, e, 0, 4)
	write(t, e, "Y") // 仍停在最后一格覆盖
	assertText(t, e, "123XY")
	assertCursor(t, e, 0, 4)
}

func TestTabStopsAndTBC(t *testing.T) {
	e := NewEmulator(20, 2)
	write(t, e, "a\tb")
	assertCursor(t, e, 0, 9) // 默认制表位 8

	// 清除全部制表位后 TAB 走到行尾
	e = NewEmulator(20, 2)
	write(t, e, "\x1b[3g")
	write(t, e, "a\tb")
	assertText(t, e, "a"+strings.Repeat(" ", 18)+"b")
	assertCursor(t, e, 0, 19)
}

func TestIRSInsertMode(t *testing.T) {
	// IRM(CSI 4 h):后续字符前移插入,不覆盖(包装层注入 ICH 实现)
	e := NewEmulator(10, 2)
	write(t, e, "abc")
	write(t, e, "\x1b[2D\x1b[4hXY")
	assertText(t, e, "aXYbc") // X/Y 插入,b、c 前移
	write(t, e, "\x1b[4lZ")   // 退出插入模式:Z 覆盖
	assertText(t, e, "aXYZc")
}

func TestCSISaveRestoreCursorTranslated(t *testing.T) {
	// x/vt 未实现 ANSI CSI s/u;包装层翻译为 DECSC/DECRC(ESC 7/8)
	e := NewEmulator(10, 5)
	write(t, e, "\x1b[3;4H\x1b[s\x1b[H\x1b[u")
	assertCursor(t, e, 2, 3)
	// DECSC/DECRC 原生路径也保持
	e = NewEmulator(10, 5)
	write(t, e, "\x1b[3;4H\x1b7\x1b[H\x1b8")
	assertCursor(t, e, 2, 3)
}

func TestColorsMappedFromXVT(t *testing.T) {
	e := NewEmulator(20, 2)
	write(t, e, "\x1b[31mR\x1b[38;5;123mX\x1b[38;2;1;2;3mY")
	grid := e.Screen()
	if c := grid.Cell(0, 0); c.Fg.Mode != ColorIndex || c.Fg.Index != 1 {
		t.Errorf("SGR31 fg = %+v, want index 1", c.Fg)
	}
	if c := grid.Cell(0, 1); c.Fg.Mode != ColorIndex || c.Fg.Index != 123 {
		t.Errorf("SGR38;5 fg = %+v, want index 123", c.Fg)
	}
	if c := grid.Cell(0, 2); c.Fg.Mode != ColorRGB || c.Fg.RGB != 0x010203 {
		t.Errorf("SGR38;2 fg = %+v, want RGB 0x010203", c.Fg)
	}
	// 属性映射
	e = NewEmulator(20, 2)
	write(t, e, "\x1b[1;4;7mZ")
	c := e.Screen().Cell(0, 0)
	if !c.Bold || !c.Underline || !c.Reverse {
		t.Errorf("attrs = bold:%v underline:%v reverse:%v, want all true",
			c.Bold, c.Underline, c.Reverse)
	}
}

func TestLNM(t *testing.T) {
	// 默认 LNM 关:LF 只下移不归位
	e := NewEmulator(10, 3)
	write(t, e, "ab\ndef") // def 在行1 第 3 列起
	assertText(t, e, "ab\n  def")

	// LNM(20h)开后 LF 隐含 CR(x/vt 与 xterm 一致)
	e = NewEmulator(10, 3)
	write(t, e, "\x1b[20h")
	write(t, e, "ab\ndef")
	assertText(t, e, "ab\ndef")
}
