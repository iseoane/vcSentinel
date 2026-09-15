//go:build windows

package git

import (
	"errors"

	"golang.org/x/sys/windows"
)

const snapshotStillActive = 259

// processAlive treats uncertain results as alive. PID reuse can therefore only
// produce a false ALIVE: the orphan survives to the ordinary 24-hour rule,
// which is harmless, never a false DEAD because dead requires no process to
// hold that PID. That asymmetry needs no process-start-time comparison.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false
		}
		return true
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == snapshotStillActive
}
