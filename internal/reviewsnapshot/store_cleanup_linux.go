//go:build linux

package reviewsnapshot

import "io/fs"

func removalMode(mode fs.FileMode, directory bool) fs.FileMode {
	if directory {
		return mode.Perm() | 0o700
	}
	return mode.Perm() | 0o200
}
