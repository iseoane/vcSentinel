//go:build linux

package git

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type snapshotFileLock struct {
	file *os.File
}

func lockSnapshot(path string, exclusive, wait bool) (*snapshotFileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	operation := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		operation = unix.LOCK_EX | unix.LOCK_NB
	}
	for {
		if err := unix.Flock(int(file.Fd()), operation); err == nil {
			return &snapshotFileLock{file: file}, true, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
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
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
