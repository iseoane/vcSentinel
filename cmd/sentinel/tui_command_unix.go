//go:build unix

package main

import "syscall"

// detachSysProcAttr returns the SysProcAttr that detaches a spawned daemon
// from this session on Unix platforms: its own process group (the same
// setpgid convention internal/process uses for owned trees), so the daemon
// survives after the TUI exits and never receives terminal signals meant
// for the foreground program.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
