package capture

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
)

func TestTextTrims(t *testing.T) {
	// 行内尾部空格与尾部空行被裁剪
	e := NewEmulator(10, 4)
	write(t, e, "abc   \r\n\r\n  d")
	assertText(t, e, "abc\n\n  d")
}

func TestTextWideCharWidth(t *testing.T) {
	// 宽字符占位格不产生额外宽度
	e := NewEmulator(10, 3)
	write(t, e, "a中b")
	got := Text(e.Screen())
	if got != "a中b" {
		t.Errorf("text = %q, want %q", got, "a中b")
	}
	// 渲染文本的显示宽度 = 4(a/中/中/b 占 1+2+1 格),占位格不引入空白
	width := 0
	for _, rn := range []rune(got) {
		width += runewidth.RuneWidth(rn)
	}
	if width != 4 {
		t.Errorf("display width = %d, want 4", width)
	}
}

func TestHTMLBasic(t *testing.T) {
	e := NewEmulator(10, 2)
	write(t, e, "\x1b[31mR\x1b[0m&<")
	got := HTML(e.Screen())
	for _, want := range []string{
		`<pre class="gotty-capture">`,
		`<span style="color:#cd0000">R</span>`,
		"&amp;&lt;", // 转义
		"</pre>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q in:\n%s", want, got)
		}
	}
}

func TestHTML256AndRGBColors(t *testing.T) {
	e := NewEmulator(20, 3)
	write(t, e, "\x1b[38;5;196mR")   // 纯红立方体色
	write(t, e, "\x1b[38;2;1;2;3mG") // RGB
	write(t, e, "\x1b[48;5;232m ")   // 灰度背景
	got := HTML(e.Screen())
	for _, want := range []string{
		`color:#ff0000`,
		`color:#010203`,
		`background-color:#080808`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}

func TestHTMLReverseSwapsColors(t *testing.T) {
	e := NewEmulator(20, 2)
	write(t, e, "\x1b[7;31;47mX") // reverse + 红字/白底
	got := HTML(e.Screen())
	if !strings.Contains(got, `color:#e5e5e5`) || !strings.Contains(got, `background-color:#cd0000`) {
		t.Errorf("reverse swap not applied:\n%s", got)
	}
}

func TestDocumentJSON(t *testing.T) {
	e := NewEmulator(12, 4)
	write(t, e, "\x1b[31mhi")
	row, col := e.Cursor()
	code := 0
	doc := NewDocument(e, "printf", []string{"hi"}, &code, false, 1500*time.Millisecond, StopExit,
		nil, 9, 18, true)

	if doc.Engine != "native" || doc.Cols != 12 || doc.Rows != 4 {
		t.Errorf("doc meta wrong: %+v", doc)
	}
	if doc.Text != "hi" {
		t.Errorf("doc text = %q", doc.Text)
	}
	if doc.Cursor.Row != row || doc.Cursor.Col != col {
		t.Errorf("cursor wrong: %+v", doc.Cursor)
	}
	if doc.DurationMS != 1500 {
		t.Errorf("duration = %d", doc.DurationMS)
	}
	if len(doc.Cells) != 2 {
		t.Errorf("cells len = %d, want 2 (%+v)", len(doc.Cells), doc.Cells)
	}
	if doc.Cells[0].Ch != "h" || doc.Cells[0].Fg == nil || *doc.Cells[0].Fg != "#cd0000" {
		t.Errorf("cell0 = %+v", doc.Cells[0])
	}

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"text":"hi"`) {
		t.Errorf("json missing text: %s", b)
	}
}

func TestDocumentWithoutCells(t *testing.T) {
	e := NewEmulator(8, 2)
	write(t, e, "x")
	doc := NewDocument(e, "x", nil, nil, true, 10*time.Millisecond, StopTimeout,
		nil, 9, 18, false)
	if doc.Cells != nil {
		t.Errorf("cells should be omitted: %+v", doc.Cells)
	}
	if !doc.TimedOut {
		t.Error("timed_out not carried")
	}
}

func TestANSI256ColorMath(t *testing.T) {
	cases := []struct {
		idx  int
		want string
	}{
		{0, "#000000"},
		{9, "#ff0000"},
		{16, "#000000"},  // cube[0,0,0]
		{46, "#00ff00"},  // cube[0,5,0]
		{196, "#ff0000"}, // cube[5,0,0]
		{231, "#ffffff"}, // cube[5,5,5]
		{232, "#080808"},
		{255, "#eeeeee"},
	}
	for _, tc := range cases {
		if got := ansi256CSS(tc.idx); got != tc.want {
			t.Errorf("ansi256CSS(%d) = %s, want %s", tc.idx, got, tc.want)
		}
	}
}

func TestCSSColorDefault(t *testing.T) {
	if cssColor(DefaultColor()) != "" {
		t.Error("default color should map to empty css")
	}
}
