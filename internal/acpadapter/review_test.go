package acpadapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
)

// reviewFixturePath is a tracked regular file of this repository, relative to
// the worktree root, used as audited path material for the real snapshot
// discipline.
const reviewFixturePath = "internal/acpadapter/identity.go"

// headSha resolves the current HEAD commit so the review tests exercise the
// sanctioned snapshot mechanism against genuinely committed content.
func headSha(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("resolve HEAD for snapshot fixture: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// chdirToRepoRoot pins the test process to the worktree root, matching
// production usage: snapshot callers run from the root and address audited
// paths relative to it.
func chdirToRepoRoot(t *testing.T) {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}
	t.Chdir(strings.TrimSpace(string(out)))
}

// --- prompt passthrough ------------------------------------------------------

func TestEjecutarPromptPassthrough(t *testing.T) {
	a, err := spawnHelper(t, helperModeOK)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.EjecutarPrompt("say PROBE")
	if err != nil {
		t.Fatalf("EjecutarPrompt returned error: %v", err)
	}
	if out != "HELLO FROM FAKE ACPX" {
		t.Errorf("EjecutarPrompt = %q, want normalized chunk text", out)
	}
}

func TestEjecutarPromptMapsNonSuccessToTypedOutcome(t *testing.T) {
	a, err := spawnHelper(t, helperModeTrap)
	if err != nil {
		t.Fatal(err)
	}
	out, runErr := a.EjecutarPrompt("say PROBE")
	if runErr == nil {
		t.Fatal("EjecutarPrompt must surface a classified error for a cancelled turn")
	}
	if out != "" {
		t.Errorf("output = %q, want empty on non-success", out)
	}
	var outcome *OutcomeError
	if !errors.As(runErr, &outcome) {
		t.Fatalf("error type = %T, want *OutcomeError carrying the outcome class", runErr)
	}
	if outcome.Outcome() != agentrun.OutcomeCancellation {
		t.Errorf("Outcome() = %q, want %q", outcome.Outcome(), agentrun.OutcomeCancellation)
	}
}

// --- revision mode: snapshot cwd and cleanup ---------------------------------

func TestEjecutarRevisionSnapshotCwdAndCleanup(t *testing.T) {
	chdirToRepoRoot(t)
	argFile := filepath.Join(t.TempDir(), "argv.txt")
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeOK,
			helperArgFileEnv+"="+argFile,
			// The tail embeds the generated snapshot directory; the exact
			// contract is asserted below from the recorded arguments.
			helperExpectEnv+"=-",
		)
	})

	out, err := a.EjecutarRevision("review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("EjecutarRevision returned error: %v", err)
	}
	if out != "HELLO FROM FAKE ACPX" {
		t.Errorf("EjecutarRevision = %q, want normalized chunk text", out)
	}

	data, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("helper did not record its arguments: %v", err)
	}
	got := strings.Split(string(data), "\n")
	cwdIdx := -1
	for i, arg := range got {
		if arg == "--cwd" {
			cwdIdx = i
			break
		}
	}
	if cwdIdx < 0 {
		t.Fatalf("revision mode must pass --cwd; recorded args %v", got)
	}
	// --cwd sits among the global flags, after --timeout's value and BEFORE
	// the agent token.
	if cwdIdx < 2 || got[cwdIdx-2] != "--timeout" {
		t.Errorf("--cwd placement %v: expected it right after the --timeout value", got)
	}
	if cwdIdx+3 >= len(got) || got[cwdIdx+2] != "claude" || got[cwdIdx+3] != "exec" {
		t.Errorf("--cwd must precede the agent token; recorded args %v", got)
	}
	snapshotDir := got[cwdIdx+1]
	if base := filepath.Base(snapshotDir); !strings.HasPrefix(base, "vas-sentinel-review-") {
		t.Errorf("--cwd value %q is not a review snapshot directory", snapshotDir)
	}
	if _, statErr := os.Stat(snapshotDir); !os.IsNotExist(statErr) {
		t.Errorf("snapshot directory %q survived cleanup; it must be removed when the turn ends", snapshotDir)
	}
}

