package capture

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// 本文件覆盖 0005 §4 的 scrollback 验收项:历史行转 Grid 的文本/JSON 断言
// (含宽字符、SGR 颜色、超长行换行/截断)、lines=N 的尾部优先语义、备用屏、
// --scrollback=0(SetScrollbackLines(0))与容量上限。
//
// helper 统一以 sb 开头:同包可能有其他人并行补测试,避免重名。

// sbHistory 把 lines 逐行写进仿真器,并保证它们全部滚入历史(而非留在可见屏)。
//
// 两个不可省的步骤:
//   - 开头的归位:上一批结束时屏内已空、但光标停在底部,直接续写会让空行
//     先滚进历史,容量/尾部断言就不准了;
//   - 结尾补 rows-1 个换行:最后停在可见屏顶部的那些行必须再被挤出屏幕一次,
//     否则 Total 会比实际写入行数少(屏幕有几行,就要多滚几行才能全部进入历史)。
func sbHistory(t *testing.T, e *Emulator, lines ...string) {
	t.Helper()
	write(t, e, "\x1b[H")
	for _, line := range lines {
		write(t, e, line+"\r\n")
	}
	if extra := e.Rows() - 1; extra > 0 {
		write(t, e, strings.Repeat("\r\n", extra))
	}
}

// sbGrid 返回非空的滚动历史网格;为空时直接失败,后续断言可以放心解引用。
func sbGrid(t *testing.T, v ScrollbackView) *Grid {
	t.Helper()
	if v.Grid == nil {
		t.Fatalf("scrollback grid is nil (disabled=%v alternate=%v total=%d)",
			v.Disabled, v.Alternate, v.Total)
	}
	return v.Grid
}

// sbRows 返回网格逐行的纯文本(不裁剪尾部空行),按行断言比整段 Text 更精确。
func sbRows(g *Grid) []string {
	out := make([]string, g.Rows())
	for r := range out {
		out[r] = RowText(g, r)
	}
	return out
}

// 历史行到 Grid 的文本投影:补齐到屏幕宽度、宽字符占位与尾部空格裁剪都必须
// 正确(它不复用 Screen(),而是自己物化)。
func TestScrollbackLineToGridText(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 10)
	sbHistory(t, e, "first", "中a", "pad   ")

	v := e.Scrollback(0)
	if v.Disabled {
		t.Fatal("scrollback unexpectedly disabled")
	}
	if v.Alternate {
		t.Fatal("main screen reported as alternate")
	}
	if v.Total != 3 {
		t.Fatalf("total = %d, want 3", v.Total)
	}

	g := sbGrid(t, v)
	// 历史行本身按内容裁掉了尾部空格,物化时再补齐到当前屏幕宽度。
	if g.Cols() != 10 || g.Rows() != 3 {
		t.Fatalf("grid = %d rows x %d cols, want 3 rows x 10 cols", g.Rows(), g.Cols())
	}
	want := []string{"first", "中a", "pad"}
	if got := sbRows(g); !slices.Equal(got, want) {
		t.Errorf("row texts = %q, want %q", got, want)
	}

	// 宽字符续列必须是占位格(Rune==0),与 Screen() 一致:若退化成空格,
	// RowText 会多出一个空格,文本宽度就失真了。
	if c := g.Cell(1, 1); c.Rune != 0 {
		t.Errorf("wide-char continuation cell = %q (fg=%+v), want placeholder rune 0", c.Rune, c.Fg)
	}
	// 历史行尾被裁掉后,补出来的格子是普通空格,不带任何样式("first" 占 0-4)。
	if c := g.Cell(0, 5); c.Rune != ' ' || cellHasStyle(c) {
		t.Errorf("padding cell = %+v, want plain blank", c)
	}

	// 整段文本:各行按历史顺序拼接,且尾部空行/空格被裁剪。
	if got, want := Text(g), "first\n中a\npad"; got != want {
		t.Errorf("scrollback text = %q, want %q", got, want)
	}
}

