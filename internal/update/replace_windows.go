//go:build windows

package update

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// staleMarker names the "moved aside" copy of a binary that was still running
// when it was replaced (see replaceFile). It is deliberately distinctive so
// reaping cannot touch unrelated files.
const staleMarker = ".gotty-old-"

// replaceFile swaps tmp into place over target.
//
// Windows maps a running executable's image, so that file cannot be replaced or
// deleted while the process lives: os.Rename over it fails with
// ERROR_ACCESS_DENIED ("Access is denied"). That is precisely the situation
// `gotty self update` is in — it replaces the binary it was started from — so
// without this fallback every self-update on Windows failed.
//
// Renaming the running image *aside* does work, so the fallback is:
//
//	rename(target → target.gotty-old-<pid>)   // the running process keeps working
//	rename(tmp → target)                      // the new binary takes the name
//
// The aside copy stays behind because it is still mapped; the next run reaps it.
func replaceFile(tmp, target string) error {
	// 先直接替换:目标没在运行时 Windows 与 unix 行为一致,不必多此一举。
	err := os.Rename(tmp, target)
	if err == nil {
		return nil
	}

	aside := target + staleMarker + strconv.Itoa(os.Getpid())
	if rerr := os.Rename(target, aside); rerr != nil {
		// 连让位都不行(目标不存在、无权限等):回报最初的错误,它更贴近真相。
		return err
	}
	if rerr := os.Rename(tmp, target); rerr != nil {
		// 回滚,旧二进制绝不能以 aside 的名字失踪。
		_ = os.Rename(aside, target)
		return rerr
	}
	// 运行中的旧镜像还锁着这个文件,删不掉是正常的;留给下一次运行回收。
	_ = os.Remove(aside)
	return nil
}

// reapStaleBinaries removes moved-aside binaries left by earlier updates, now
// that whatever held them has exited. Failures are ignored: a file that is
// still locked simply survives to a later run.
func reapStaleBinaries(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), staleMarker) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