// --- context cancellation -----------------------------------------------------

func TestReviewWithContextCancelYieldsCanceledClass(t *testing.T) {
	chdirToRepoRoot(t)
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeSleep,
			// The tail embeds a generated snapshot directory; its contract
			// is pinned by TestEjecutarRevisionSnapshotCwdAndCleanup.
			helperExpectEnv+"=-",
		)
	})
	// Short cooperative grace so the containment watchdog fires well within
	// the bounded wait instead of the 6s default budget.
	ctx, cancel := context.WithCancel(process.WithContainmentGrace(context.Background(), 50*time.Millisecond))
	time.AfterFunc(150*time.Millisecond, cancel)

	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		_, err := a.ReviewWithContext(ctx, "review CANCEL", headSha(t), []string{reviewFixturePath})
		done <- outcome{err}
	}()

	select {
	case got := <-done:
		var oe *OutcomeError
		if !errors.As(got.err, &oe) {
			t.Fatalf("error type = %T (%v), want *OutcomeError", got.err, got.err)
		}
		if oe.Outcome() != agentrun.OutcomeCancellation {
			t.Errorf("Outcome() = %q, want canceled outcome class", oe.Outcome())
		}
		if !strings.Contains(oe.Error(), "context canceled") {
			t.Errorf("detail = %q, want it to wrap the context error", oe.Error())
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("ReviewWithContext did not return within the bounded wait")
	}
}

// --- output budget -------------------------------------------------------------

func TestMaxOutputBytesCapTriggersFailure(t *testing.T) {
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.MaxOutputBytes = 64 * 1024 // child floods megabytes; breach is immediate
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeFlood,
			helperExpectEnv+"=-",
		)
	})

	type outcome struct {
		out string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := a.EjecutarPrompt("flood")
		done <- outcome{out, err}
	}()
	select {
	case got := <-done:
		var oe *OutcomeError
		if !errors.As(got.err, &oe) {
			t.Fatalf("error type = %T (%v), want *OutcomeError", got.err, got.err)
		}
		if oe.Outcome() != agentrun.OutcomeFailure {
			t.Errorf("Outcome() = %q, want failure classification on budget breach", oe.Outcome())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not end within the bounded wait after breaching the output cap")
	}
}

// --- process ownership ----------------------------------------------------------

func TestOwnedTreeTransitionsAroundARun(t *testing.T) {
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeSleep,
			// Plain prompt path: no generated directory in the tail, but the
			// scenario contract (ownership transitions) is asserted below.
			helperExpectEnv+"=-",
		)
	})
	if tree := a.OwnedTree(); tree != nil {
		t.Fatalf("OwnedTree before any run = %v, want nil while idle", tree)
	}

	ctx, cancel := context.WithCancel(process.WithContainmentGrace(context.Background(), 50*time.Millisecond))
	defer cancel()

	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		_, err := a.Run(ctx, "hang")
		done <- outcome{err}
	}()

	// The tree must become observable while the child runs...
	deadline := time.Now().Add(5 * time.Second)
	for a.OwnedTree() == nil {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("OwnedTree never became non-nil during an active run")
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case got := <-done:
		var oe *OutcomeError
		if !errors.As(got.err, &oe) {
			t.Fatalf("run error type = %T (%v), want *OutcomeError", got.err, got.err)
		}
		if oe.Outcome() != agentrun.OutcomeCancellation {
			t.Errorf("Outcome() = %q, want cancellation after ctx cancel", oe.Outcome())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return within the bounded wait after cancellation")
	}

	// ...and fall back to nil once the run settled.
	deadline = time.Now().Add(5 * time.Second)
	for a.OwnedTree() != nil {
		if time.Now().After(deadline) {
			t.Fatal("OwnedTree stayed non-nil after the run finished")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
