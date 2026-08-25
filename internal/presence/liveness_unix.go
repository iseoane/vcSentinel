//go:build unix

package presence

import "syscall"

// pidAlive reports whether a process with the given pid exists on Unix.
// Signal 0 checks existence and permission without delivering a signal: nil
// is alive, ESRCH is dead, EPERM means another user's existing process
// (alive, conservatively); any other error is dead. Verdict-for-verdict this
// mirrors internal/daemon's unexported helper; the copy lives here because
// presence must not depend on daemon internals beyond its stable read API.
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