// 历史行的样式要能通过 CellsJSON 还原成 --format json 的 wire 形式
// (SGR 索引色 / 256 色 / 真彩背景 + 属性),否则 agent 拿到的是无色文本。
func TestScrollbackLineToGridJSON(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 10)
	sbHistory(t, e,
		"\x1b[31mred\x1b[0m \x1b[1;38;5;196mB\x1b[0m",
		"\x1b[48;2;0;128;255m \x1b[0m",
	)

	g := sbGrid(t, e.Scrollback(0))
	cells := CellsJSON(g)

	type key struct{ r, c int }
	byPos := make(map[key]CellJSON, len(cells))
	for _, c := range cells {
		byPos[key{c.R, c.C}] = c
	}

	if c, ok := byPos[key{0, 0}]; !ok || c.Ch != "r" || c.Fg == nil || *c.Fg != "#cd0000" {
		t.Errorf("cell(0,0) = %+v (present=%v), want red 'r' (#cd0000)", c, ok)
	}
	if c, ok := byPos[key{0, 4}]; !ok || c.Ch != "B" || !c.Bold ||
		c.Fg == nil || *c.Fg != "#ff0000" {
		t.Errorf("cell(0,4) = %+v (present=%v), want bold 'B' (#ff0000)", c, ok)
	}
	// 无样式的空格属于"内容为空",不应出现在 JSON 里(保持 payload 精简)。
	if _, ok := byPos[key{0, 3}]; ok {
		t.Error("unstyled space must be omitted from cells JSON")
	}
	// 只带背景色的空格必须保留:它是界面的一部分(选中条/状态栏底色)。
	if c, ok := byPos[key{1, 0}]; !ok || c.Ch != " " || c.Bg == nil || *c.Bg != "#0080ff" {
		t.Errorf("cell(1,0) = %+v (present=%v), want bg #0080ff", c, ok)
	}

	// JSON 线上字段名同样锁住,避免 CellJSON 的 tag 被无意改掉。
	raw, err := json.Marshal(cells)
	if err != nil {
		t.Fatalf("marshal cells: %v", err)
	}
	for _, want := range []string{`"ch":"B"`, `"bold":true`, `"fg":"#ff0000"`, `"bg":"#0080ff"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("cells JSON missing %s: %s", want, raw)
		}
	}
}

// 超长逻辑行在历史里按屏幕宽度折行:历史本身不会出现超过终端宽度的行
// (x/vt 的 Scrollback.Push 按渲染缓冲的行推送),所以一条 30 列的行会变成
// 三条 10 列的历史行。
func TestScrollbackLongLineWraps(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 10)
	sbHistory(t, e, strings.Repeat("0123456789", 3))

	g := sbGrid(t, e.Scrollback(0))
	if g.Cols() != 10 || g.Rows() != 3 {
		t.Fatalf("grid = %d rows x %d cols, want 3 rows x 10 cols", g.Rows(), g.Cols())
	}
	for r, row := range sbRows(g) {
		if row != "0123456789" {
			t.Errorf("row %d = %q, want %q", r, row, "0123456789")
		}
	}
}

// 屏幕变窄后,已存下的历史行仍保留旧宽度(以及旧样式),物化时必须按当前
// 宽度截断而不是越界或泄漏。截断只是视图投影:再拉宽后原内容应完整回来。
func TestScrollbackLineTruncatedToWidth(t *testing.T) {
	const long = "ABCDEFGHIJKLMNOPQRST" // 20 格
	e := NewEmulatorWithScrollback(20, 2, 10)
	sbHistory(t, e, long)

	if got := sbRows(sbGrid(t, e.Scrollback(0)))[0]; got != long {
		t.Fatalf("stored history row = %q, want %q", got, long)
	}

	e.Resize(10, 2)
	v := e.Scrollback(0)
	if v.Total != 1 {
		t.Fatalf("total after resize = %d, want 1", v.Total)
	}
	g := sbGrid(t, v)
	if g.Cols() != 10 {
		t.Fatalf("grid cols after resize = %d, want 10", g.Cols())
	}
	if got, want := sbRows(g)[0], long[:10]; got != want {
		t.Errorf("truncated row = %q, want %q", got, want)
	}

	// 回到原宽度:历史行没有被改写,前半段之外的内容仍可见。
	e.Resize(20, 2)
	if got := sbRows(sbGrid(t, e.Scrollback(0)))[0]; got != long {
		t.Errorf("history row after widening = %q, want %q (truncation must not mutate history)", got, long)
	}
}

// lines=N 是尾部优先:agent 要的是最近上下文,Total 仍报告裁剪前的总量。
func TestScrollbackTailFirst(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 10)
	sbHistory(t, e, "L0", "L1", "L2", "L3", "L4")

	const all = "L0\nL1\nL2\nL3\nL4"
	full := e.Scrollback(0)
	if full.Total != 5 {
		t.Fatalf("total = %d, want 5", full.Total)
	}
	if got := Text(sbGrid(t, full)); got != all {
		t.Errorf("full text = %q, want %q", got, all)
	}

	v := e.Scrollback(2)
	if v.Total != 5 {
		t.Errorf("total with limit 2 = %d, want 5 (total is before trimming)", v.Total)
	}
	g := sbGrid(t, v)
	if g.Rows() != 2 {
		t.Fatalf("limited grid rows = %d, want 2", g.Rows())
	}
	if got, want := Text(g), "L3\nL4"; got != want {
		t.Errorf("tail-2 text = %q, want %q (most recent lines)", got, want)
	}
	if got, want := Text(sbGrid(t, e.Scrollback(1))), "L4"; got != want {
		t.Errorf("tail-1 text = %q, want %q", got, want)
	}

	// limit <= 0 与超过总量都返回全部;越界不能 panic。
	for _, limit := range []int{0, -1, 5, 99} {
		if got := Text(sbGrid(t, e.Scrollback(limit))); got != all {
			t.Errorf("limit %d text = %q, want full %q", limit, got, all)
		}
	}
}

// 备用屏:全屏程序自己管翻页,主屏历史此刻不代表"用户看到的滚动内容"。
// 因此返回 alternate_screen: true 且不物化历史(Grid 为 nil → API text 为空),
// 但 Total 仍报告主屏保留的条数(scrollback.go 的既定语义,便于诊断)。
func TestScrollbackAlternateScreen(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 10)
	sbHistory(t, e, "main1", "main2")

	write(t, e, "\x1b[?1049h")
	// 备用屏里的输出不能污染主屏历史。
	sbHistory(t, e, "alt1", "alt2", "alt3")

	v := e.Scrollback(0)
	if !v.Alternate {
		t.Fatal("alternate screen not reported")
	}
	if v.Disabled {
		t.Error("scrollback disabled = true, want false")
	}
	if v.Grid != nil {
		t.Errorf("alternate screen materialized a %dx%d grid, want nil (empty text)",
			v.Grid.Rows(), v.Grid.Cols())
	}
	if v.Total != 2 {
		t.Errorf("total in alternate = %d, want retained main-screen history 2", v.Total)
	}

	write(t, e, "\x1b[?1049l")
	back := e.Scrollback(0)
	if back.Alternate {
		t.Fatal("still reported alternate after leaving it")
	}
	if got, want := Text(sbGrid(t, back)), "main1\nmain2"; got != want {
		t.Errorf("history after leaving alternate = %q, want %q", got, want)
	}
}

// --scrollback=0:构造时即禁用,读取明确报告 Disabled(API 的 503 依据),
// 文本/历史都为空;底层缓冲也要压到 1 行,不能白付 x/vt 的 10000 行默认成本。
func TestScrollbackDisabledAtConstruction(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 0)
	if !e.ScrollbackDisabled() {
		t.Error("ScrollbackDisabled() = false, want true")
	}
	if got := e.ScrollbackLines(); got != 0 {
		t.Errorf("ScrollbackLines() = %d, want 0", got)
	}

	// 禁用后照样写输出:历史读取必须始终为空,而不是悄悄留存。
	sbHistory(t, e, "a", "b", "c")
	v := e.Scrollback(0)
	if !v.Disabled {
		t.Error("view disabled = false, want true")
	}
	if v.Grid != nil {
		t.Error("disabled scrollback must not materialize a grid")
	}
	if v.Total != 0 {
		t.Errorf("total = %d, want 0 when disabled", v.Total)
	}
	if n := e.vt.Scrollback().MaxLines(); n != 1 {
		t.Errorf("underlying scrollback max lines = %d, want capped at 1 (no 10000-line default)", n)
	}
}

// 运行期 SetScrollbackLines(0) 等价于 --scrollback=0:清掉已有历史、禁用读取;
// 之后重新打开时从空历史开始,新容量重新生效(旧内容不应复活)。
func TestScrollbackSetDisabledClearsHistory(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 10)
	sbHistory(t, e, "before1", "before2")
	if got := e.Scrollback(0).Total; got != 2 {
		t.Fatalf("initial total = %d, want 2", got)
	}

	e.SetScrollbackLines(0)
	if !e.ScrollbackDisabled() {
		t.Error("after SetScrollbackLines(0): disabled = false, want true")
	}
	if got := e.ScrollbackLines(); got != 0 {
		t.Errorf("after SetScrollbackLines(0): lines = %d, want 0", got)
	}
	if v := e.Scrollback(0); !v.Disabled || v.Grid != nil || v.Total != 0 {
		t.Errorf("disabled view = {disabled:%v grid:%v total:%d}, want empty disabled view",
			v.Disabled, v.Grid != nil, v.Total)
	}

	// 关闭期间继续写入:读取仍为空(不保留历史)。
	sbHistory(t, e, "after1", "after2")
	if v := e.Scrollback(0); !v.Disabled || v.Grid != nil || v.Total != 0 {
		t.Errorf("view after writing while disabled = {disabled:%v grid:%v total:%d}, want empty",
			v.Disabled, v.Grid != nil, v.Total)
	}

	// 重新打开:容量生效,且旧历史已被清除。
	e.SetScrollbackLines(2)
	if e.ScrollbackDisabled() {
		t.Error("re-enabled scrollback still disabled")
	}
	if got := e.ScrollbackLines(); got != 2 {
		t.Errorf("re-enabled lines = %d, want 2", got)
	}
	sbHistory(t, e, "x", "y", "z")
	v := e.Scrollback(0)
	if v.Total != 2 {
		t.Errorf("total after re-enable = %d, want 2", v.Total)
	}
	got := Text(sbGrid(t, v))
	if want := "y\nz"; got != want {
		t.Errorf("history after re-enable = %q, want %q (capacity 2, newest lines)", got, want)
	}
	if strings.Contains(got, "before") {
		t.Errorf("cleared history resurrected after re-enable: %q", got)
	}
}

// 容量上限:超过上限后最旧的行走一路被丢弃,Total 永远不超过容量;
// 运行期缩小容量要立刻裁掉最旧的行。
func TestScrollbackCapacityLimit(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 3)
	if got := e.ScrollbackLines(); got != 3 {
		t.Fatalf("ScrollbackLines() = %d, want 3", got)
	}

	sbHistory(t, e, "L0", "L1", "L2", "L3", "L4")
	v := e.Scrollback(0)
	if v.Total != 3 {
		t.Fatalf("total = %d, want capacity 3", v.Total)
	}
	if got, want := Text(sbGrid(t, v)), "L2\nL3\nL4"; got != want {
		t.Errorf("history = %q, want %q (oldest dropped)", got, want)
	}

	// 再写一行:窗口继续前移,容量不变。
	sbHistory(t, e, "L5")
	if v := e.Scrollback(0); v.Total != 3 {
		t.Errorf("total after more output = %d, want 3", v.Total)
	}
	if got, want := Text(sbGrid(t, e.Scrollback(0))), "L3\nL4\nL5"; got != want {
		t.Errorf("history after more output = %q, want %q", got, want)
	}

	// 运行期缩小容量:SetScrollbackSize 会裁掉最旧的行。
	e.SetScrollbackLines(2)
	v = e.Scrollback(0)
	if v.Total != 2 {
		t.Fatalf("total after shrink = %d, want 2", v.Total)
	}
	if got, want := Text(sbGrid(t, v)), "L4\nL5"; got != want {
		t.Errorf("history after shrink = %q, want %q", got, want)
	}
}

// TestScrollbackWideCharAtLastColumn 覆盖宽字符恰好落在当前屏最后一列的边界:
// 续列无处安放,整格必须留空,行宽不能超出 Grid.Cols()。
//
// 回归的是 scrollbackLineToCells 的旧写法:它只在 "x+1 < cols" 时补续列,
// 于是前导格照写、续列落在网格外 —— Text/HTML 比 Grid.Cols() 宽一格,PNG 按
// runewidth 画两格并越过画布右缘被裁。
func TestScrollbackWideCharAtLastColumn(t *testing.T) {
	e := NewEmulatorWithScrollback(10, 2, 10)
	// 8 个 ASCII + 1 个宽字符 = 9 显示列,在 10 列屏上完整可见。
	sbHistory(t, e, "12345678中")

	e.Resize(9, 2)
	g := sbGrid(t, e.Scrollback(0))
	if g.Cols() != 9 {
		t.Fatalf("grid cols = %d, want 9", g.Cols())
	}

	if got, want := RowText(g, 0), "12345678"; got != want {
		t.Errorf("row = %q, want %q (wide char has no continuation column)", got, want)
	}
}
