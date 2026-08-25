//go:build windows

package presence

import (
	"errors"
	"syscall"
)

// processQueryLimitedInformation is the standard Windows SDK access right
// (0x1000) for minimal existence queries. The stdlib syscall package does
// not export it (golang.org/x/sys/windows does); this file deliberately
// depends on the standard library only.
const processQueryLimitedInformation = 0x1000

// pidAlive reports whether a process with the given pid exists on Windows,
// stdlib-only. OpenProcess probes existence: ERROR_ACCESS_DENIED means
// another user's existing process (alive, conservatively); any other open
// failure is dead. For an opened handle, WaitForSingleObject with zero
// timeout separates running (WAIT_TIMEOUT) from terminating-but-resolvable
// pids (WAIT_OBJECT_0). The record is re-read on every Probe, so a wrong
// verdict is transient.
func pidAlive(pid int) bool {
	handle, err := syscall.OpenProcess(
		processQueryLimitedInformation|syscall.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
	}
	defer syscall.CloseHandle(handle)
	event, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return true // existed when opened; death unproven, stay conservative
	}
	return event == syscall.WAIT_TIMEOUT
}
