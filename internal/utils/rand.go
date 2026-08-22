package utils

import (
	"crypto/rand"
	"math/big"
	"strconv"
)

// RandomString generates a random alphanumeric string of the given length.
func RandomString(length int) string {
	const base = 36
	size := big.NewInt(base)
	n := make([]byte, length)
	for i := range n {
		c, _ := rand.Int(rand.Reader, size)
		n[i] = strconv.FormatInt(c.Int64(), base)[0]
	}
	return string(n)
}

// IsValidSessionID reports whether id is a client-generated session id:
// exactly 16 base36 characters, the same alphabet RandomString(16) emits.
func IsValidSessionID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}
