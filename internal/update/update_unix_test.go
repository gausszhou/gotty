//go:build unix

package update

// POSIX-only assertions for AtomicReplace, moved out of update_test.go so the
// Windows build is not permanently red on semantics Windows does not have:
// os.Stat reports 0666 for a regular file (there is no exec bit) and os.Chmod on
// a directory is a no-op, so a read-only directory cannot be created at all.
//
// No Windows equivalent is asserted on purpose: os.Rename over an existing file
// can fail on Windows for unrelated reasons (a running executable, a read-only
// target), so such an assertion would fail for the wrong cause and read as a
// green light it does not deserve.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAtomicReplacePreservesMode: 新二进制沿用旧二进制的权限位(含 exec 位)。
func TestAtomicReplacePreservesMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gotty")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicReplace(target, []byte("new binary")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755 (old binary's mode preserved)", fi.Mode().Perm())
	}
}

// TestAtomicReplaceFailsOnReadOnlyDir: 目标存在、目录不可写时替换必须失败,
// 且旧二进制原样保留(非 root 下 CreateTemp 即失败)。
func TestAtomicReplaceFailsOnReadOnlyDir(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "ro")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(targetDir, "gotty")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(targetDir, 0o755)
	if err := AtomicReplace(target, []byte("new")); err == nil {
		t.Fatal("replace into read-only directory must fail")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("old binary not preserved: %q", got)
	}
}
