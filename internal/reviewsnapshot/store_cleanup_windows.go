//go:build windows

package reviewsnapshot

import "io/fs"

func removalMode(fs.FileMode, bool) fs.FileMode {
	return 0o600
}
