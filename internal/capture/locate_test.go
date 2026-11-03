package capture

import (
	"fmt"
	"regexp"
	"testing"
)

// helpers ----------------------------------------------------------------

// mxGrid 建一个 cols×rows 的屏幕并写入 s,返回**快照**网格:API 的语义定位走的是
// Snapshot().Grid() 这条深拷贝路径,测试跟着走才算覆盖真实输入。
func mxGrid(t *testing.T, cols, rows int, s string) *Grid {
	t.Helper()
	e := NewEmulator(cols, rows)
	write(t, e, s)
	return e.Snapshot().Grid()
}

// mxAssertLocate 统一断言 (row, col, ok) 三元组;未命中时 row/col 固定为 0,
// 不做无意义的坐标断言。
func mxAssertLocate(t *testing.T, what string, row, col int, ok bool, wantRow, wantCol int, wantOK bool) {
	t.Helper()
	if ok != wantOK || (ok && (row != wantRow || col != wantCol)) {
		t.Errorf("%s = (%d,%d,%v), want (%d,%d,%v)", what, row, col, ok, wantRow, wantCol, wantOK)
	}
}

// mxLocateText 是 LocateText 的断言包装,让表驱动用例只写四元组。
func mxLocateText(t *testing.T, g *Grid, needle string, index, wantRow, wantCol int, wantOK bool) {
	t.Helper()
	row, col, ok := g.LocateText(needle, index)
	mxAssertLocate(t, fmt.Sprintf("LocateText(%q, %d)", needle, index), row, col, ok, wantRow, wantCol, wantOK)
}

// mxLocateRegex 是 LocateRegex 的断言包装。
func mxLocateRegex(t *testing.T, g *Grid, re *regexp.Regexp, index, wantRow, wantCol int, wantOK bool) {
	t.Helper()
	pattern := "<nil>"
	if re != nil {
		pattern = re.String()
	}
	row, col, ok := g.LocateRegex(re, index)
	mxAssertLocate(t, fmt.Sprintf("LocateRegex(%q, %d)", pattern, index), row, col, ok, wantRow, wantCol, wantOK)
}

// ---------------------------------------------------------------------------
// 中心格与列号

func TestLocateTextCenterCell(t *testing.T) {
	// 定位返回匹配跨度(首格..末格)的中心格(§2.3):偶数长度落在左中格,
	// 奇数长度落在正中格。这里按列号逐格核对,防止"中心"被算成 UTF-8 字节中点。
	g := mxGrid(t, 30, 4, "Total: 42\r\nfoo bar")
	mxLocateText(t, g, "Total", 0, 0, 2, true) // 0..4 → 2
	mxLocateText(t, g, "42", 0, 0, 7, true)    // 7..8 → 7(左中)
	mxLocateText(t, g, "bar", 0, 1, 5, true)   // 4..6 → 5
	mxLocateText(t, g, "T", 0, 0, 0, true)     // 单格就是自身

	// 用 CUP 精确定位一列:结果必须跟着网格列走,而不是跟着"第几个字符"走。
	g = mxGrid(t, 20, 3, "\x1b[2;5HOK") // row1, col4..5
	mxLocateText(t, g, "OK", 0, 1, 4, true)
}

// ---------------------------------------------------------------------------
// 重复匹配 + match_index

func TestLocateTextMatchIndex(t *testing.T) {
	// match_index 按"从左到右、从上到下"的出现顺序计数(§2.3):同一行的多个
	// 匹配先数完,才轮到下一行。
	g := mxGrid(t, 30, 4, "\x1b[1;1HOK OK OK") // OK 于 col0、col3、col6
	mxLocateText(t, g, "OK", 0, 0, 0, true)
	mxLocateText(t, g, "OK", 1, 0, 3, true)
	mxLocateText(t, g, "OK", 2, 0, 6, true)
	mxLocateText(t, g, "OK", 3, 0, 0, false) // 第三个之后没有了

	// 跨行:row0 有一个匹配,row1 在 "xx OK" 的 col3,row2 又有一个。
	g = mxGrid(t, 30, 5, "OK\r\nxx OK\r\nOK")
	mxLocateText(t, g, "OK", 0, 0, 0, true)
	mxLocateText(t, g, "OK", 1, 1, 3, true)
	mxLocateText(t, g, "OK", 2, 2, 0, true)

	// 负索引按 0 处理(LocateRegex 的文档语义),而不是报错或从行尾倒着找。
	mxLocateText(t, g, "OK", -3, 0, 0, true)

	// 越界:超过匹配总数的索引返回未命中(API 映射为 404),不能回绕到第一个。
	mxLocateText(t, g, "OK", 3, 0, 0, false)
	mxLocateText(t, g, "OK", 100, 0, 0, false)

	// 空 needle 直接未命中,避免返回 (0,0) 让人误以为点到了左上角。
	mxLocateText(t, g, "", 0, 0, 0, false)
}

