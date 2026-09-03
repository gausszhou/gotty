package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// startFakeRelease serves a GitHub-shaped release index plus the binary and
// checksum assets. When tamper is true the index advertises a wrong digest,
// so verification must abort.
func startFakeRelease(t *testing.T, binary []byte, tamper bool) (*httptest.Server, string) {
	t.Helper()
	sum := sha256.Sum256(binary)
	hexSum := hex.EncodeToString(sum[:])
	if tamper {
		hexSum = strings.Repeat("ab", 32)
	}
	var srv *httptest.Server
	mux := http.NewServeMux()
	index := func() string {
		return fmt.Sprintf(`{
  "tag_name": "v2.1.0",
  "name": "v2.1.0",
  "body": "release notes",
  "assets": [
    {"name": "gotty-linux-amd64", "size": %d, "browser_download_url": "%s/gotty-linux-amd64"},
    {"name": "sha256sums.txt", "size": 100, "browser_download_url": "%s/sha256sums.txt"}
  ]
}`, len(binary), srv.URL, srv.URL)
	}
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(index()))
	})
	mux.HandleFunc("/gotty-linux-amd64", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(binary)
	})
	mux.HandleFunc("/sha256sums.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  gotty-linux-amd64\n", hexSum)
	})
	srv = httptest.NewServer(mux)
	return srv, srv.URL + "/index.json"
}

func TestRunEndToEnd(t *testing.T) {
	payload := []byte("new gotty binary v2.1.0")
	srv, index := startFakeRelease(t, payload, false)
	defer srv.Close()

	binDir := t.TempDir()
	exe := filepath.Join(binDir, "gotty")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := Env{GOOS: "linux", GOARCH: "amd64", Executable: func() (string, error) { return exe, nil }}

	var out strings.Builder
	res, err := Run(t.Context(), srv.Client(), Options{
		Version: "v2.1.0", BaseURL: index, Current: "v2.0.0",
		Yes: true, Out: &out,
	}, env)
	if err != nil {
		t.Fatalf("Run: %v\noutput:\n%s", err, out.String())
	}
	if res.Outcome != OutcomeUpdated {
		t.Fatalf("outcome = %v, want updated", res.Outcome)
	}
	if !strings.Contains(out.String(), "restart") {
		t.Errorf("output missing restart hint:\n%s", out.String())
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("binary not replaced: %q", got)
	}
	// 无残留临时文件
	entries, err := os.ReadDir(binDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want 1 (temp leftovers?): %v", len(entries), entries)
	}
}

func TestRunAbortsOnTamperedChecksum(t *testing.T) {
	payload := []byte("new gotty binary v2.1.0")
	srv, index := startFakeRelease(t, payload, true) // 索引里广告错误摘要
	defer srv.Close()

	binDir := t.TempDir()
	exe := filepath.Join(binDir, "gotty")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := Env{GOOS: "linux", GOARCH: "amd64", Executable: func() (string, error) { return exe, nil }}

	var out strings.Builder
	_, err := Run(t.Context(), srv.Client(), Options{
		Version: "v2.1.0", BaseURL: index, Current: "v2.0.0",
		Yes: true, Out: &out,
	}, env)
	if err == nil {
		t.Fatal("Run must fail on checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v, want checksum mismatch mention", err)
	}
	// 旧二进制保留,不落盘新内容
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old binary" {
		t.Errorf("old binary not preserved: %q", got)
	}
}

func TestRunDryRunAndCheck(t *testing.T) {
	payload := []byte("binary")
	srv, index := startFakeRelease(t, payload, false)
	defer srv.Close()

	dir := t.TempDir()
	exe := filepath.Join(dir, "gotty")
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	env := Env{GOOS: "linux", GOARCH: "amd64", Executable: func() (string, error) { return exe, nil }}

	// dry-run:报告但不下载不替换
	var out strings.Builder
	res, err := Run(t.Context(), srv.Client(), Options{
		Version: "v2.1.0", BaseURL: index, Current: "v2.0.0",
		DryRun: true, Out: &out,
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeDryRun {
		t.Errorf("dry-run outcome = %v", res.Outcome)
	}
	if !strings.Contains(out.String(), "would download") {
		t.Errorf("dry-run output:\n%s", out.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Errorf("dry-run must not replace: %q", got)
	}

	// check:报告版本差并返回 ErrOutdated
	var out2 strings.Builder
	_, err = Run(t.Context(), srv.Client(), Options{
		Version: "v2.1.0", BaseURL: index, Current: "v2.0.0",
		Check: true, Out: &out2,
	}, env)
	if err != ErrOutdated {
		t.Errorf("check err = %v, want ErrOutdated", err)
	}

	// 已是最新:不下载、不提示变更
	var out3 strings.Builder
	res3, err := Run(t.Context(), srv.Client(), Options{
		Version: "v2.1.0", BaseURL: index, Current: "v2.1.0",
		Yes: true, Out: &out3,
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Outcome != OutcomeUpToDate {
		t.Errorf("up-to-date outcome = %v", res3.Outcome)
	}
}
