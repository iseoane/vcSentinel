package agentadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// compileSlowGit compiles the testdata/slowgit helper — a transparent "git"
// stand-in that sleeps a configured duration and then forwards to the real
// git — and returns the directory holding it, named so it resolves as "git"
// when that directory is placed ahead of the real git on PATH.
func compileSlowGit(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skips compiling the helper in -short mode")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("resolve real git for the slow-git helper: %v", err)
	}

	dir := t.TempDir()
	name := "git"
	if os.PathSeparator == '\\' {
		name += ".exe"
	}
	exe := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = filepath.Join("testdata", "slowgit")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("could not compile the slow-git helper: %v\n%s", err, output)
	}
	t.Setenv("VAS_SENTINEL_TEST_REAL_GIT", realGit)
	return dir
}

// TestReviewWithContextResultBoundsSnapshotCreationByTheAdapterTimeout pins
// the fix for the truncated-turn defect where the review time budget bounded
// provider execution but not snapshot creation: with a caller context that
// carries no deadline of its own (context.Background(), matching every real
// caller today), a slow or pathological committed tree must not be able to
// consume unbounded wall-clock time before the adapter's own timeout ever
// starts applying.
//
// A "git" stand-in placed ahead of the real git on PATH sleeps for longer
// than the configured adapter timeout before forwarding to the real git.
// Before the fix, snapshot creation ran under the caller's raw (undeadlined)
// context, so the slow git call was never interrupted, completed
// successfully after its full sleep, and the review proceeded to a fresh,
// independently-timed provider budget — reporting success despite the
// combined wall-clock time having already blown through the configured
// timeout. After the fix, the SAME timeout-bounded context governs snapshot
// creation, so the slow git invocation is killed mid-sleep and the call
// fails with an error wrapping context.DeadlineExceeded.
func TestReviewWithContextResultBoundsSnapshotCreationByTheAdapterTimeout(t *testing.T) {
	// Compile both helpers (relative to this package's testdata) before
	// changing the working directory to the repository root: chdirToRepoRoot
	// would otherwise break their relative build paths.
	slowGitDir := compileSlowGit(t)
	binary := compileAgentBinary(t, "claude")
	chdirToRepoRoot(t)
	t.Setenv("VAS_SENTINEL_TEST_GIT_SLEEP_MS", "600")
	t.Setenv("PATH", slowGitDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"audit"}`)
	adapter := CLIAdapter{
		BinaryName: binary,
		Timeout:    150 * time.Millisecond,
	}

	start := time.Now()
	_, err := adapter.ReviewWithContextResult(context.Background(), "review SLOW-SNAPSHOT", headSha(t), []string{reviewFixturePath})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("ReviewWithContextResult() = nil error after %s, want a timeout: the configured %s budget must bound snapshot creation, not just provider execution", elapsed, adapter.Timeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded so a snapshot aborted by the deadline classifies as a timeout, not a cancellation", err)
	}
}
