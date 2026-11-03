package capture

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

// helpers ----------------------------------------------------------------

// mxEmulator 造一个镜像仿真器,并按参数写入 DECSET 序列(如 "\x1b[?1000h")。
// 鼠标用例都需要"应用已经启用了某个模式"这个前置条件,集中在一个 helper
// 里可以避免每个用例重复拼转义序列,也保证模式注入的写法一致。
func mxEmulator(t *testing.T, cols, rows int, decset ...string) *Emulator {
	t.Helper()
	e := NewEmulator(cols, rows)
	for _, seq := range decset {
		write(t, e, seq)
	}
	return e
}

// mxEncode 编码一个"应当成功"的事件;失败直接终止用例(此时继续断言字节没有意义)。
func mxEncode(t *testing.T, e *Emulator, ev MouseEvent) []byte {
	t.Helper()
	got, err := e.EncodeMouse(ev)
	if err != nil {
		t.Fatalf("EncodeMouse(%+v): unexpected error: %v", ev, err)
	}
	return got
}

// mxAssertBytes 逐字节比较。鼠标协议的错误常常只差一个偏移位或一个 final
// 字节,同时打出 %q 和十六进制才能一眼看出是哪一字节错。
func mxAssertBytes(t *testing.T, what string, got, want []byte) {
	t.Helper()
	if bytes.Equal(got, want) {
		return
	}
	t.Errorf("%s = %q (hex % x), want %q (hex % x)", what, got, got, want, want)
}

// mxX10 用上游编码器拼出期望的 X10 字节,用于和自家 EncodeMouse 对拍。
// 逐字节断言锁定格式,对拍锁定位域没有被手抄错。
func mxX10(button uv.MouseButton, motion, shift, alt, ctrl bool, col, row int) []byte {
	b := ansi.EncodeMouseButton(button, motion, shift, alt, ctrl)
	return []byte(ansi.MouseX10(b, col, row))
}

// mxSgr 是 mxX10 的 SGR 版本;release 决定 final 是 'm' 还是 'M'。
func mxSgr(button uv.MouseButton, motion, release, shift, alt, ctrl bool, col, row int) []byte {
	b := ansi.EncodeMouseButton(button, motion, shift, alt, ctrl)
	return []byte(ansi.MouseSgr(b, col, row, release))
}

// mxVtMouseBytes 把 DECSET 模式喂给裸 x/vt 仿真器,再用上游 SendMouse 编码一组
// 事件,逐次读回它写入的字节。SendMouse 写的是无缓冲 io.Pipe:一次 SendMouse
// 对应一次 Write,而 io.Pipe 的一次 Write 只会被一次 Read 取走,所以"读者先起、
// 按事件数读"既不会死锁,也不需要 Close —— x/vt 的 Close 与 Read 之间有一个
// 未同步的 closed 标志,并发调用会被 -race 抓到,测试里刻意避开。
func mxVtMouseBytes(t *testing.T, modes string, events ...uv.MouseEvent) []byte {
	t.Helper()
	term := vt.NewEmulator(80, 24)
	if modes != "" {
		if _, err := term.Write([]byte(modes)); err != nil {
			t.Fatalf("vt.Write(%q): %v", modes, err)
		}
	}

	read := make(chan []byte, 1)
	go func() {
		var out []byte
		buf := make([]byte, 64)
		for range events {
			n, err := term.Read(buf)
			out = append(out, buf[:n]...)
			if err != nil {
				break
			}
		}
		read <- out
	}()
	for _, ev := range events {
		term.SendMouse(ev)
	}

	select {
	case got := <-read:
		return got
	case <-time.After(2 * time.Second):
		// 读不到只可能是"一次 SendMouse 一次写"的假设被上游改掉,给出明确
		// 失败而不是把测试挂住。
		t.Fatal("timed out reading vt.SendMouse output")
		return nil
	}
}

