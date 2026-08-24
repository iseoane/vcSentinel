package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The containment watchdog tests drive real owned trees through the same
// test-binary helper pattern the adapter packages use, so the contract holds
// identically on every supported platform: a long-lived child that only ends
// through the watchdog, and a quick child that exits inside the grace window.

const (
	containHelperEnv     = "GO_WANT_PROCESS_CONTAIN_HELPER"
	containHelperModeEnv = "PROCESS_CONTAIN_HELPER_MODE"
	// containHelperSleepLong stands in for a stuck backend that never exits
	// on its own; only a hard termination can end it.
	containHelperSleepLong = "sleep-long"
	// containHelperExitSoon exits cooperatively well inside any grace budget.
	containHelperExitSoon = "exit-soon"
)

// TestContainHelperProcess is the child process for the watchdog tests.
func TestContainHelperProcess(t *testing.T) {
	if os.Getenv(containHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	if os.Getenv(containHelperModeEnv) == containHelperExitSoon {
		time.Sleep(300 * time.Millisecond)
	} else {
		time.Sleep(60 * time.Second)
	}
	os.Exit(0)
}

func spawnContainHelper(t *testing.T, ctx context.Context, mode string) (*exec.Cmd, *Tree) {
	t.Helper()
	cmd, tree, err := Spawn(ctx, os.Args[0], []string{"-test.run=^TestContainHelperProcess$", "--"}, func(c *exec.Cmd) {
		c.Env = append(os.Environ(),
			containHelperEnv+"=1",
			containHelperModeEnv+"="+mode,
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	return cmd, tree
}

// TestContainAfterCancellationFiresTerminateAfterDeadline pins the hard
// branch: once the context stays canceled past grace+margin, the watchdog
// must end a tree that ignores its context. The test never calls Terminate
// itself; if the watchdog failed to fire, Wait would block until the helper's
// 60s sleep ran out and the bounded wait below fails first.
func TestContainAfterCancellationFiresTerminateAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(WithContainmentGrace(context.Background(), 50*time.Millisecond))
	cmd, tree := spawnContainHelper(t, ctx, containHelperSleepLong)
	defer tree.Release()
	go ContainAfterCancellation(ctx, tree)

	cancel()
	start := time.Now()
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		tree.MarkExited()
		elapsed := time.Since(start)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("Wait error = %v, want an exit error from the watchdog termination", err)
		}
		if elapsed < time.Second {
			t.Fatalf("tree died %s after cancellation; the watchdog must respect the full grace+margin deadline", elapsed)
		}
		if elapsed > 10*time.Second {
			t.Fatalf("tree outlived its canceled context by %s; the watchdog must hard-terminate at the deadline", elapsed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("watchdog did not terminate the tree after the containment deadline")
	}
}

// TestContainAfterCancellationLetsCooperativeExitWin pins the soft branch: a
// tree that exits on its own inside the grace+margin window is never
// signaled. The parent context IS canceled first, so os/exec's Wait reports
// the wrapped context error, but the helper's own exit code stays zero: a
// watchdog termination would surface as a signal/job kill instead. The grace
// budget is generous (2s) because the helper is this same test binary and
// its startup cost under -race can approach a second.
func TestContainAfterCancellationLetsCooperativeExitWin(t *testing.T) {
	ctx, cancel := context.WithCancel(WithContainmentGrace(context.Background(), 2*time.Second))
	defer cancel()
	cmd, tree := spawnContainHelper(t, ctx, containHelperExitSoon)
	defer tree.Release()
	go ContainAfterCancellation(ctx, tree)
	cancel()

	waitErr := cmd.Wait()
	tree.MarkExited()
	if cmd.ProcessState == nil {
		t.Fatal("ProcessState missing after Wait")
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("helper exited %d (wait error %v); a child exiting inside the grace window must not be terminated", code, waitErr)
	}
}

// TestContainAfterCancellationDoesNotFireWhenTreeExitsFirst pins the other
// select arm: when the tree exits before the context ever fires, the
// watchdog returns without touching anything, and Wait reports a clean exit.
func TestContainAfterCancellationDoesNotFireWhenTreeExitsFirst(t *testing.T) {
	ctx, cancel := context.WithCancel(WithContainmentGrace(context.Background(), 50*time.Millisecond))
	defer cancel()
	cmd, tree := spawnContainHelper(t, ctx, containHelperExitSoon)
	defer tree.Release()
	go ContainAfterCancellation(ctx, tree)

	if err := cmd.Wait(); err != nil {
		t.Fatalf("Wait error = %v; an uncancelled quick child must exit cleanly", err)
	}
	tree.MarkExited()
}

// TestContainAfterCancellationNilContextIsNoOp pins the unified nil-context
// guard inherited from the original cli_review_context copy: a nil context
// returns immediately without touching the live tree.
func TestContainAfterCancellationNilContextIsNoOp(t *testing.T) {
	cmd, tree := spawnContainHelper(t, context.Background(), containHelperSleepLong)
	defer func() {
		_ = Terminate(tree)
		_ = cmd.Wait()
		tree.MarkExited()
		tree.Release()
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ContainAfterCancellation(nil, tree)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("nil context must disable the watchdog immediately")
	}
	if !tree.Alive() {
		t.Fatal("tree was signaled despite a nil context")
	}
}
