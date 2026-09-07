//go:build windows

package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachSysProcAttr returns the SysProcAttr that detaches a spawned daemon
// from this session on Windows: DETACHED_PROCESS keeps it off this console,
// and CREATE_NEW_PROCESS_GROUP isolates it from Ctrl+C events delivered to
// the foreground group.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