// mxVtMouseSilent 断言上游 SendMouse 在给定模式下"一个字节都不写":io.Pipe
// 是无缓冲的,真有写入时 SendMouse 会因为没有读者而永远阻塞,所以"能及时返回"
// 本身就是零字节的证明。
func mxVtMouseSilent(t *testing.T, modes string, events ...uv.MouseEvent) {
	t.Helper()
	term := vt.NewEmulator(80, 24)
	if modes != "" {
		if _, err := term.Write([]byte(modes)); err != nil {
			t.Fatalf("vt.Write(%q): %v", modes, err)
		}
	}
	done := make(chan struct{})
	go func() {
		for _, ev := range events {
			term.SendMouse(ev)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("vt.SendMouse blocked (i.e. wrote bytes) although no mouse reporting mode is enabled")
	}
}

// ---------------------------------------------------------------------------
// §2.2 X10 编码:逐字节断言

func TestMouseX10Bytes(t *testing.T) {
	// X10 是 CSI M Cb Cx Cy:坐标 0-based +33(1-based +32),没有 final 字节区分
	// 按下/抬起,抬起只能靠 Cb=3。这里把两条完整序列写成字面量,任何偏移错位
	// (例如少加 1 或把修饰位算进坐标)都会立刻失败。
	t.Run("click", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1000h")
		got := mxEncode(t, e, MouseEvent{Action: MouseClickAction, Button: "left", Col: 0, Row: 0})
		// 按下: Cb=0+32=' ', Cx=0+33='!', Cy=0+33='!'
		// 抬起: Cb=3+32='#', 坐标不变
		mxAssertBytes(t, "click left at (0,0)", got, []byte("\x1b[M !!\x1b[M#!!"))
		want := append(mxX10(uv.MouseLeft, false, false, false, false, 0, 0),
			mxX10(uv.MouseNone, false, false, false, false, 0, 0)...)
		mxAssertBytes(t, "click left at (0,0) vs ansi.MouseX10", got, want)
	})

	t.Run("coordinates", func(t *testing.T) {
		// 非零坐标单独锁一次:偏移量必须作用在两个坐标字节上,而不是只作用一个。
		e := mxEmulator(t, 80, 24, "\x1b[?1000h")
		got := mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "left", Col: 5, Row: 10})
		mxAssertBytes(t, "down at (5,10)", got, []byte("\x1b[M &+")) // Cx=38 '&', Cy=43 '+'
		mxAssertBytes(t, "down at (5,10) vs ansi.MouseX10", got,
			mxX10(uv.MouseLeft, false, false, false, false, 5, 10))

		got = mxEncode(t, e, MouseEvent{
			Action: MouseDownAction, Button: "left", Col: 5, Row: 10,
			Mods: MouseMods{Shift: true, Alt: true, Ctrl: true},
		})
		mxAssertBytes(t, "down at (5,10) all mods", got, []byte("\x1b[M<&+")) // Cb=28+32=60 '<'
	})

	t.Run("modifier bits", func(t *testing.T) {
		// Cb 的 4/8/16 位分别是 Shift/Alt/Ctrl(§2.2 的位表)。八种组合逐位锁死,
		// 这样把 Alt 错放到 4 或漏掉 Ctrl 都会直接定位到具体组合。
		cases := []struct {
			name string
			mods MouseMods
			cb   byte
		}{
			{"none", MouseMods{}, 0},
			{"shift", MouseMods{Shift: true}, 4},
			{"alt", MouseMods{Alt: true}, 8},
			{"ctrl", MouseMods{Ctrl: true}, 16},
			{"shift+alt", MouseMods{Shift: true, Alt: true}, 12},
			{"shift+ctrl", MouseMods{Shift: true, Ctrl: true}, 20},
			{"alt+ctrl", MouseMods{Alt: true, Ctrl: true}, 24},
			{"shift+alt+ctrl", MouseMods{Shift: true, Alt: true, Ctrl: true}, 28},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := mxEmulator(t, 80, 24, "\x1b[?1000h")
				ev := MouseEvent{Action: MouseDownAction, Button: "left", Col: 2, Row: 3, Mods: tc.mods}
				got := mxEncode(t, e, ev)
				want := []byte{0x1b, '[', 'M', 32 + tc.cb, '#', '$'}
				mxAssertBytes(t, "down with "+tc.name, got, want)
				mxAssertBytes(t, "down with "+tc.name+" vs ansi.MouseX10", got,
					mxX10(uv.MouseLeft, false, tc.mods.Shift, tc.mods.Alt, tc.mods.Ctrl, 2, 3))
			})
		}
	})

	t.Run("clicks", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1000h")
		// 多击 = 同一坐标连续 N 组按下/抬起(§2.2),所以序列长度严格是单次的 N 倍。
		press := mxX10(uv.MouseLeft, false, false, false, false, 1, 1)
		release := mxX10(uv.MouseNone, false, false, false, false, 1, 1)
		pair := append(append([]byte{}, press...), release...)
		want := bytes.Repeat(pair, 3)

		got := mxEncode(t, e, MouseEvent{Action: MouseClickAction, Button: "left", Col: 1, Row: 1, Clicks: 3})
		mxAssertBytes(t, "triple click", got, want)
		if n := bytes.Count(got, []byte("\x1b[M")); n != 6 {
			t.Errorf("triple click emitted %d X10 sequences, want 6 (3 press + 3 release)", n)
		}
		// 连击结束不留下"按住"状态:抬起是每次按下都配对的。
		if st := e.MouseState(); len(st.Held) != 0 {
			t.Errorf("held after triple click = %v, want empty", st.Held)
		}

		// Clicks < 1 归一为 1;若返回零字节,上层会误报"发送成功但什么都没发生"。
		got = mxEncode(t, e, MouseEvent{Action: MouseClickAction, Button: "left", Col: 1, Row: 1})
		mxAssertBytes(t, "click with Clicks=0", got, pair)
	})

	t.Run("drag", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1000h")
		// 先按下左键,再发不带 button 的 drag:必须沿用按住中的按钮,并沿路径
		// 逐格发 motion(Cb=0|32=64,即 '@'),否则拖动型控件只看到两个端点。
		mxAssertBytes(t, "down before drag",
			mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "left", Col: 1, Row: 1}),
			[]byte("\x1b[M \"\""))

		got := mxEncode(t, e, MouseEvent{Action: MouseDragAction, Col: 1, Row: 1, ToCol: 4, ToRow: 2})
		mxAssertBytes(t, "drag (1,1)->(4,2)", got,
			[]byte("\x1b[M@\"\"\x1b[M@#\"\x1b[M@$\"\x1b[M@%#"))
		var want []byte
		for _, p := range interpolate(1, 1, 4, 2) {
			want = append(want, mxX10(uv.MouseLeft, true, false, false, false, p.col, p.row)...)
		}
		mxAssertBytes(t, "drag (1,1)->(4,2) vs ansi.MouseX10", got, want)
		// 拖动期间按钮保持按住。
		if st := e.MouseState(); !slices.Equal(st.Held, []string{"left"}) {
			t.Errorf("held during drag = %v, want [left]", st.Held)
		}
	})

	t.Run("move", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1003h")
		// 无按键移动:Cb = 3(无按键) | 32(motion) = 35('C')。
		got := mxEncode(t, e, MouseEvent{Action: MouseMoveAction, Col: 2, Row: 1})
		mxAssertBytes(t, "move to (2,1)", got, []byte("\x1b[MC#\""))
		mxAssertBytes(t, "move vs ansi.MouseX10", got,
			mxX10(uv.MouseNone, true, false, false, false, 2, 1))
	})

	t.Run("scroll", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1000h")
		// 滚轮在 X10 里由 Cb 的 wheel 位表示(wheelup=64, wheeldown=65),Amount
		// 就是同一序列的重复次数。
		got := mxEncode(t, e, MouseEvent{Action: MouseScrollAction, Button: "wheelup", Col: 0, Row: 0, Amount: 2})
		mxAssertBytes(t, "wheelup x2", got, []byte("\x1b[M`!!\x1b[M`!!"))
		got = mxEncode(t, e, MouseEvent{Action: MouseScrollAction, Button: "wheeldown", Col: 0, Row: 0})
		mxAssertBytes(t, "wheeldown", got, []byte("\x1b[Ma!!"))
		mxAssertBytes(t, "wheeldown vs ansi.MouseX10", got,
			mxX10(uv.MouseWheelDown, false, false, false, false, 0, 0))
	})
}

