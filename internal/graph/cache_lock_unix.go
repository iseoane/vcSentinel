//go:build !windows

package graph

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openLockFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0600)
}

func tryLockFile(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