// ---------------------------------------------------------------------------
// 宽字符取左格

func TestLocateTextWideChar(t *testing.T) {
	// 宽字符占两格:续格的 Rune==0,不是可点击字符。定位必须落在**左格**(§2.3),
	// 否则点击坐标会偏一列,控件可能完全没反应。
	g := mxGrid(t, 20, 2, "\x1b[1;5H中") // 中占 col4..5
	if cell := g.Cell(0, 5); cell.Rune != 0 {
		t.Fatalf("expected continuation cell at (0,5), got rune %q", cell.Rune)
	}
	mxLocateText(t, g, "中", 0, 0, 4, true) // 左格 4,不是续格 5

	// 宽字符夹在 ASCII 之间:前后字符的列号必须跳过续格。
	g = mxGrid(t, 20, 2, "\x1b[1;3HA中B") // A=2, 中=3..4, B=5
	mxLocateText(t, g, "A", 0, 0, 2, true)
	mxLocateText(t, g, "中", 0, 0, 3, true)
	mxLocateText(t, g, "B", 0, 0, 5, true)

	// 连续宽字符:第二个宽字符的列号是 2 而不是 1(续格不参与 rune 编号)。
	g = mxGrid(t, 20, 2, "中中X") // 中=0..1, 中=2..3, X=4
	mxLocateText(t, g, "X", 0, 0, 4, true)
	mxLocateText(t, g, "中", 1, 0, 2, true)
}

// ---------------------------------------------------------------------------
// 未命中与字面量语义

func TestLocateTextMiss(t *testing.T) {
	g := mxGrid(t, 10, 3, "hello\r\nworld")
	// 未命中:ok=false(API 映射 404);此时 row/col 不参与判断。
	mxLocateText(t, g, "nope", 0, 0, 0, false)
	// 字面量匹配区分大小写:"Hello" 不存在。
	mxLocateText(t, g, "Hello", 0, 0, 0, false)

	// 匹配不跨行:两行残片拼成的针脚必须找不到(列号算术按行才有意义)。
	g = mxGrid(t, 10, 3, "AB\r\nCD")
	mxLocateText(t, g, "BC", 0, 0, 0, false)
	mxLocateText(t, g, "AB", 0, 0, 0, true)

	// --on-text 是字面量(QuoteMeta):"a.c" 只匹配字面量,不会把 '.' 当通配符。
	g = mxGrid(t, 20, 2, "a.c abc")
	mxLocateText(t, g, "a.c", 0, 0, 1, true)  // 0..2 → 中心 1
	mxLocateText(t, g, "a.c", 1, 0, 0, false) // 只有字面量那一处
}

// ---------------------------------------------------------------------------
// --on-regex 语义