// ---------------------------------------------------------------------------
// §2.2 SGR 编码:逐字节断言

func TestMouseSgrBytes(t *testing.T) {
	// SGR 是 CSI < Cb ; Cx ; Cy M(1-based,抬起用 'm');按键/修饰位与 X10 相同,
	// 只是十进制文本而不是 +32 单字节。
	t.Run("click", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1000h", "\x1b[?1006h")
		got := mxEncode(t, e, MouseEvent{Action: MouseClickAction, Button: "left", Col: 0, Row: 0})
		mxAssertBytes(t, "click left at (0,0)", got, []byte("\x1b[<0;1;1M\x1b[<3;1;1m"))
		want := append(mxSgr(uv.MouseLeft, false, false, false, false, false, 0, 0),
			mxSgr(uv.MouseNone, false, true, false, false, false, 0, 0)...)
		mxAssertBytes(t, "click vs ansi.MouseSgr", got, want)
	})

	t.Run("coordinates and modifiers", func(t *testing.T) {
		// 1-based 坐标与修饰位一起锁:按下 Cb=28,抬起 Cb=3|28=31(抬起也带修饰位,
		// 与上游 SendMouse 对 MouseReleaseEvent 的处理一致)。
		e := mxEmulator(t, 80, 24, "\x1b[?1000h", "\x1b[?1006h")
		got := mxEncode(t, e, MouseEvent{
			Action: MouseClickAction, Button: "left", Col: 5, Row: 10,
			Mods: MouseMods{Shift: true, Alt: true, Ctrl: true},
		})
		mxAssertBytes(t, "click at (5,10) all mods", got, []byte("\x1b[<28;6;11M\x1b[<31;6;11m"))
	})

	t.Run("modifier bits", func(t *testing.T) {
		cases := []struct {
			name string
			mods MouseMods
			want string
		}{
			{"none", MouseMods{}, "\x1b[<0;3;4M"},
			{"shift", MouseMods{Shift: true}, "\x1b[<4;3;4M"},
			{"alt", MouseMods{Alt: true}, "\x1b[<8;3;4M"},
			{"ctrl", MouseMods{Ctrl: true}, "\x1b[<16;3;4M"},
			{"shift+alt", MouseMods{Shift: true, Alt: true}, "\x1b[<12;3;4M"},
			{"shift+ctrl", MouseMods{Shift: true, Ctrl: true}, "\x1b[<20;3;4M"},
			{"alt+ctrl", MouseMods{Alt: true, Ctrl: true}, "\x1b[<24;3;4M"},
			{"shift+alt+ctrl", MouseMods{Shift: true, Alt: true, Ctrl: true}, "\x1b[<28;3;4M"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				e := mxEmulator(t, 80, 24, "\x1b[?1000h", "\x1b[?1006h")
				got := mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "left", Col: 2, Row: 3, Mods: tc.mods})
				mxAssertBytes(t, "down with "+tc.name, got, []byte(tc.want))
				mxAssertBytes(t, "down with "+tc.name+" vs ansi.MouseSgr", got,
					mxSgr(uv.MouseLeft, false, false, tc.mods.Shift, tc.mods.Alt, tc.mods.Ctrl, 2, 3))
			})
		}
	})

	t.Run("clicks", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1000h", "\x1b[?1006h")
		pair := []byte("\x1b[<0;2;2M\x1b[<3;2;2m")
		got := mxEncode(t, e, MouseEvent{Action: MouseClickAction, Button: "left", Col: 1, Row: 1, Clicks: 2})
		mxAssertBytes(t, "double click", got, bytes.Repeat(pair, 2))
	})

	t.Run("drag", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1002h", "\x1b[?1006h")
		// 按住拖动的 motion 位是 32,SGR 下 Cb=32;三个点对应 (1,1)->(3,2) 的插值。
		mxAssertBytes(t, "down before drag",
			mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "left", Col: 1, Row: 1}),
			[]byte("\x1b[<0;2;2M"))
		got := mxEncode(t, e, MouseEvent{Action: MouseDragAction, Col: 1, Row: 1, ToCol: 3, ToRow: 2})
		mxAssertBytes(t, "drag (1,1)->(3,2)", got,
			[]byte("\x1b[<32;2;2M\x1b[<32;3;2M\x1b[<32;4;3M"))

		// 裸 up(不带 button)释放按住中的左键,抬起字节固定 Cb=3。
		got = mxEncode(t, e, MouseEvent{Action: MouseUpAction, Col: 3, Row: 2})
		mxAssertBytes(t, "bare up releases held button", got, []byte("\x1b[<3;4;3m"))
		if st := e.MouseState(); len(st.Held) != 0 {
			t.Errorf("held after up = %v, want empty", st.Held)
		}
	})

	t.Run("move", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1003h", "\x1b[?1006h")
		got := mxEncode(t, e, MouseEvent{Action: MouseMoveAction, Col: 4, Row: 5})
		mxAssertBytes(t, "move to (4,5)", got, []byte("\x1b[<35;5;6M"))
		mxAssertBytes(t, "move vs ansi.MouseSgr", got,
			mxSgr(uv.MouseNone, true, false, false, false, false, 4, 5))
	})

	t.Run("scroll", func(t *testing.T) {
		e := mxEmulator(t, 80, 24, "\x1b[?1000h", "\x1b[?1006h")
		got := mxEncode(t, e, MouseEvent{Action: MouseScrollAction, Button: "wheelup", Col: 0, Row: 0, Amount: 2})
		mxAssertBytes(t, "wheelup x2", got, []byte("\x1b[<64;1;1M\x1b[<64;1;1M"))
		got = mxEncode(t, e, MouseEvent{Action: MouseScrollAction, Button: "wheeldown", Col: 0, Row: 0})
		mxAssertBytes(t, "wheeldown", got, []byte("\x1b[<65;1;1M"))
	})
}

