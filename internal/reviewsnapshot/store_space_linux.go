//go:build linux

package reviewsnapshot

import "golang.org/x/sys/unix"

func filesystemSpaceForPath(path string) (filesystemSpace, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return filesystemSpace{}, err
	}
	blockSize := uint64(stat.Bsize)
	return filesystemSpace{
		available: uint64(stat.Bavail) * blockSize,
		capacity:  uint64(stat.Blocks) * blockSize,
	}, nil
}
