//go:build linux

package store

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type executionLock struct {
	file *os.File
}

func acquireExecutionLock(path string, timeout time.Duration) (*executionLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &executionLock{file: file}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
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
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
