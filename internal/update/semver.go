package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version (major.minor.patch[-prerelease]).
// Build metadata (+...) is parsed and ignored for precedence, per semver.
type Version struct {
	Major, Minor, Patch int
	Prerelease          string // without the leading '-'
}

// ParseVersion parses a semver string with an optional leading 'v' or 'V'
// (git describe --tags emits v-prefixed tags). Non-semver inputs
// ("unknown_version", bare commit ids, "1.2.3.4") are rejected — callers
// decide how to treat an unparseable *current* version.
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Version{}, fmt.Errorf("empty version")
	}
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	// Strip build metadata: 1.2.3+meta → 1.2.3.
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	// Split prerelase: 1.2.3-beta.1 → 1.2.3 / beta.1.
	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre, s = s[i+1:], s[:i]
	}
	nums := strings.Split(s, ".")
	if len(nums) != 3 {
		return Version{}, fmt.Errorf("%q is not a semantic version (want major.minor.patch)", s)
	}
	major, err1 := strconv.Atoi(nums[0])
	minor, err2 := strconv.Atoi(nums[1])
	patch, err3 := strconv.Atoi(nums[2])
	if err1 != nil || err2 != nil || err3 != nil ||
		major < 0 || minor < 0 || patch < 0 {
		return Version{}, fmt.Errorf("%q is not a semantic version (non-numeric part)", s)
	}
	return Version{Major: major, Minor: minor, Patch: patch, Prerelease: pre}, nil
}

// String renders the version back as a string (without build metadata).
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Prerelease != "" {
		s += "-" + v.Prerelease
	}
	return s
}

// comparePrerelease orders two prerelease strings per semver §11:
//
//   - a version without prerelease is higher than one with it;
//   - identifiers are split on '.', numeric ones compare numerically,
//     alphanumeric ones lexically, and numeric < alphanumeric.
func comparePrerelease(a, b string) int {
	if a == "" && b == "" {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		aNum, aIsNum := isNumericIdentifier(as[i])
		bNum, bIsNum := isNumericIdentifier(bs[i])
		switch {
		case aIsNum && bIsNum:
			if aNum < bNum {
				return -1
			}
			if aNum > bNum {
				return 1
			}
		case aIsNum != bIsNum:
			if aIsNum {
				return -1 // numeric < alphanumeric
			}
			return 1
		default: // both alphanumeric: ASCII lexical
			if as[i] < bs[i] {
				return -1
			}
			if as[i] > bs[i] {
				return 1
			}
		}
	}
	// A longer identifier list wins when the shared prefix is equal.
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}

func isNumericIdentifier(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// CompareVersions orders two versions: -1 if a < b, 0 if equal,
// +1 if a > b (precedence, build metadata ignored).
func CompareVersions(a, b string) (int, error) {
	va, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	if va.Major != vb.Major {
		return sign(va.Major - vb.Major), nil
	}
	if va.Minor != vb.Minor {
		return sign(va.Minor - vb.Minor), nil
	}
	if va.Patch != vb.Patch {
		return sign(va.Patch - vb.Patch), nil
	}
	return comparePrerelease(va.Prerelease, vb.Prerelease), nil
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}
