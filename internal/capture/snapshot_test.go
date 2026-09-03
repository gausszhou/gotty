package capture

import (
	"encoding/base64"
	"image/color"
	"testing"
)

// Snapshot 是 agent 读屏/等待的数据载体:必须是仿真器的深拷贝,
// 快照后继续写入的 PTY 输出不得污染已取的快照,反之亦然。

func TestSnapshotDeepCopiesGrid(t *testing.T) {
	e := NewEmulator(20, 3)
	write(t, e, "hello")
	snap := e.Snapshot()

	// 快照之后仿真器继续演进,快照内容不变
	write(t, e, " world")
	if got := snap.Text(); got != "hello" {
		t.Errorf("snapshot text after emulator write = %q, want %q", got, "hello")
	}
	assertText(t, e, "hello world")

	// 修改快照网格,仿真器不受影响(行列不共享切片)
	snap.grid.cells[0][0] = Cell{Rune: 'X'}
	assertText(t, e, "hello world")
	if got := snap.Text(); got != "Xello" {
		t.Errorf("mutated snapshot text = %q, want %q", got, "Xello")
	}
}

func TestSnapshotCarriesState(t *testing.T) {
	e := NewEmulator(12, 4)
	write(t, e, "ab\r\n\x1b[2;3H\x1b[?25l") // 光标 (1,2),隐藏

	snap := e.Snapshot()
	if snap.Cols != 12 || snap.Rows != 4 {
		t.Errorf("size = %dx%d, want 12x4", snap.Cols, snap.Rows)
	}
	if snap.CursorRow != 1 || snap.CursorCol != 2 {
		t.Errorf("cursor = (%d,%d), want (1,2)", snap.CursorRow, snap.CursorCol)
	}
	if snap.CursorVisible {
		t.Error("cursor visible = true, want false")
	}
	if snap.CellW != 9 || snap.CellH != 18 {
		t.Errorf("cell size = %dx%d, want default 9x18", snap.CellW, snap.CellH)
	}
	if snap.TakenAt.IsZero() {
		t.Error("taken_at must be set")
	}
}

func TestSnapshotCarriesImages(t *testing.T) {
	e := NewEmulator(20, 10)
	raw := testPNGBytes(t, 2, 2, color.RGBA{R: 255, A: 255})
	write(t, e, kittyAPC("a=T,f=100,m=0", base64.StdEncoding.EncodeToString(raw)))

	snap := e.Snapshot()
	if len(snap.Images) != 1 {
		t.Fatalf("snapshot images = %d, want 1", len(snap.Images))
	}
	if snap.Images[0].Protocol != ProtoKitty {
		t.Errorf("protocol = %s, want kitty", snap.Images[0].Protocol)
	}
}

func TestSnapshotRenders(t *testing.T) {
	e := NewEmulator(20, 3)
	write(t, e, "\x1b[31mred\x1b[0m text")
	snap := e.Snapshot()

	if got := snap.Text(); got != "red text" {
		t.Errorf("snapshot text = %q, want %q", got, "red text")
	}

	cells := snap.CellsJSON()
	if len(cells) == 0 {
		t.Fatal("cells must not be empty")
	}
	if cells[0].Ch != "r" || cells[0].Fg == nil {
		t.Errorf("first cell = %+v, want styled red 'r'", cells[0])
	}

	pngBytes, err := snap.PNG()
	if err != nil {
		t.Fatal(err)
	}
	if len(pngBytes) < 8 || pngBytes[0] != 0x89 || pngBytes[1] != 'P' {
		t.Errorf("PNG output is not a PNG (len=%d)", len(pngBytes))
	}
}

// 快照渲染不依赖仿真器后续状态:Text/CellsJSON 作用于深拷贝的网格。
func TestSnapshotIsolatedFromEmulatorState(t *testing.T) {
	e := NewEmulator(10, 3)
	write(t, e, "snap")
	snap := e.Snapshot()

	// 仿真器清屏后,快照仍保留内容
	write(t, e, "\x1bc") // RIS
	if got := snap.Text(); got != "snap" {
		t.Errorf("snapshot text after emulator reset = %q, want %q", got, "snap")
	}
}
