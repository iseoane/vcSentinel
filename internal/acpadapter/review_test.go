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

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/process"
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

func TestRunPromptPassthrough(t *testing.T) {
	a := spawnHelper(t, helperModeOK)
	out, err := a.RunPrompt("say PROBE")
	if err != nil {
		t.Fatalf("RunPrompt returned error: %v", err)
	}
	if out != "HELLO FROM FAKE ACPX" {
		t.Errorf("RunPrompt = %q, want normalized chunk text", out)
	}
}
func TestRunRetainsObservedAndConfiguredEffortSeparately(t *testing.T) {
	a := spawnHelper(t, helperModeOK)
	result, err := a.Run(context.Background(), "say PROBE")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.ObservedEffort != "wire-high" {
		t.Fatalf("observed effort = %q, want wire-high from the ACP stream", result.ObservedEffort)
	}
	if result.RequestedEffort != "high" {
		t.Fatalf("requested effort = %q, want configured high", result.RequestedEffort)
	}
	if result.AdapterObservation().Effort != "wire-high" ||
		result.AdapterObservation().RequestedEffort != "high" {
		t.Fatalf("adapter observation lost effort provenance: %+v", result.AdapterObservation())
	}
}

func TestRunErrorAndPreSpawnResultsRetainConfiguredEffort(t *testing.T) {
	t.Run("terminal provider error", func(t *testing.T) {
		a := spawnHelper(t, helperModeFailQuiet)
		result, err := a.Run(context.Background(), "fail")
		if err == nil {
			t.Fatal("Run returned nil error for provider failure")
		}
		if result.RequestedEffort != "high" || result.ObservedEffort != "" {
			t.Fatalf("error result effort = observed %q/requested %q, want empty observed and configured high", result.ObservedEffort, result.RequestedEffort)
		}
	})

	t.Run("pre-spawn failure", func(t *testing.T) {
		a, err := NewAcpx(Config{
			Launcher: []string{filepath.Join(t.TempDir(), "missing-acpx")},
			Agent:    "claude",
			Model:    "fallback-model",
			Effort:   "high",
		})
		if err != nil {
			t.Fatal(err)
		}
		result, runErr := a.Run(context.Background(), "never launched")
		if runErr == nil {
			t.Fatal("Run returned nil error for missing launcher")
		}
		var outcome *OutcomeError
		if !errors.As(runErr, &outcome) || outcome.Outcome() != agentrun.OutcomeProcessError {
			t.Fatalf("pre-spawn error = %T/%v, want typed process error", runErr, runErr)
		}
		if result.Agent != "claude" || result.RequestedModel != "fallback-model" ||
			result.RequestedEffort != "high" || result.ObservedEffort != "" {
			t.Fatalf("pre-spawn result = %+v, want configured declarations and no observed effort", result)
		}
	})
}

func TestRunTerminalSuccessWithNonZeroExitReturnsProcessFailure(t *testing.T) {
	a := spawnHelper(t, helperModeEndTurnExit)
	result, err := a.Run(context.Background(), "say PROBE")
	if err == nil {
		t.Fatal("Run returned nil error after non-zero child exit")
	}
	var outcome *OutcomeError
	if !errors.As(err, &outcome) {
		t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
	}
	if outcome.Outcome() != agentrun.OutcomeProcessError {
		t.Fatalf("Outcome() = %q, want process error", outcome.Outcome())
	}
	if result.StopReason != "end_turn" || result.Output != "PARTIAL" {
		t.Fatalf("err=%v result = %+v, want terminal and partial observations preserved", err, result)
	}
}

