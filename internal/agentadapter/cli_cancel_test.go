package agentadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/process"
)

// TestRunCapturedCommandHelperChild is the child side of the real-subprocess
// cancellation proof: it signals readiness by creating the file named in
// VAS_SENTINEL_REVIEW_READY_FILE and then sleeps long enough that only a
// context-driven kill can end it promptly. The environment guard keeps a
// normal `go test` run from treating this as an empty test.
func TestRunCapturedCommandHelperChild(t *testing.T) {
	if os.Getenv("VAS_SENTINEL_REVIEW_CHILD") != "1" {
		return
	}
	if ready := os.Getenv("VAS_SENTINEL_REVIEW_READY_FILE"); ready != "" {
		file, err := os.Create(ready)
		if err == nil {
			_ = file.Close()
		}
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func reviewChildCommand() (name string, args []string) {
	return os.Args[0], []string{"-test.run=^TestRunCapturedCommandHelperChild$", "-test.timeout=10m"}
}

// waitForChildReady polls for the helper child's readiness file so cancel()
// is guaranteed to fire only after the child is actually inside its sleep,
// with no timing guesswork.
func waitForChildReady(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper child never created its readiness file %s", path)
		}
		<-ticker.C
	}
}

// TestRunCapturedCommandKillsChildOnContextCancellation proves with a real
// spawned process that the review execution spawn point is bound to its
// context: canceling the parent context ends the owned child promptly through
// the containment watchdog instead of waiting for it to finish. The test
// stamps a short containment grace so the proof stays fast; production uses
// the controller's escalation budget.
func TestRunCapturedCommandKillsChildOnContextCancellation(t *testing.T) {
	name, args := reviewChildCommand()
	readyFile := filepath.Join(t.TempDir(), "review-child-ready")

	parent, cancel := context.WithCancel(context.Background())
	parent = process.WithContainmentGrace(parent, 200*time.Millisecond)
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		env := append(os.Environ(),
			"VAS_SENTINEL_REVIEW_CHILD=1",
			"VAS_SENTINEL_REVIEW_READY_FILE="+readyFile,
		)
		_, _, err := runCapturedCommand(parent, name, args, env, "", "")
		done <- err
	}()

	waitForChildReady(t, readyFile)
	cancel()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("runCapturedCommand() error = nil, want the killed child to report failure")
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) && !strings.Contains(err.Error(), "kill") {
			t.Fatalf("error = %v, want the canceled child's exit or kill evidence", err)
		}
		if elapsed >= 15*time.Second {
			t.Fatalf("child ran %s; cancellation must kill it far before its own sleep ends", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runCapturedCommand() did not return after cancellation; the child survived its context")
	}
}

// TestRunCapturedCommandReturnsWhenChildExits keeps the positive case honest:
// without cancellation the same spawn point returns normally.
func TestRunCapturedCommandReturnsWhenChildExits(t *testing.T) {
	out, stderr, err := runCapturedCommand(context.Background(),
		os.Args[0], []string{"-test.run=^TestRunCapturedCommandHelperChild$"}, // guard absent: exits immediately
		os.Environ(), "", "")
	if err != nil {
		t.Fatalf("runCapturedCommand() error = %v, stderr %q; uncancelled quick child must succeed", err, stderr)
	}
	_ = out
}
