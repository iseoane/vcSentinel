//go:build windows

package graph

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openLockFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDWR|os.O_CREATE, 0600)
}

func tryLockFile(file *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}
