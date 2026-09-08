//go:build windows

package reviewsnapshot

import "io/fs"

// publishedPerm requests a read-only file on Windows. Windows does not retain
// POSIX execute bits through os.Chmod.
func publishedPerm(string) fs.FileMode {
	return 0o400
}

// publishedPermMatches accepts Windows' read-only mode representation while
// still rejecting any writable published evidence.
func publishedPermMatches(got, _ fs.FileMode) bool {
	return got&0o222 == 0
}
