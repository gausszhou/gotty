//go:build unix

package update

import "os"

// replaceFile swaps tmp into place over target.
//
// POSIX rename is atomic and replaces a running executable without complaint:
// the old inode stays alive for as long as the running process maps it, so
// `gotty self update` needs no special handling here.
func replaceFile(tmp, target string) error {
	return os.Rename(tmp, target)
}

// reapStaleBinaries is a no-op on unix: replaceFile never has to move the old
// binary aside, so it never leaves one behind.
func reapStaleBinaries(string) {}
