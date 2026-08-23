//go:build windows

package daemon

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is the Windows exit-code constant reported by
// GetExitCodeProcess while a process keeps running (x/sys/windows does not
// re-export it).
const stillActive = 259

// pidAlive reports whether a process with the given pid currently exists on
// Windows. OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION probes for
// existence: ERROR_ACCESS_DENIED means the process exists but belongs to
// another user (counted as alive, conservatively). For handles we can open,
// the exit-code probe distinguishes a running process from one whose pid
// still resolves while terminating. Any other open failure is treated as
// dead; the claim protocol re-checks liveness on every use.
//
// Residual risk accepted at D2 slice 1: PID reuse between the claim write
// and this check is not detected (see Claim).
func pidAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		// The process existed when opened; without an exit code we cannot
		// prove death, so stay conservative.
		return true
	}
	return exitCode == stillActive
}
