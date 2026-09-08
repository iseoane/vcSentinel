//go:build linux

package reviewsnapshot

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// snapshotFileLock holds one open handle on a lock file with an advisory
// flock(2) lock over it. It is the package-local counterpart of
// internal/git's snapshot lock, with one difference: acquisition is always
// nonblocking here, because the snapshot store's waiting policies (a creator
// waiting for another process's exclusive hold, a caller waiting out a
// cancellation) need to interleave context checks into the retry loop, which
// a blocking syscall cannot do.
type snapshotFileLock struct {
	file *os.File
}

// lockSnapshot takes a shared (exclusive=false) or exclusive advisory lock on
// the lock file at path and never blocks: the boolean reports whether the
// lock was acquired. flock locks are keyed by open file description, so two
// handles in one process contend exactly like two processes: any number of
// shared leases may coexist, and an exclusive lock succeeds only while no
// shared or exclusive holder exists anywhere — the property the reaper and
// the final lease release rely on to prove a snapshot tree is unused.
func lockSnapshot(path string, exclusive bool) (*snapshotFileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	operation := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		operation = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), operation); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &snapshotFileLock{file: file}, true, nil
}

// Close releases the advisory lock and closes the handle. Releasing a lock
// whose file has since been unlinked is harmless: the kernel drops the lock
// with the now-unreferenced inode.
func (l *snapshotFileLock) Close() error {
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