func TestRunPromptMapsNonSuccessToTypedOutcome(t *testing.T) {
	a := spawnHelper(t, helperModeTrap)
	out, runErr := a.RunPrompt("say PROBE")
	if runErr == nil {
		t.Fatal("RunPrompt must surface a classified error for a cancelled turn")
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

// --- revision mode: retained snapshot cwd ------------------------------------

// TestRunReviewRunsInRetainedSnapshotCwd proves the revision shape: the
// provider runs with --cwd pointed at the published shared snapshot for the
// audited SHA — a usable review snapshot carrying the committed audited
// fixture — and the caller-owned cleanup only releases the lease, so the
// validated snapshot remains on disk for the next invocation auditing the
// same commit.
func TestRunReviewRunsInRetainedSnapshotCwd(t *testing.T) {
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

	auditedSha := headSha(t)
	out, err := a.RunReview("review SNAPSHOT", auditedSha, []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("RunReview returned error: %v", err)
	}
	if out != "HELLO FROM FAKE ACPX" {
		t.Errorf("RunReview = %q, want normalized chunk text", out)
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
	// --cwd is the published shared snapshot FOR THE AUDITED SHA, not the
	// live repository: its name is the SHA storage key and it lives directly
	// under the shared snapshot store root, whose directory name carries the
	// vas-sentinel-snapshots prefix on every supported platform (per-UID
	// suffixed on Linux, plain under the user's temp location on Windows).
	if base := filepath.Base(snapshotDir); base != "sha-"+auditedSha {
		t.Errorf("--cwd %q is not the published snapshot for the audited SHA %s", snapshotDir, auditedSha)
	}
	storeRoot := filepath.Dir(snapshotDir)
	if parent := filepath.Dir(storeRoot); parent != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(storeRoot), "vas-sentinel-snapshots") {
		t.Errorf("--cwd %q is not under the shared snapshot store root", snapshotDir)
	}
	// Cleanup is an idempotent lease release, never a per-call deletion: the
	// published snapshot for the SHA is retained after the turn, and the
	// stale reaper owns its removal.
	info, statErr := os.Stat(snapshotDir)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("snapshot directory %q did not survive the turn: %v", snapshotDir, statErr)
	}
	// The cwd is a usable review snapshot: it carries the committed audited
	// fixture the revision was addressed to.
	if fixture, readErr := os.ReadFile(filepath.Join(snapshotDir, reviewFixturePath)); readErr != nil || len(fixture) == 0 {
		t.Fatalf("snapshot %q does not carry the audited fixture: %v", snapshotDir, readErr)
	}
}

// --- context cancellation -----------------------------------------------------

func TestReviewWithContextCancelYieldsCanceledClass(t *testing.T) {
	chdirToRepoRoot(t)
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeSleep,
			// The tail embeds a generated snapshot directory; its contract
			// is pinned by TestRunReviewRunsInRetainedSnapshotCwd.
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

// TestReviewWithContextSnapshotCancelYieldsCanceledClass is the deterministic
// counterpart of TestReviewWithContextCancelYieldsCanceledClass: instead of
// racing a real 150ms timer against a real spawned process, it cancels the
// context BEFORE ReviewWithContextResult is ever called, so the failure is
// pinned to the snapshot-creation error path (reviewsnapshot.Create returns
// immediately on an already-canceled context, before any acpx process is
// spawned). No provider process runs, so there is nothing to race.
func TestReviewWithContextSnapshotCancelYieldsCanceledClass(t *testing.T) {
	chdirToRepoRoot(t)
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv, helperModeEnv+"="+helperModeOK)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.ReviewWithContext(ctx, "review CANCEL", headSha(t), []string{reviewFixturePath})

	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
	}
	if oe.Outcome() != agentrun.OutcomeCancellation {
		t.Errorf("Outcome() = %q, want canceled outcome class", oe.Outcome())
	}
	if !strings.Contains(oe.Error(), "context canceled") {
		t.Errorf("detail = %q, want it to wrap the context error", oe.Error())
	}
}

// TestReviewWithContextSnapshotDeadlineYieldsTimeoutClass is the deadline
// counterpart: an already-expired deadline hitting the snapshot-creation
// path must classify as a timeout, never a cancellation, exactly like every
// other caller-context-ended path in this package.
func TestReviewWithContextSnapshotDeadlineYieldsTimeoutClass(t *testing.T) {
	chdirToRepoRoot(t)
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv, helperModeEnv+"="+helperModeOK)
	})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := a.ReviewWithContext(ctx, "review DEADLINE", headSha(t), []string{reviewFixturePath})

	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
	}
	if oe.Outcome() != agentrun.OutcomeTimeout {
		t.Errorf("Outcome() = %q, want timeout outcome class", oe.Outcome())
	}
	if !strings.Contains(oe.Error(), "deadline exceeded") {
		t.Errorf("detail = %q, want it to wrap the deadline error", oe.Error())
	}
}

// TestRunSpawnFailureAfterCallerCancelYieldsCanceledClass is the deterministic
// closure of the SECOND site the truncated-turn defect could surface at: the
// caller's context can die AFTER the snapshot has already succeeded but
// before (or during) process.Spawn. Go's os/exec checks ctx.Err() inside
// Start() and returns it immediately when the context is already done, so
// canceling parent before calling run — with no timer, no sleeping helper
// process, nothing to race — deterministically reproduces exactly that
// window without depending on the 150ms integration test's timing.
func TestRunSpawnFailureAfterCallerCancelYieldsCanceledClass(t *testing.T) {
	chdirToRepoRoot(t)
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv, helperModeEnv+"="+helperModeOK)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.run(ctx, a.buildArgs("never spawned", t.TempDir()))

	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
	}
	if oe.Outcome() != agentrun.OutcomeCancellation {
		t.Errorf("Outcome() = %q, want canceled outcome class", oe.Outcome())
	}
	if !strings.Contains(oe.Error(), "context canceled") {
		t.Errorf("detail = %q, want it to wrap the context error", oe.Error())
	}
}