// ---------------------------------------------------------------------------
// 拖拽插值点集

func TestMouseDragInterpolation(t *testing.T) {
	// 插值点集决定拖动型控件能否跟手:只有端点时控件会认成瞬移。这里锁死
	// 起点/终点都包含、每格一步、对角线整数取整、反向拖动与 256 步上限。
	cases := []struct {
		name   string
		c0, r0 int
		c1, r1 int
		want   []point
	}{
		{"horizontal", 1, 1, 4, 1, []point{{1, 1}, {2, 1}, {3, 1}, {4, 1}}},
		{"diagonal shallow", 1, 1, 4, 2, []point{{1, 1}, {2, 1}, {3, 1}, {4, 2}}},
		{"diagonal 45", 0, 0, 2, 2, []point{{0, 0}, {1, 1}, {2, 2}}},
		{"vertical", 0, 0, 0, 2, []point{{0, 0}, {0, 1}, {0, 2}}},
		{"single cell", 2, 2, 2, 2, []point{{2, 2}}},
		{"reverse", 3, 0, 0, 0, []point{{3, 0}, {2, 0}, {1, 0}, {0, 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := interpolate(tc.c0, tc.r0, tc.c1, tc.r1)
			if !slices.Equal(got, tc.want) {
				t.Errorf("interpolate(%d,%d,%d,%d) = %v, want %v",
					tc.c0, tc.r0, tc.c1, tc.r1, got, tc.want)
			}
			// 终点必须命中:否则拖动会停在离目标一格的地方。
			if last := got[len(got)-1]; last != (point{tc.c1, tc.r1}) {
				t.Errorf("last point = %v, want destination (%d,%d)", last, tc.c1, tc.r1)
			}
		})
	}

	// 上限:长拖动不能变成无界字节风暴(每次 move 都是一条 CSI),但终点仍须
	// 落在路径末端,控件才会收到最终坐标。
	pts := interpolate(0, 0, 1000, 0)
	if len(pts) != 257 { // 256 步 + 起点
		t.Errorf("capped path has %d points, want 257", len(pts))
	}
	if pts[0] != (point{0, 0}) || pts[len(pts)-1] != (point{1000, 0}) {
		t.Errorf("capped path endpoints = %v..%v, want (0,0)..(1000,0)",
			pts[0], pts[len(pts)-1])
	}
}

// ---------------------------------------------------------------------------
// §2.1 自家模式跟踪

// mxAssertEncoding 比较 MouseEncoding 的完整三元组(启用/线格式/模式列表)。
func mxAssertEncoding(t *testing.T, e *Emulator, enabled bool, mode string, modes []int) {
	t.Helper()
	enc := e.MouseEncoding()
	if enc.Enabled != enabled || enc.Mode != mode || !slices.Equal(enc.Modes, modes) {
		t.Errorf("MouseEncoding() = {Enabled:%v Mode:%q Modes:%v}, want {Enabled:%v Mode:%q Modes:%v}",
			enc.Enabled, enc.Mode, enc.Modes, enabled, mode, modes)
	}
}

func TestMouseTrackingModes(t *testing.T) {
	// x/vt 应用模式但不暴露状态,镜像必须自己跟踪(§2.1)。这组断言锁死:
	// 哪些模式算"应用要鼠标事件"、?1006h 只改线格式、以及全关/RIS 后归零。
	e := NewEmulator(80, 24)
	mxAssertEncoding(t, e, false, "", nil)
	// 未启用时回 409 而非静默写字节:这里锁住底层的 error 与零字节。
	if got, err := e.EncodeMouse(MouseEvent{Action: MouseClickAction, Button: "left", Col: 0, Row: 0}); !errors.Is(err, ErrMouseDisabled) || len(got) != 0 {
		t.Fatalf("disabled EncodeMouse = (%q, %v), want (nil, ErrMouseDisabled)", got, err)
	}

	write(t, e, "\x1b[?1000h")
	mxAssertEncoding(t, e, true, "x10", []int{modeMouseNormal})

	// ?1006h 只切换线格式:SGR 不是"应用请求了鼠标事件"的证据,也不该出现在
	// Modes 里(否则 mouse state 会把编码模式误报成跟踪模式)。
	write(t, e, "\x1b[?1006h")
	mxAssertEncoding(t, e, true, "sgr", []int{modeMouseNormal})

	// 多个报告模式叠加时按编号升序汇报,保证 CLI/JSON 输出稳定。
	write(t, e, "\x1b[?1003h\x1b[?1002h")
	mxAssertEncoding(t, e, true, "sgr", []int{modeMouseNormal, modeMouseButtonEvent, modeMouseAnyEvent})

	// ?1005/?1015 是我们不实现的协议(§5.8):跟踪但不改变启用判定与线格式。
	write(t, e, "\x1b[?1005h\x1b[?1015h")
	mxAssertEncoding(t, e, true, "sgr", []int{modeMouseNormal, modeMouseButtonEvent, modeMouseAnyEvent})

	// 关掉所有报告模式、只留 1006:必须判为未启用,否则会凭空发 SGR 字节。
	write(t, e, "\x1b[?1000l\x1b[?1002l\x1b[?1003l")
	mxAssertEncoding(t, e, false, "", nil)

	// 单开 X10(?)9 也是合法报告模式,线格式仍是默认的 X10。
	e9 := mxEmulator(t, 80, 24, "\x1b[?9h")
	mxAssertEncoding(t, e9, true, "x10", []int{modeMouseX10})

	// 非私有 CSI(缺 '?')不是 DECSET,不能当作鼠标模式。
	ePlain := NewEmulator(80, 24)
	write(t, ePlain, "\x1b[1000h")
	mxAssertEncoding(t, ePlain, false, "", nil)

	// 一条 CSI 带多个参数:?1000;1006h 必须同时设置,复位同理。
	eMulti := NewEmulator(80, 24)
	write(t, eMulti, "\x1b[?1000;1006h")
	mxAssertEncoding(t, eMulti, true, "sgr", []int{modeMouseNormal})
	write(t, eMulti, "\x1b[?1000;1006l")
	mxAssertEncoding(t, eMulti, false, "", nil)

	// RIS 必须清空自跟踪状态:应用重置后不应残留"可点"的鼠标模式。
	eRIS := mxEmulator(t, 80, 24, "\x1b[?1000h", "\x1b[?1006h", "\x1b[?2004h")
	mxAssertEncoding(t, eRIS, true, "sgr", []int{modeMouseNormal})
	if !eRIS.BracketedPaste() {
		t.Fatal("?2004h not tracked")
	}
	write(t, eRIS, "\x1bc")
	mxAssertEncoding(t, eRIS, false, "", nil)
	if eRIS.BracketedPaste() {
		t.Error("RIS left bracketed paste enabled")
	}

	// 括号粘贴独立跟踪:与鼠标模式互不影响(条件粘贴依赖它)。
	ePaste := NewEmulator(80, 24)
	if ePaste.BracketedPaste() {
		t.Fatal("bracketed paste on by default")
	}
	write(t, ePaste, "\x1b[?2004h")
	if !ePaste.BracketedPaste() {
		t.Error("?2004h not tracked")
	}
	mxAssertEncoding(t, ePaste, false, "", nil)
	write(t, ePaste, "\x1b[?2004l")
	if ePaste.BracketedPaste() {
		t.Error("?2004l did not clear bracketed paste")
	}
}

// ---------------------------------------------------------------------------
// §2.1 与上游 SendMouse 的一致性

func TestMouseSendMouseConsistency(t *testing.T) {
	// 设计原本打算把编码交给上游 SendMouse;实现复用了同一套 ansi 编码器。
	// 这里用上游做对拍,保证自家模式跟踪选出的线格式与上游一致:
	// ?1000h 走 X10,叠加 ?1006h 走 SGR,坐标/修饰位/滚轮/移动全部一致。
	cases := []struct {
		name   string
		modes  string
		ev     MouseEvent
		events []uv.MouseEvent
	}{
		{
			name:  "x10 click under ?1000h",
			modes: "\x1b[?1000h",
			ev:    MouseEvent{Action: MouseClickAction, Button: "left", Col: 3, Row: 2},
			events: []uv.MouseEvent{
				uv.MouseClickEvent(uv.Mouse{X: 3, Y: 2, Button: uv.MouseLeft}),
				uv.MouseReleaseEvent(uv.Mouse{X: 3, Y: 2, Button: uv.MouseNone}),
			},
		},
		{
			name:  "sgr click with modifiers under ?1000h+?1006h",
			modes: "\x1b[?1000h\x1b[?1006h",
			ev: MouseEvent{Action: MouseClickAction, Button: "left", Col: 5, Row: 10,
				Mods: MouseMods{Shift: true, Alt: true, Ctrl: true}},
			events: []uv.MouseEvent{
				uv.MouseClickEvent(uv.Mouse{X: 5, Y: 10, Button: uv.MouseLeft, Mod: uv.ModShift | uv.ModAlt | uv.ModCtrl}),
				uv.MouseReleaseEvent(uv.Mouse{X: 5, Y: 10, Button: uv.MouseNone, Mod: uv.ModShift | uv.ModAlt | uv.ModCtrl}),
			},
		},
		{
			name:  "sgr drag motion under ?1002h+?1006h",
			modes: "\x1b[?1002h\x1b[?1006h",
			ev:    MouseEvent{Action: MouseDragAction, Button: "left", Col: 1, Row: 1, ToCol: 3, ToRow: 2},
			events: []uv.MouseEvent{
				uv.MouseMotionEvent(uv.Mouse{X: 1, Y: 1, Button: uv.MouseLeft}),
				uv.MouseMotionEvent(uv.Mouse{X: 2, Y: 1, Button: uv.MouseLeft}),
				uv.MouseMotionEvent(uv.Mouse{X: 3, Y: 2, Button: uv.MouseLeft}),
			},
		},
		{
			name:  "x10 wheel repeats under ?1000h",
			modes: "\x1b[?1000h",
			ev:    MouseEvent{Action: MouseScrollAction, Button: "wheeldown", Col: 0, Row: 0, Amount: 2},
			events: []uv.MouseEvent{
				uv.MouseWheelEvent(uv.Mouse{X: 0, Y: 0, Button: uv.MouseWheelDown}),
				uv.MouseWheelEvent(uv.Mouse{X: 0, Y: 0, Button: uv.MouseWheelDown}),
			},
		},
		{
			name:  "x10 motion without button under ?1003h",
			modes: "\x1b[?1003h",
			ev:    MouseEvent{Action: MouseMoveAction, Col: 4, Row: 5},
			events: []uv.MouseEvent{
				uv.MouseMotionEvent(uv.Mouse{X: 4, Y: 5, Button: uv.MouseNone}),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := mxEmulator(t, 80, 24, tc.modes)
			got := mxEncode(t, e, tc.ev)
			want := mxVtMouseBytes(t, tc.modes, tc.events...)
			mxAssertBytes(t, "EncodeMouse vs vt.SendMouse", got, want)
		})
	}

	t.Run("all modes off writes nothing", func(t *testing.T) {
		// 上游 SendMouse 在没有任何报告模式时静默返回(零字节);自家实现把同一
		// 情况映射为 ErrMouseDisabled。两边都"不写字节",上层据此回 409。
		e := NewEmulator(80, 24)
		ev := MouseEvent{Action: MouseClickAction, Button: "left", Col: 1, Row: 1}
		got, err := e.EncodeMouse(ev)
		if !errors.Is(err, ErrMouseDisabled) {
			t.Errorf("EncodeMouse error = %v, want ErrMouseDisabled", err)
		}
		mxAssertBytes(t, "our disabled bytes", got, nil)
		mxVtMouseSilent(t, "",
			uv.MouseClickEvent(uv.Mouse{X: 1, Y: 1, Button: uv.MouseLeft}),
			uv.MouseReleaseEvent(uv.Mouse{X: 1, Y: 1, Button: uv.MouseNone}))
	})

	t.Run("sgr mode alone is not a mouse request", func(t *testing.T) {
		// 只开 ?1006h(编码格式)不算"应用要鼠标事件":两边都必须不写字节。
		e := mxEmulator(t, 80, 24, "\x1b[?1006h")
		got, err := e.EncodeMouse(MouseEvent{Action: MouseClickAction, Button: "left", Col: 1, Row: 1})
		if !errors.Is(err, ErrMouseDisabled) {
			t.Errorf("EncodeMouse error = %v, want ErrMouseDisabled", err)
		}
		mxAssertBytes(t, "our bytes with only ?1006h", got, nil)
		mxVtMouseSilent(t, "\x1b[?1006h",
			uv.MouseClickEvent(uv.Mouse{X: 1, Y: 1, Button: uv.MouseLeft}),
			uv.MouseReleaseEvent(uv.Mouse{X: 1, Y: 1, Button: uv.MouseNone}))
	})

	t.Run("force sends sgr without modes", func(t *testing.T) {
		// --force 是上游没有的兜底(§2.2 的 tu --force 语义):没有报告模式也要
		// 按 SGR 发出去,否则 agent 无法驱动未声明鼠标支持的 TUI。
		e := NewEmulator(80, 24)
		got := mxEncode(t, e, MouseEvent{Action: MouseClickAction, Button: "left", Col: 1, Row: 1, Force: true})
		mxAssertBytes(t, "forced click", got, []byte("\x1b[<0;2;2M\x1b[<3;2;2m"))
	})
}

// ---------------------------------------------------------------------------
// 错误路径

func TestMouseErrors(t *testing.T) {
	e := mxEmulator(t, 10, 5, "\x1b[?1000h")
	cases := []struct {
		name string
		ev   MouseEvent
		want error
	}{
		{"col at width", MouseEvent{Action: MouseDownAction, Button: "left", Col: 10, Row: 0}, ErrMouseOutOfBounds},
		{"row at height", MouseEvent{Action: MouseDownAction, Button: "left", Col: 0, Row: 5}, ErrMouseOutOfBounds},
		{"negative col", MouseEvent{Action: MouseDownAction, Button: "left", Col: -1, Row: 0}, ErrMouseOutOfBounds},
		{"drag target out of bounds", MouseEvent{Action: MouseDragAction, Button: "left", Col: 0, Row: 0, ToCol: 0, ToRow: 5}, ErrMouseOutOfBounds},
		{"unknown button", MouseEvent{Action: MouseDownAction, Button: "side", Col: 0, Row: 0}, ErrMouseButton},
		{"click without button", MouseEvent{Action: MouseClickAction, Col: 0, Row: 0}, ErrMouseButton},
		{"scroll with plain button", MouseEvent{Action: MouseScrollAction, Button: "left", Col: 0, Row: 0}, ErrMouseButton},
		{"unknown action", MouseEvent{Action: MouseAction("wiggle"), Button: "left", Col: 0, Row: 0}, ErrMouseAction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.EncodeMouse(tc.ev)
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
			// 失败必须零字节:上层把 error 映射成 400/409,不能有半条序列漏进 PTY。
			mxAssertBytes(t, "failed encode bytes", got, nil)
		})
	}

	// 失败的请求不得推进合成光标/按住状态,否则后续不带坐标的 up/scroll 会打偏。
	if st := e.MouseState(); st.CursorCol != 0 || st.CursorRow != 0 || len(st.Held) != 0 || st.LastEvent != "" {
		t.Errorf("state changed by failed encodes: %+v", st)
	}

	// 错误信息要带候选,agent 才能自纠(API 直接把这句话放进 400 响应)。
	if _, err := e.EncodeMouse(MouseEvent{Action: MouseDownAction, Button: "side", Col: 0, Row: 0}); err == nil || !strings.Contains(err.Error(), "wheelup") {
		t.Errorf("unknown-button error = %v, want candidate list including wheelup", err)
	}
	if _, err := e.EncodeMouse(MouseEvent{Action: MouseAction("wiggle"), Button: "left", Col: 0, Row: 0}); err == nil || !strings.Contains(err.Error(), "drag") {
		t.Errorf("unknown-action error = %v, want candidate list including drag", err)
	}
}

