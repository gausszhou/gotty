package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// semver ----------------------------------------------------------------

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"2.0.0", Version{Major: 2, Minor: 0, Patch: 0}, true},
		{"v2.1.0", Version{Major: 2, Minor: 1, Patch: 0}, true},
		{"V2.1.0", Version{Major: 2, Minor: 1, Patch: 0}, true},
		{"2.1.0-beta.1", Version{Major: 2, Minor: 1, Patch: 0, Prerelease: "beta.1"}, true},
		{"2.1.0+build5", Version{Major: 2, Minor: 1, Patch: 0}, true},
		{"2.1.0-beta.1+build5", Version{Major: 2, Minor: 1, Patch: 0, Prerelease: "beta.1"}, true},
		{"", Version{}, false},
		{"abc1234", Version{}, false},
		{"unknown_version", Version{}, false},
		{"2.1", Version{}, false},
		{"2.1.0.1", Version{}, false},
		{"2.x.0", Version{}, false},
		{"-1.0.0", Version{}, false},
	}
	for _, c := range cases {
		got, err := ParseVersion(c.in)
		if c.ok != (err == nil) {
			t.Errorf("ParseVersion(%q) err = %v, want ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"2.0.0", "2.0.0", 0},
		{"v2.0.0", "v2.0.0", 0},
		{"2.1.0", "2.0.9", 1},
		{"2.0.0", "2.1.0", -1},
		{"2.1.0", "2.1.1", -1},
		// pre-release 排序
		{"2.1.0", "2.1.0-beta.1", 1}, // release > pre-release
		{"2.1.0-beta.1", "2.1.0-beta.2", -1},
		{"2.1.0-beta.9", "2.1.0-beta.10", -1},         // 数字标识符按数值比
		{"2.1.0-alpha", "2.1.0-beta", -1},             // 字母标识符按 ASCII
		{"2.1.0-beta.1", "2.1.0-beta", 1},             // 更长标识符列表优先
		{"2.1.0-rc.1+build7", "2.1.0-rc.1+build9", 0}, // build 元数据忽略
		{"2.0.0", "v2.1.0", -1},
	}
	for _, c := range cases {
		got, err := CompareVersions(c.a, c.b)
		if err != nil {
			t.Errorf("CompareVersions(%q, %q) err = %v", c.a, c.b, err)
			continue
		}
		if got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareVersionsRejectsJunk(t *testing.T) {
	if _, err := CompareVersions("abc1234", "2.0.0"); err == nil {
		t.Error("CompareVersions with a bare git hash must fail")
	}
}

// checksum --------------------------------------------------------------

func TestParseChecksums(t *testing.T) {
	// 两空格标准格式与二进制模式(* 前缀)混排;hex 由程序生成保证 64 位。
	sum1 := strings.Repeat("1a", 32)
	sum2 := strings.Repeat("0f", 32)
	data := []byte(sum1 + "  gotty-linux-amd64\n" +
		sum2 + " *gotty-windows-amd64.exe\n")
	sums, err := ParseChecksums(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(sums))
	}
	if _, err := sums.Lookup("gotty-linux-amd64"); err != nil {
		t.Errorf("lookup linux: %v", err)
	}
	if _, err := sums.Lookup("gotty-windows-amd64.exe"); err != nil {
		t.Errorf("lookup windows: %v", err)
	}
}

func TestParseChecksumsRejectsMalformed(t *testing.T) {
	bad := []string{
		"",
		"abcd gotty-linux-amd64", // 非 hex
		"1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a only-one-field",
		"1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a  a  b",
	}
	for _, data := range bad {
		if _, err := ParseChecksums([]byte(data)); err == nil {
			t.Errorf("ParseChecksums(%q) must fail", data)
		}
	}
}

func TestVerifyChecksum(t *testing.T) {
	payload := []byte("gotty binary payload")
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])
	if err := VerifyChecksum(payload, good); err != nil {
		t.Errorf("VerifyChecksum(good) = %v, want nil", err)
	}
	if err := VerifyChecksum([]byte("tampered"), good); err == nil {
		t.Error("VerifyChecksum(tampered) must fail — 篡改内容必须中止更新")
	}
	if err := VerifyChecksum(payload, "deadbeef"); err == nil {
		t.Error("VerifyChecksum(wrong digest) must fail")
	}
}

// atomic replace --------------------------------------------------------

func TestAtomicReplaceSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gotty")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicReplace(target, []byte("new binary")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new binary" {
		t.Errorf("target content = %q, want %q", got, "new binary")
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755 (old binary's mode preserved)", fi.Mode().Perm())
	}
	// 无残留临时文件
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir entries = %d, want 1 (no temp leftovers): %v", len(entries), entries)
	}
}

func TestAtomicReplaceKeepsOldOnFailure(t *testing.T) {
	dir := t.TempDir()
	// 目标目录不存在 → CreateTemp 失败 → 旧二进制(不存在的状态)保持,
	// 且目录中不留下任何 .gotty-update-* 残骸。
	if err := AtomicReplace(filepath.Join(dir, "missing", "gotty"), []byte("x")); err == nil {
		t.Fatal("replace into non-existent directory must fail")
	}

	// 目标存在、目录不可写(非 root 下 CreateTemp 即失败)→ 旧二进制原样保留。
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