// TestRunSpawnFailureAfterCallerDeadlineYieldsTimeoutClass is the deadline
// counterpart: a spawn failure caused by an already-expired caller deadline
// must classify as a timeout, never a cancellation, mirroring the
// distinction callerContextOutcome maintains at every other call site in
// this package.
func TestRunSpawnFailureAfterCallerDeadlineYieldsTimeoutClass(t *testing.T) {
	chdirToRepoRoot(t)
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv, helperModeEnv+"="+helperModeOK)
	})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := a.run(ctx, a.buildArgs("never spawned", t.TempDir()))

	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
	}
	if oe.Outcome() != agentrun.OutcomeTimeout {
		t.Errorf("Outcome() = %q, want timeout outcome class", oe.Outcome())
	}
	if !strings.Contains(oe.Error(), "deadline exceeded") {
		t.Errorf("detail = %q, want it to wrap the deadline error", oe.Error())
	}
}

// TestReviewWithCallerDeadlineYieldsTimeoutClass separates the two ways a
// caller context ends. An expired deadline is budget exhaustion, so recording
// it as a cancellation would file a timeout under the class reserved for a
// deliberate stop and make the failure metrics describe the wrong event.
func TestReviewWithCallerDeadlineYieldsTimeoutClass(t *testing.T) {
	chdirToRepoRoot(t)
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeSleep,
			helperExpectEnv+"=-",
		)
	})
	// The child sleeps for a minute, so the deadline decides the outcome. It is
	// deliberately wide: a margin measured in seconds keeps a slow spawn on a
	// loaded runner from failing before the deadline and turning this into a
	// process error, which is the neighbouring class this test exists to keep
	// separate.
	ctx, cancel := context.WithTimeout(
		process.WithContainmentGrace(context.Background(), 50*time.Millisecond), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := a.ReviewWithContext(ctx, "review DEADLINE", headSha(t), []string{reviewFixturePath})
		done <- err
	}()

	select {
	case err := <-done:
		var oe *OutcomeError
		if !errors.As(err, &oe) {
			t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
		}
		if oe.Outcome() != agentrun.OutcomeTimeout {
			t.Errorf("Outcome() = %q, want the timeout class for an expired caller deadline", oe.Outcome())
		}
		if !strings.Contains(oe.Error(), "context deadline exceeded") {
			t.Errorf("detail = %q, want it to wrap the context error", oe.Error())
		}
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatal("ReviewWithContext did not return within the bounded wait")
	}
}

// TestRunSpawnFailureConcurrentWithCallerCancelStaysProcessError pins the
// other direction of the truncated-turn spawn-classification fix: a launch
// that fails for its OWN reason (missing binary, permission denied, ...)
// must stay a process error even when the caller's context happens to have
// ended around the same moment. Go's os/exec resolves the executable and
// stores any lookup failure BEFORE it ever checks ctx.Done() in Start(), so
// an already-canceled context here does not change which error Start()
// returns — the launcher genuinely never existed. A classifier that infers
// the outcome class from parent.Err() alone (instead of inspecting whether
// the spawn error IS the context ending) would misreport this as a
// cancellation, hiding a broken installation behind "canceled".
func TestRunSpawnFailureConcurrentWithCallerCancelStaysProcessError(t *testing.T) {
	// A launcher NAME (no path separator) that is not on PATH goes through
	// os/exec's LookPath resolution, whose failure Start() reports BEFORE it
	// ever checks ctx.Done() — exactly the case this fix must not
	// misclassify. An ABSOLUTE path to a missing file, by contrast, is never
	// looked up ahead of time and would hit the ctx.Done() check first,
	// defeating this test.
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.Launcher = []string{"vas-sentinel-missing-acpx-launcher"}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.run(ctx, a.buildArgs("never spawned", t.TempDir()))

	var oe *OutcomeError
	if !errors.As(err, &oe) {
		t.Fatalf("error type = %T (%v), want *OutcomeError", err, err)
	}
	if oe.Outcome() != agentrun.OutcomeProcessError {
		t.Errorf("Outcome() = %q, want process error even though the caller context had already ended", oe.Outcome())
	}
	if strings.Contains(oe.Error(), "canceled") || strings.Contains(oe.Error(), "cancelled") {
		t.Errorf("detail = %q, must not report a genuine launch failure as a cancellation", oe.Error())
	}
}

