package capture

import (
	"testing"
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
	write(t, e, "\x1b[2;1H\x1b[1J") // 向上全擦:行0 整行 + 行1 到光标(含光标格 col0)
	assertText(t, e, "\n bb\nccc")

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
	// 冒号扩展 4:3(下划线样式)不应把参数误读成 reset
	e := NewEmulator(20, 2)
	write(t, e, "\x1b[4:3mX")
	c := e.Screen().Cell(0, 0)
	if !c.Underline || !c.Italic {
		t.Errorf("colon SGR degraded wrongly: %+v", c)
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

func TestAnswerDA1(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[c")
	if got := string(e.DrainAnswers()); got != "\x1b[?1;2c" {
		t.Errorf("DA1 answer = %q, want %q", got, "\x1b[?1;2c")
	}
	// vim 风格:private 前缀 + 参数,应答相同
	write(t, e, "\x1b[?1;2c")
	if got := string(e.DrainAnswers()); got != "\x1b[?1;2c" {
		t.Errorf("DA1 (private) answer = %q, want %q", got, "\x1b[?1;2c")
	}
}

func TestAnswerDA2(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[>c")
	if got := string(e.DrainAnswers()); got != "\x1b[>0;0;0c" {
		t.Errorf("DA2 answer = %q, want %q", got, "\x1b[>0;0;0c")
	}
}

func TestAnswerDSR(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "ab\r\n") // 光标 (1,0)
	write(t, e, "\x1b[6n")
	if got := string(e.DrainAnswers()); got != "\x1b[2;1R" {
		t.Errorf("DSR6 answer = %q, want %q", got, "\x1b[2;1R")
	}
	write(t, e, "\x1b[5n")
	if got := string(e.DrainAnswers()); got != "\x1b[0n" {
		t.Errorf("DSR5 answer = %q, want %q", got, "\x1b[0n")
	}
	write(t, e, "\x1b[?6n") // DECXCPR
	if got := string(e.DrainAnswers()); got != "\x1b[?2;1R" {
		t.Errorf("DECXCPR answer = %q, want %q", got, "\x1b[?2;1R")
	}
}

func TestAnswerDECRQM(t *testing.T) {
	e := NewEmulator(20, 5)
	write(t, e, "\x1b[?25$p") // 光标可见 → 1
	if got := string(e.DrainAnswers()); got != "\x1b[?25;1$y" {
		t.Errorf("DECRQM ?25 = %q, want %q", got, "\x1b[?25;1$y")
	}
	write(t, e, "\x1b[?25l\x1b[?25$p") // 隐藏后 → 2
	if got := string(e.DrainAnswers()); got != "\x1b[?25;2$y" {
		t.Errorf("DECRQM ?25 after hide = %q, want %q", got, "\x1b[?25;2$y")
	}
	write(t, e, "\x1b[?1049h\x1b[?1049$p") // 备用屏 → 1
	if got := string(e.DrainAnswers()); got != "\x1b[?1049;1$y" {
		t.Errorf("DECRQM ?1049 = %q, want %q", got, "\x1b[?1049;1$y")
	}
	write(t, e, "\x1b[?2026$p") // 未跟踪模式 → 0
	if got := string(e.DrainAnswers()); got != "\x1b[?2026;0$y" {
		t.Errorf("DECRQM unknown = %q, want %q", got, "\x1b[?2026;0$y")
	}
}

func TestAnswersBoundedAndDrainable(t *testing.T) {
	e := NewEmulator(20, 5)
	e.answersCap = 8
	write(t, e, "\x1b[5n") // 4 字节应答
	write(t, e, "\x1b[5n")
	write(t, e, "\x1b[5n") // 第三条超出上限被丢弃
	if got := string(e.DrainAnswers()); got != "\x1b[0n\x1b[0n" {
		t.Errorf("capped answers = %q, want two DSR5 replies", got)
	}
	if got := e.DrainAnswers(); len(got) != 0 {
		t.Errorf("second drain = %q, want empty", got)
	}
}
