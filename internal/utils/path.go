package utils

import "os"

// Expand expands ~/ to the user's home directory.
func Expand(path string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		return os.Getenv("HOME") + path[1:]
	}
	return path
}