// --- output budget -------------------------------------------------------------

func TestMaxOutputBytesCapTriggersFailure(t *testing.T) {
	const capBytes = 64 * 1024
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.MaxOutputBytes = capBytes // child floods megabytes; breach is immediate
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeFlood,
			helperExpectEnv+"=-",
		)
	})

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := a.Run(context.Background(), "flood")
		done <- outcome{res, err}
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
		if len(got.res.RawStream) > capBytes {
			t.Errorf("RawStream = %d bytes, want at most the %d-byte budget (exact retention)", len(got.res.RawStream), capBytes)
		}
		if len(got.res.RawStream) == 0 {
			t.Error("RawStream empty; the drained prefix up to the breach must be retained")
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

// waitForLiveTrees polls until the adapter's owned-tree registry holds at
// least want registered trees, failing on the bounded deadline.
func waitForLiveTrees(t *testing.T, a *AcpxAdapter, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.treeMu.Lock()
		got := len(a.liveTrees)
		a.treeMu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned-tree registry = %d live trees, want at least %d", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestOwnedTreeRegistryKeepsSiblingsVisibleUntilBothFinish pins the
// concurrency contract of the owned-tree registry: while two runs execute
// concurrently on one adapter, abort escalation sees a live tree until BOTH
// complete, the most recently started run is preferred, and completing one
// run never hides its still-running sibling (a single active slot would do
// exactly that).
func TestOwnedTreeRegistryKeepsSiblingsVisibleUntilBothFinish(t *testing.T) {
	a := spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv,
			helperModeEnv+"="+helperModeSleep,
			helperExpectEnv+"=-",
		)
	})
	if tree := a.OwnedTree(); tree != nil {
		t.Fatalf("OwnedTree before any run = %v, want nil while idle", tree)
	}

	type outcome struct {
		tag string
		err error
	}
	done := make(chan outcome, 2)

	ctx1, cancel1 := context.WithCancel(process.WithContainmentGrace(context.Background(), 50*time.Millisecond))
	defer cancel1()
	go func() {
		_, err := a.Run(ctx1, "hang-1")
		done <- outcome{"first", err}
	}()
	waitForLiveTrees(t, a, 1)
	first := a.OwnedTree()
	if first == nil {
		t.Fatal("registry reports a live entry but OwnedTree returned nil")
	}

	ctx2, cancel2 := context.WithCancel(process.WithContainmentGrace(context.Background(), 50*time.Millisecond))
	defer cancel2()
	go func() {
		_, err := a.Run(ctx2, "hang-2")
		done <- outcome{"second", err}
	}()
	waitForLiveTrees(t, a, 2)

	second := a.OwnedTree()
	if second == nil {
		t.Fatal("OwnedTree nil while two runs are live")
	}
	if second == first {
		t.Error("OwnedTree must prefer the most recently started live tree")
	}

	awaitCanceled := func(tag string) {
		t.Helper()
		select {
		case got := <-done:
			if got.tag != tag {
				t.Fatalf("completion order broken: got %q, want %q", got.tag, tag)
			}
			var oe *OutcomeError
			if !errors.As(got.err, &oe) {
				t.Fatalf("%s run error type = %T (%v), want *OutcomeError", tag, got.err, got.err)
			}
			if oe.Outcome() != agentrun.OutcomeCancellation {
				t.Errorf("%s run Outcome() = %q, want cancellation after ctx cancel", tag, oe.Outcome())
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s run did not return within the bounded wait after cancellation", tag)
		}
	}

	// Completing the FIRST run must not hide the still-running sibling.
	cancel1()
	awaitCanceled("first")
	if tree := a.OwnedTree(); tree == nil || tree != second {
		t.Fatalf("OwnedTree after first completion = %v, want the still-live sibling %v", tree, second)
	}

	// Only when BOTH runs settle does the registry drain back to nil.
	cancel2()
	awaitCanceled("second")
	deadline := time.Now().Add(5 * time.Second)
	for a.OwnedTree() != nil {
		if time.Now().After(deadline) {
			t.Fatal("OwnedTree stayed non-nil after both runs finished")
		}
		time.Sleep(2 * time.Millisecond)
	}
}
