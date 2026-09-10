package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 轮转:每行 40B、上限 100B、保留 2 份 → 写 10 行后应当轮转 4 次,
// 只剩最后 6 行(base 2 行 + .1 两行 + .2 两行),更早的被丢弃。
func TestRotatingWriterRotatesAndDropsOldest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gotty.log")
	w, err := NewRotatingWriter(path, 100, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	line := strings.Repeat("x", 39) + "\n" // 40 字节
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write #%d: %v", i, err)
		}
	}

	if got := fileSize(t, path); got != 80 {
		t.Errorf("current file size = %d, want 80", got)
	}
	if got := fileSize(t, w.BackupPath(1)); got != 80 {
		t.Errorf(".1 size = %d, want 80", got)
	}
	if got := fileSize(t, w.BackupPath(2)); got != 80 {
		t.Errorf(".2 size = %d, want 80", got)
	}
	if _, err := os.Stat(w.BackupPath(3)); !os.IsNotExist(err) {
		t.Errorf(".3 must not exist (maxBackups=2), stat err = %v", err)
	}
}

// maxSize <= 0 关闭轮转:全部内容留在当前文件里。
func TestRotatingWriterRotationDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gotty.log")
	w, err := NewRotatingWriter(path, 0, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	for i := 0; i < 50; i++ {
		if _, err := w.Write([]byte(strings.Repeat("y", 100))); err != nil {
			t.Fatalf("write #%d: %v", i, err)
		}
	}
	if got := fileSize(t, path); got != 5000 {
		t.Errorf("size = %d, want 5000", got)
	}
	if _, err := os.Stat(w.BackupPath(1)); !os.IsNotExist(err) {
		t.Errorf("rotation disabled but .1 exists, stat err = %v", err)
	}
}

// 单条写入大于上限时必须照写(否则日志会被"轮转掉"什么也不剩)。
func TestRotatingWriterOversizedWriteIsKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gotty.log")
	w, err := NewRotatingWriter(path, 10, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("z", 500))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := fileSize(t, path); got != 500 {
		t.Errorf("size = %d, want 500", got)
	}
	if _, err := os.Stat(w.BackupPath(1)); !os.IsNotExist(err) {
		t.Errorf("oversized first write must not rotate, stat err = %v", err)
	}
}

// 打开已存在的文件时按现有大小继续计数:90B 基础上再写 40B 会触发轮转。
func TestRotatingWriterCountsExistingSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gotty.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 90)), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewRotatingWriter(path, 100, 1)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	if _, err := w.Write([]byte(strings.Repeat("b", 40))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := fileSize(t, w.BackupPath(1)); got != 90 {
		t.Errorf(".1 size = %d, want 90", got)
	}
	if got := fileSize(t, path); got != 40 {
		t.Errorf("current size = %d, want 40", got)
	}
}

// Close 之后写入返回 os.ErrClosed,且重复 Close 不报错。
func TestRotatingWriterWriteAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gotty.log")
	w, err := NewRotatingWriter(path, 100, 1)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := w.Write([]byte("x")); err != os.ErrClosed {
		t.Errorf("write after close err = %v, want os.ErrClosed", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}
