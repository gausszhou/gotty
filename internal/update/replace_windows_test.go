//go:build windows

package update

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// staleNames lists the moved-aside binaries left in dir.
func staleNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.Contains(e.Name(), staleMarker) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestAtomicReplaceRunningExecutable is the regression test for `gotty self
// update` on Windows, which replaces the binary it is running from.
//
// Windows maps a running image, so a plain rename over it fails with
// ERROR_ACCESS_DENIED: before replaceFile grew its fallback, every self-update
// on Windows failed with "the old binary was left intact". The victim here is a
// copy of cmd.exe held open by a long ping — that reproduces the lock without
// re-executing the test binary.
func TestAtomicReplaceRunningExecutable(t *testing.T) {
	systemCmd := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	src, err := os.ReadFile(systemCmd)
	if err != nil {
		t.Skipf("no cmd.exe to copy (%s): %s", systemCmd, err)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "victim.exe")
	if err := os.WriteFile(target, src, 0o755); err != nil {
		t.Fatal(err)
	}

	victim := exec.Command(target, "/c", "ping -n 30 127.0.0.1 >NUL")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = victim.Process.Kill()
		_, _ = victim.Process.Wait()
	})
	// 给它时间把镜像映射起来(映射之后这个文件才删不掉/换不掉)。
	time.Sleep(time.Second)

	const first = "first replacement payload"
	if err := AtomicReplace(target, []byte(first)); err != nil {
		t.Fatalf("replacing a running executable must succeed: %s", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != first {
		t.Errorf("target content = %q, want %q", got, first)
	}
	// 更新只换文件、不重启进程:原进程必须还活着(我们从未 Wait 它)。
	if victim.ProcessState != nil {
		t.Error("the running process was reaped; an update must not kill it")
	}

	// 旧镜像仍被映射,所以让位文件此时删不掉,应当留在原地。
	if names := staleNames(t, dir); len(names) == 0 {
		t.Log("note: the moved-aside copy could be removed immediately on this system")
	}

	// 进程退出后,下一次替换应把它回收掉。
	_ = victim.Process.Kill()
	if _, err := victim.Process.Wait(); err != nil {
		t.Fatalf("waiting for the victim: %s", err)
	}
	const second = "second replacement payload"
	if err := AtomicReplace(target, []byte(second)); err != nil {
		t.Fatalf("second replace: %s", err)
	}
	got, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != second {
		t.Errorf("target content = %q, want %q", got, second)
	}
	if names := staleNames(t, dir); len(names) != 0 {
		t.Errorf("stale aside files were not reaped: %v", names)
	}
}
