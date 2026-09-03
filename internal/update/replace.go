package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicReplace writes data to a temp file in target's directory, fsyncs
// it, then renames it over target. Same directory ⇒ same filesystem ⇒ the
// rename is atomic, so a crash mid-update never leaves a truncated binary.
// The old binary survives if anything fails (temp cleanup included).
func AtomicReplace(target string, data []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".gotty-update-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	defer cleanup()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	// 保留原二进制的权限(Windows 无 exec 位,仅 Unix 有意义)。
	if fi, err := os.Stat(target); err == nil {
		_ = os.Chmod(tmpName, fi.Mode().Perm())
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", target, err)
	}

	if err := os.Rename(tmpName, target); err != nil {
		// Windows 上运行中的 exe 不可被 rename;错误路径保留旧二进制。
		return fmt.Errorf("replace %s: %w (the old binary was left intact)", target, err)
	}
	return nil
}
