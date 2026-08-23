//go:build unix

package daemon

import "syscall"

// pidAlive reports whether a process with the given pid currently exists on
// Unix systems. signal 0 (kill(pid, 0)) performs permission and existence
// checks without delivering a signal: nil means alive, ESRCH means the pid
// does not exist, and EPERM means the process exists but is owned by another
// user, which counts as alive. Any other error is treated as dead; the claim
// protocol re-checks liveness on every use.
//
// Residual risk accepted at D2 slice 1: PID reuse between the claim write
// and this check is not detected (see Claim).
func pidAlive(pid int) bool {
	switch err := syscall.Kill(pid, 0); {
	case err == nil:
		return true
	case err == syscall.ESRCH:
		return false
	case err == syscall.EPERM:
		return true
	default:
		return false
	}
}
