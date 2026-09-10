//go:build windows

package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startWindowsRelease serves a GitHub-shaped index advertising the Windows
// asset name (AssetName("windows","amd64")), plus the binary and checksums.
func startWindowsRelease(t *testing.T, asset string, payload []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])

	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v2.1.0","name":"v2.1.0","body":"release notes","assets":[`+
			`{"name":%q,"size":%d,"browser_download_url":"%s/%s"},`+
			`{"name":"sha256sums.txt","size":100,"browser_download_url":"%s/sha256sums.txt"}]}`,
			asset, len(payload), srv.URL, asset, srv.URL)
	})
	mux.HandleFunc("/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/sha256sums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hexSum, asset)
	})
	srv = httptest.NewServer(mux)
	return srv
}

// TestSelfUpdateReplacesRunningExecutable drives the whole `gotty self update`
// path on Windows against a local release index, where the target *is* the
// binary of a running process — which is what self-update always is.
//
// Before replaceFile learned the rename-aside fallback, this failed with
// ERROR_ACCESS_DENIED and the "old binary was left intact" message. It is the
// end-to-end counterpart of TestAtomicReplaceRunningExecutable: this one also
// covers asset selection for windows/amd64 and the download+verify steps.
func TestSelfUpdateReplacesRunningExecutable(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	if err != nil {
		t.Skipf("no cmd.exe to copy: %s", err)
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "gotty.exe")
	if err := os.WriteFile(exe, src, 0o755); err != nil {
		t.Fatal(err)
	}

	victim := exec.Command(exe, "/c", "ping -n 30 127.0.0.1 >NUL")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = victim.Process.Kill()
		_, _ = victim.Process.Wait()
	})
	// 给它时间把镜像映射起来 —— 映射之后这个文件才换不掉。
	time.Sleep(time.Second)

	payload := []byte("new gotty binary v2.1.0")
	srv := startWindowsRelease(t, "gotty-windows-amd64.exe", payload)
	defer srv.Close()

	var out strings.Builder
	res, err := Run(t.Context(), srv.Client(), Options{
		Version: "v2.1.0", BaseURL: srv.URL + "/index.json", Current: "v2.0.0",
		Yes: true, Out: &out,
	}, Env{
		GOOS: "windows", GOARCH: "amd64",
		Executable: func() (string, error) { return exe, nil },
	})
	if err != nil {
		t.Fatalf("self update over a running binary must succeed: %v\noutput:\n%s", err, out.String())
	}
	if res.Outcome != OutcomeUpdated {
		t.Fatalf("outcome = %v, want updated", res.Outcome)
	}
	if res.TargetPath != exe {
		t.Errorf("target path = %s, want %s", res.TargetPath, exe)
	}

	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("binary not replaced: %q", got)
	}
	// 更新只换文件、不重启进程。
	if victim.ProcessState != nil {
		t.Error("the running process was reaped; an update must not kill it")
	}

	// 进程必须先退出,否则 t.TempDir 的清理删不掉仍被映射的目录。
	_ = victim.Process.Kill()
	_, _ = victim.Process.Wait()
}
