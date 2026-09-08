//go:build linux

package reviewsnapshot

import "io/fs"

// publishedPerm maps a committed Git regular-file mode to the immutable
// POSIX permission the store publishes.
func publishedPerm(gitMode string) fs.FileMode {
	if gitMode == "100755" {
		return 0o500
	}
	return 0o400
}

// publishedPermMatches preserves the strict POSIX validation contract.
func publishedPermMatches(got, want fs.FileMode) bool {
	return got == want
}