func TestLocateRegexSemantics(t *testing.T) {
	// --on-regex 与 --on-text 走同一条网格扫描,只是匹配器换成调用方编译的正则
	// (§2.3);因此中心格、跨行边界、index 计数的规则必须完全一致。
	g := mxGrid(t, 30, 3, "item 42 done")
	mxLocateRegex(t, g, regexp.MustCompile(`[0-9]+`), 0, 0, 5, true)   // 5..6 → 左中 5
	mxLocateRegex(t, g, regexp.MustCompile(`\bdone\b`), 0, 0, 9, true) // 8..11 → 中心 9

	// 交替选择 + match_index:第一个匹配是 OK,第二个是 Cancel。
	g = mxGrid(t, 30, 2, "OK Cancel")
	re := regexp.MustCompile(`OK|Cancel`)
	mxLocateRegex(t, g, re, 0, 0, 0, true) // 0..1 → 0
	mxLocateRegex(t, g, re, 1, 0, 5, true) // Cancel 3..8 → 5
	mxLocateRegex(t, g, re, 2, 0, 0, false)

	// 正则元字符与大小写不敏感:同一屏里 a.c 会让 "abc" 也命中(LocateText 不会)。
	g = mxGrid(t, 20, 2, "a.c abc")
	mxLocateRegex(t, g, regexp.MustCompile(`a.c`), 0, 0, 1, true)
	mxLocateRegex(t, g, regexp.MustCompile(`a.c`), 1, 0, 5, true) // "abc" 4..6 → 5
	mxLocateRegex(t, g, regexp.MustCompile(`(?i)ABC`), 0, 0, 5, true)

	// 零长度匹配必须跳过:否则 `a*` 会用空匹配占满 index,甚至返回一个不可点的格。
	g = mxGrid(t, 20, 2, "banana")
	mxLocateRegex(t, g, regexp.MustCompile(`a*`), 0, 0, 1, true)  // 第一个非空匹配是 col1 的 a
	mxLocateRegex(t, g, regexp.MustCompile(`x*`), 0, 0, 0, false) // 全是空匹配 → 未命中

	// nil 正则按未命中处理(调用方没给 target 时的防御),不能 panic。
	mxLocateRegex(t, g, nil, 0, 0, 0, false)

	// 逐行扫描:^ 锚定的是行首,跨行拼不出匹配。
	g = mxGrid(t, 10, 3, "AB\r\nCD")
	mxLocateRegex(t, g, regexp.MustCompile(`^AB`), 0, 0, 0, true)
	mxLocateRegex(t, g, regexp.MustCompile(`^CD`), 0, 1, 0, true)
	mxLocateRegex(t, g, regexp.MustCompile(`B.C`), 0, 0, 0, false)

	// 负索引同 LocateText,归一为 0。
	mxLocateRegex(t, g, regexp.MustCompile(`AB`), -2, 0, 0, true)
}

// ---------------------------------------------------------------------------
// §2.3 验收:--on-text / --on-regex 的四种语义

func TestOnTextSemanticTargeting(t *testing.T) {
	// 端到端模拟 resolveMouseTarget(agent_handler.go):在可见屏快照上按
	// --on-text/--on-regex + match_index 求中心格,覆盖"重复匹配、宽字符、
	// 未命中、越界"四条验收。
	g := mxGrid(t, 30, 5,
		"\x1b[1;3HOK\x1b[1;10HOK"+ // 第 1 行两个 OK:col2 与 col9
			"\x1b[2;5H中"+ // 第 2 行宽字符标签 col4
			"\x1b[3;1HCancel") // 第 3 行 Cancel:col0..5

	t.Run("repeated match by index", func(t *testing.T) {
		mxLocateText(t, g, "OK", 0, 0, 2, true)
		mxLocateText(t, g, "OK", 1, 0, 9, true)
	})

	t.Run("wide char takes lead cell", func(t *testing.T) {
		mxLocateText(t, g, "中", 0, 1, 4, true)
	})

	t.Run("miss", func(t *testing.T) {
		// 未命中时 API 回 404;定位层只负责 ok=false。
		mxLocateText(t, g, "Nope", 0, 0, 0, false)
	})

	t.Run("match index out of range", func(t *testing.T) {
		// 只有两个 OK,index=2 视为越界(同样是 404,而不是夹到最后一个)。
		mxLocateText(t, g, "OK", 2, 0, 0, false)
	})

	t.Run("on regex scans rows in order", func(t *testing.T) {
		re := regexp.MustCompile(`OK|Cancel`)
		mxLocateRegex(t, g, re, 0, 0, 2, true) // 第一个 OK
		mxLocateRegex(t, g, re, 1, 0, 9, true) // 第二个 OK
		mxLocateRegex(t, g, re, 2, 2, 2, true) // Cancel 在第 3 行,中心 (0+5)/2=2
	})
}

// TestLocateTextWideSpanCenterClamp 回归:匹配跨度横跨宽字符时,算术中心格会
// 落在续格(Rune==0)上。续格不是可点击单元,坐标必须回退到前导格,否则点击
// 会偏一列,控件可能完全没反应(§2.3 "跨宽字符时取其左格")。
func TestLocateTextWideSpanCenterClamp(t *testing.T) {
	g := mxGrid(t, 20, 2, "中中X") // 中=col0..1, 中=col2..3, X=col4

	// "中中" 跨 col0..3:算术中心是 1(第一个"中"的续格)→ 必须回到 0。
	mxLocateText(t, g, "中中", 0, 0, 0, true)

	// "中X" 跨 col2..4:算术中心是 3(第二个"中"的续格)→ 必须回到 2。
	mxLocateText(t, g, "中X", 0, 0, 2, true)
}
