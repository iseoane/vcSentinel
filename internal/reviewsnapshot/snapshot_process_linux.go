//go:build linux

package reviewsnapshot

import (
	"errors"

	"golang.org/x/sys/unix"
)

// processAlive treats uncertain results as alive. PID reuse can therefore only
// produce a false ALIVE: the orphan is deferred to the ordinary age
// ceiling, which is harmless, never a false DEAD because dead requires no
// process to hold that PID. That asymmetry needs no process-start-time comparison.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	if err == nil || errors.Is(err, unix.EPERM) {
		return true
	}
	if errors.Is(err, unix.ESRCH) {
		return false
	}
	return true
}
