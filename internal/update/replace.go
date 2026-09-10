package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicReplace writes data to a temp file in target's directory, fsyncs
// it, then swaps it into place over target. Same directory ⇒ same filesystem ⇒
// the swap is atomic, so a crash mid-update never leaves a truncated binary.
// The old binary survives if anything fails (temp cleanup included).
//
// The swap itself is platform-specific (see replaceFile). It matters because
// `gotty self update` replaces the binary it is *currently running from*: unix
// can rename over a running executable, Windows cannot.
func AtomicReplace(target string, data []byte) error {
	dir := filepath.Dir(target)
	// 上一次更新若在 Windows 上让位过一个运行中的旧 exe,它会留在这里;
	// 那个进程已经退出的话,现在正好回收。
	reapStaleBinaries(dir)

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

	if err := replaceFile(tmpName, target); err != nil {
		// Windows 上运行中的 exe 不能被覆盖;replaceFile 会先让位再安装,
		// 回滚也由它负责 —— 走到这里说明旧二进制仍在原位。
		return fmt.Errorf("replace %s: %w (the old binary was left intact)", target, err)
	}
	return nil
}
