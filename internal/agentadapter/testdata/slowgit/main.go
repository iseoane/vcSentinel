package main

// Test helper for internal/agentadapter: a transparent "git" stand-in that
// sleeps for a configured duration and then execs the real git binary with
// the exact same arguments, working directory, and standard streams.
//
// Compiled and placed ahead of the real git on PATH, it lets a test observe
// whether a context passed into snapshot creation actually bounds git
// invocations: killed mid-sleep by an expiring context means the budget
// applies; completing its sleep and forwarding to real git successfully
// means it does not.
//
// VCSENTINEL_TEST_REAL_GIT names the real git binary to forward to.
// VCSENTINEL_TEST_GIT_SLEEP_MS sets the sleep duration in milliseconds
// (0 if absent or invalid).
import (
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	sleepMS := 0
	if raw := os.Getenv("VCSENTINEL_TEST_GIT_SLEEP_MS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			sleepMS = n
		}
	}
	time.Sleep(time.Duration(sleepMS) * time.Millisecond)

	realGit := os.Getenv("VCSENTINEL_TEST_REAL_GIT")
	if realGit == "" {
		realGit = "git"
	}
	cmd := exec.Command(realGit, os.Args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}
