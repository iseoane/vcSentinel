//go:build windows

package reviewsnapshot

import "golang.org/x/sys/windows"

func filesystemSpaceForPath(path string) (filesystemSpace, error) {
	var available, capacity, free uint64
	if err := windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(path), &available, &capacity, &free); err != nil {
		return filesystemSpace{}, err
	}
	return filesystemSpace{available: available, capacity: capacity}, nil
}
