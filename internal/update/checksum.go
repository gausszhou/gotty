package update

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Checksums holds parsed sha256sums.txt contents (hex → filename).
type Checksums map[string]string

// ParseChecksums parses standard `sha256sum` output:
//
//	<64 hex>  gotty-linux-amd64
//
// Lines are split on two-or-more spaces; sha256sum -c style lines
// ("<hex> *name" for binary mode) are accepted too.
func ParseChecksums(data []byte) (Checksums, error) {
	cs := make(Checksums)
	sc := bufio.NewScanner(bytes.NewReader(data))
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("sha256sums.txt line %d: malformed %q", line, text)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return nil, fmt.Errorf("sha256sums.txt line %d: bad hex %q: %w", line, fields[0], err)
		}
		name := strings.TrimPrefix(fields[1], "*") // binary-mode marker
		cs[name] = strings.ToLower(fields[0])
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, fmt.Errorf("sha256sums.txt is empty")
	}
	return cs, nil
}

// Lookup returns the expected hex digest for assetName.
func (cs Checksums) Lookup(assetName string) (string, error) {
	got, ok := cs[assetName]
	if !ok {
		names := make([]string, 0, len(cs))
		for n := range cs {
			names = append(names, n)
		}
		return "", fmt.Errorf("no checksum for %q in sha256sums.txt (have: %v)", assetName, names)
	}
	return got, nil
}

// VerifyChecksum reports whether data hashes to the expected hex digest
// (case-insensitive). On mismatch it returns an error naming both digests.
func VerifyChecksum(data []byte, wantHex string) error {
	sum := sha256.Sum256(data)
	gotHex := hex.EncodeToString(sum[:])
	if !strings.EqualFold(gotHex, wantHex) {
		return fmt.Errorf("checksum mismatch: got %s, want %s", gotHex, strings.ToLower(wantHex))
	}
	return nil
}