// ---------------------------------------------------------------------------
// 合成状态(虚拟光标 / 按住集合 / 最后事件)

func TestMouseStateTracking(t *testing.T) {
	// 合成状态是 `mouse state` 与 PNG 光标叠印的数据源,必须与实际写出的字节
	// 在同一时刻更新;否则"点了哪里"和"报告点了哪里"会脱节。
	e := mxEmulator(t, 80, 24, "\x1b[?1000h", "\x1b[?2004h")
	st := e.MouseState()
	if !st.Enabled || st.Mode != "x10" || !slices.Equal(st.Modes, []int{modeMouseNormal}) {
		t.Errorf("initial encoding = %+v, want enabled x10 [1000]", st)
	}
	if !st.BracketedPaste {
		t.Error("?2004h not reflected in MouseState")
	}
	if st.CursorCol != 0 || st.CursorRow != 0 || len(st.Held) != 0 || st.LastEvent != "" {
		t.Errorf("initial synthetic state = %+v, want empty at (0,0)", st)
	}

	// 按下:虚拟光标跟随事件,按钮进入 held,最后事件可读。
	mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "left", Col: 2, Row: 3})
	st = e.MouseState()
	if st.CursorCol != 2 || st.CursorRow != 3 || st.LastEvent != "down left@2,3" {
		t.Errorf("after down: cursor=(%d,%d) last=%q", st.CursorCol, st.CursorRow, st.LastEvent)
	}
	if !slices.Equal(st.Held, []string{"left"}) {
		t.Errorf("held after down = %v, want [left]", st.Held)
	}

	// 单击是成对的按下+抬起,不会把按钮留在 held 里。
	mxEncode(t, e, MouseEvent{Action: MouseClickAction, Button: "right", Col: 4, Row: 5, Clicks: 2})
	st = e.MouseState()
	if !slices.Equal(st.Held, []string{"left"}) {
		t.Errorf("held after click = %v, want [left] (left still held)", st.Held)
	}
	if st.LastEvent != "click right@4,5" {
		t.Errorf("last event after click = %q, want %q", st.LastEvent, "click right@4,5")
	}

	// 按住移动:drag 沿用 held 的按钮,最后事件带起终点。
	mxEncode(t, e, MouseEvent{Action: MouseDragAction, Button: "left", Col: 4, Row: 5, ToCol: 7, ToRow: 6})
	st = e.MouseState()
	if !slices.Equal(st.Held, []string{"left"}) || st.LastEvent != "drag left@4,5->7,6" {
		t.Errorf("after drag: held=%v last=%q", st.Held, st.LastEvent)
	}

	// 抬起:集合清空;虚拟光标停在抬起位置。
	mxEncode(t, e, MouseEvent{Action: MouseUpAction, Button: "left", Col: 7, Row: 6})
	st = e.MouseState()
	if len(st.Held) != 0 || st.CursorCol != 7 || st.CursorRow != 6 || st.LastEvent != "up left@7,6" {
		t.Errorf("after up: %+v", st)
	}

	// 裸 up(不给 button)释放按住中的按钮。
	mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "middle", Col: 1, Row: 1})
	mxEncode(t, e, MouseEvent{Action: MouseUpAction, Col: 1, Row: 1})
	if st = e.MouseState(); len(st.Held) != 0 {
		t.Errorf("bare up left held = %v, want empty", st.Held)
	}

	// 多个按住按钮按名字排序输出(map 迭代无序,输出必须稳定)。
	mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "right", Col: 0, Row: 0})
	mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "left", Col: 1, Row: 1})
	if st = e.MouseState(); !slices.Equal(st.Held, []string{"left", "right"}) {
		t.Errorf("held = %v, want sorted [left right]", st.Held)
	}
}

