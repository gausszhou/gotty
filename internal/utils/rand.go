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
