//go:build unix

package presence

import "syscall"

// pidAlive reports whether a process with the given pid exists on Unix.
// Signal 0 checks existence and permission without delivering a signal: nil
// is alive, ESRCH is dead, EPERM means another user's existing process
// (alive, conservatively); any other error is dead. Verdict-for-verdict this
// mirrors internal/daemon's unexported helper; the copy lives here because
// presence must not depend on daemon internals beyond its stable read API.
//
// Residual risk, same as the windows twin: pids are recycled by the OS, so a
// stale endpoint.json naming a since-reused pid reports Live for an unrelated
// process. Endpoint records are removed on every graceful daemon exit, so the
// window opens only after a hard kill plus pid reuse; disambiguating further
// would require daemon-owned identity checks out of scope here.
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
