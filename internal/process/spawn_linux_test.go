package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// The real-subprocess proofs in this file run only where process groups exist;
// Debian CI is the target, other platforms skip.
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("process-group ownership requires Linux")
	}
}

// waitForFile polls until path exists so tests never guess at spawn timing.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func procExists(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

func waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for procExists(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("%s (%d) still alive after tree termination", what, pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSpawnOwnedQuickChildReapsCleanly keeps the positive case honest: an
// owned child that exits on its own is waited and confirmed exited.
func TestSpawnOwnedQuickChildReapsCleanly(t *testing.T) {
	requireLinux(t)
	cmd, tree, err := Spawn(context.Background(), "sh", []string{"-c", "exit 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait() error = %v; quick child must exit cleanly", err)
	}
	tree.MarkExited()
	select {
	case <-tree.Exited():
	default:
		t.Fatal("MarkExited must close the exit channel immediately")
	}
}

// TestSpawnTerminateKillsGrandchild proves descendant coverage with a real
// tree: the shell child spawns a sleeping grandchild, and one Terminate call
// against the owned group ends both.
func TestSpawnTerminateKillsGrandchild(t *testing.T) {
	requireLinux(t)
	dir := t.TempDir()
	gpidFile := filepath.Join(dir, "grandchild-pid")
	script := "sleep 30 & printf %s $! > " + gpidFile + "\nwait"

	cmd, tree, err := Spawn(context.Background(), "sh", []string{"-c", script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()

	gpid, err := strconv.Atoi(waitForFile(t, gpidFile))
	if err != nil {
		t.Fatalf("grandchild pid file unreadable: %v", err)
	}
	if !procExists(gpid) {
		t.Fatalf("grandchild %d not running before termination", gpid)
	}

	start := time.Now()
	if err := Terminate(tree); err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		_ = exitErr // non-nil error is expected for a killed child
	}
	tree.MarkExited()

	waitGone(t, gpid, "sleeping grandchild")
	waitGone(t, tree.Pid(), "shell child")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("tree termination took %s; whole-tree kill must be prompt", elapsed)
	}
}

// TestSpawnTerminateBeatsSignalIgnoringTree proves escalation reaches the
// hard kill: every member of the tree traps-and-ignores SIGTERM, so only the
// follow-up SIGKILL can end it.
func TestSpawnTerminateBeatsSignalIgnoringTree(t *testing.T) {
	requireLinux(t)
	dir := t.TempDir()
	gpidFile := filepath.Join(dir, "grandchild-pid")
	script := "trap '' TERM\nsleep 30 & printf %s $! > " + gpidFile + "\nwait"

	cmd, tree, err := Spawn(context.Background(), "sh", []string{"-c", script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()

	gpid, err := strconv.Atoi(waitForFile(t, gpidFile))
	if err != nil {
		t.Fatalf("grandchild pid file unreadable: %v", err)
	}

	start := time.Now()
	if err := Terminate(tree); err != nil {
		t.Fatalf("Terminate() error = %v", err)
	}
	_ = cmd.Wait()
	tree.MarkExited()

	waitGone(t, gpid, "signal-ignoring grandchild")
	waitGone(t, tree.Pid(), "signal-ignoring shell child")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hard termination took %s; TERM-ignoring trees must still die promptly", elapsed)
	}
}
