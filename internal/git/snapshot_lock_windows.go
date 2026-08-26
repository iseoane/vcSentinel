//go:build windows

package git

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type snapshotFileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func lockSnapshot(path string, exclusive, wait bool) (*snapshotFileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	lock := &snapshotFileLock{file: file}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	for {
		err = windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &lock.overlapped)
		if err == nil {
			return lock, true, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = file.Close()
			return nil, false, err
		}
		if !wait {
			_ = file.Close()
			return nil, false, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (l *snapshotFileLock) Close() error {
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