// TestMouseDragMovesVirtualCursor 回归:拖拽结束后虚拟光标必须停在**终点**。
// 不带坐标的 up/scroll 会复用它,停在起点会让拖拽在错误的位置释放
// (控件认为拖拽没有结束)。
func TestMouseDragMovesVirtualCursor(t *testing.T) {
	e := mxEmulator(t, 80, 24, "\x1b[?1000h")

	mxEncode(t, e, MouseEvent{Action: MouseDownAction, Button: "left", Col: 2, Row: 3})
	mxEncode(t, e, MouseEvent{Action: MouseDragAction, Col: 2, Row: 3, ToCol: 5, ToRow: 6})

	st := e.MouseState()
	if st.CursorCol != 5 || st.CursorRow != 6 {
		t.Fatalf("cursor after drag = (%d,%d), want the endpoint (5,6)", st.CursorCol, st.CursorRow)
	}
	// last_event 必须带上按钮名:未显式给 button 时沿用 held 的 left。
	if got, want := st.LastEvent, "drag left@2,3->5,6"; got != want {
		t.Errorf("last_event = %q, want %q", got, want)
	}

	// 裸 up 在终点释放,而不是回到起点。
	mxEncode(t, e, MouseEvent{Action: MouseUpAction, Col: 5, Row: 6})
	if st = e.MouseState(); len(st.Held) != 0 {
		t.Errorf("held after up = %v, want empty", st.Held)
	}
}
