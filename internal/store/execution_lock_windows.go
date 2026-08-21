//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type executionLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireExecutionLock(path string, timeout time.Duration) (*executionLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	lock := &executionLock{file: file}
	deadline := time.Now().Add(timeout)
	for {
		err = windows.LockFileEx(
			windows.Handle(file.Fd()),
			windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0,
			1,
			0,
			&lock.overlapped,
		)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = file.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("store: timeout acquiring execution event lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (l *executionLock) Close() error {
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
