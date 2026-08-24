package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/tui"
)

// Ticket 17: `runs attach` CLI surface. List mode walks the store directly,
// snapshot mode must survive a REAL daemon socket (wire parity), and --follow
// hands the observed run to the Bubble Tea program through one stubbed seam
// — driving a real terminal headlessly is not possible, so the construction
// contract is pinned instead of the render loop (the loop itself is covered
// by internal/tui model tests).

// gatedAdapter keeps its run in StateRunning until release closes, giving
// tests a stable in-flight target.
type gatedAdapter struct {
	release chan struct{}
}

func (a gatedAdapter) Execute(ctx context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	select {
	case <-a.release:
		return execution.AdapterResult{Output: "released"}, nil
	case <-ctx.Done():
		return execution.AdapterResult{}, ctx.Err()
	}
}

// startGatedRun admits one run through a real controller against the
// worktree's common-dir store without waiting for it to settle.
func startGatedRun(t *testing.T, worktree string, release chan struct{}) agentrun.Identity {
	t.Helper()
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	controller := execution.NewController(store.NuevoStore(commonDir), gatedAdapter{release: release})
	request := agentrun.NewRunRequest(
		agentrun.Candidate("candidate:attach-follow"),
		agentrun.Prompt("follow prompt"), nil)
	handle, err := controller.Start(context.Background(), request, store.RunPolicy{ID: "policy:test"})
	if err != nil {
		t.Fatal(err)
	}
	return handle.RunID
}

// TestRunsAttachFollowConstructsTUIModel pins the slice 2 seam contract:
// --follow observes the initial snapshot through the shared pipeline, builds
// the TUI model with the right identity, principal, and a working host
// provider, and exits cleanly when the program ends.
func TestRunsAttachFollowConstructsTUIModel(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	runID := startGatedRun(t, worktree, release)

	var captured tui.Model
	original := startAttachProgram
	startAttachProgram = func(model tui.Model) (tui.Model, error) {
		captured = model
		return model, nil
	}
	defer func() { startAttachProgram = original }()

	var out bytes.Buffer
	if code := executeRuns(&out, worktree, []string{"attach", "--run", string(runID), "--follow"}); code != runExitSuccess {
		t.Fatalf("follow exit = %d, want clean success:\n%s", code, out.String())
	}
	if captured.RunID() != runID {
		t.Fatalf("TUI model attached to %q, want %q", captured.RunID(), runID)
	}
	if captured.Principal() == "" {
		t.Fatalf("TUI model carries no operating principal")
	}
	if captured.Provider() == nil {
		t.Fatalf("TUI model carries no host provider")
	}
	if host, err := captured.Provider()(); err != nil || host == nil {
		t.Fatalf("provider did not resolve a host: host=%v err=%v", host, err)
	}
	if got := captured.Snapshot().State; got != agentrun.StateRunning {
		t.Fatalf("initial snapshot state = %q, want the live running state", got)
	}
	if captured.Snapshot().RunID != string(runID) {
		t.Fatalf("initial snapshot run = %q, want %q", captured.Snapshot().RunID, string(runID))
	}
}

// TestRunsAttachFollowProgramFailureExitsInfrastructure pins the honest
// failure path at the seam: a session that ends abnormally reports
// infrastructure failure instead of pretending a clean detach happened.
// (Reconnect exhaustion itself is covered by internal/tui model tests; this
// pins the CLI's mapping of a failed session to its exit code.)
func TestRunsAttachFollowProgramFailureExitsInfrastructure(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := seedDurableRun(t, worktree, "attach-lost",
		runsSeedAdapter{result: execution.AdapterResult{Output: "done"}})

	original := startAttachProgram
	startAttachProgram = func(model tui.Model) (tui.Model, error) {
		return model, errAttachSimulatedFailure
	}
	defer func() { startAttachProgram = original }()

	var out bytes.Buffer
	if code := executeRuns(&out, worktree, []string{"attach", "--run", string(runID), "--follow"}); code != runExitInfrastructure {
		t.Fatalf("failed session exit = %d, want %d:\n%s", code, runExitInfrastructure, out.String())
	}
	if !strings.Contains(out.String(), "❌ attach session") {
		t.Fatalf("failure output missing the explicit error line:\n%s", out.String())
	}
}

var errAttachSimulatedFailure = errors.New("simulated attach program failure")

func TestRunsAttachListShowsOnlyNonTerminalRuns(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	backing := store.NuevoStore(commonDir)
	awaiting := appendReconciledFixtureStream(t, backing, "candidate:attach-awaiting", awaitingExtra())
	settled := appendReconciledFixtureStream(t, backing, "candidate:attach-settled", successExtra())

	var out bytes.Buffer
	if code := executeRuns(&out, worktree, []string{"attach"}); code != runExitSuccess {
		t.Fatalf("attach list exit = %d, output:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, string(awaiting)) || !strings.Contains(text, "state=awaiting_decision") {
		t.Fatalf("listing did not surface the non-terminal run:\n%s", text)
	}
	if strings.Contains(text, string(settled)) {
		t.Fatalf("settled run %s leaked into the attach picker:\n%s", settled, text)
	}
}

func TestRunsAttachListExitsZeroOnEmptyStore(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)

	var out bytes.Buffer
	if code := executeRuns(&out, worktree, []string{"attach"}); code != runExitSuccess {
		t.Fatalf("attach list exit = %d on empty store, want 0, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "No non-terminal durable runs are available to attach.") {
		t.Fatalf("empty listing = %q, want the explicit nothing-to-attach line", out.String())
	}
}

// TestRunsAttachSnapshotThroughDaemonSocket proves wire parity: with a live
// daemon announcing endpoint.json, the snapshot flows through the REMOTE
// host — Inspect plus paged Subscribe over the framed transport — and still
// renders the identical observation report.
func TestRunsAttachSnapshotThroughDaemonSocket(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := seedDurableRun(t, worktree, "attach-wire",
		runsSeedAdapter{result: execution.AdapterResult{Output: "done"}})
	startRunsTestDaemon(t, worktree)

	var out bytes.Buffer
	if code := executeRuns(&out, worktree, []string{"attach", "--run", string(runID)}); code != runExitSuccess {
		t.Fatalf("attach snapshot exit = %d, output:\n%s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"🔎 Run " + string(runID),
		"state succeeded",
		"🏁 outcome success",
		"invocations 1, responses 0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("snapshot output lost %q:\n%s", want, text)
		}
	}

	// A mid-stream resume (--after 2) rebuilds from strictly later frames;
	// the projection stays truthful at the stream head.
	out.Reset()
	if code := executeRuns(&out, worktree, []string{"attach", "--run", string(runID), "--after", "2"}); code != runExitSuccess {
		t.Fatalf("attach --after snapshot exit = %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "(sequence 4, revision 4)") {
		t.Fatalf("--after snapshot lost the head position:\n%s", out.String())
	}
}

func TestRunsAttachUnknownRunExitsNotFound(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)

	var out bytes.Buffer
	if code := executeRuns(&out, worktree, []string{"attach", "--run", "missing-run-id"}); code != runExitRunNotFound {
		t.Fatalf("unknown run exit = %d, want %d, output:\n%s", code, runExitRunNotFound, out.String())
	}
}

func TestRunsAttachUsageErrorsExitOne(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	tests := []struct {
		name string
		args []string
	}{
		{"undeclared flag", []string{"attach", "--limit", "5"}},
		{"follow without run", []string{"attach", "--follow"}},
		{"after missing value", []string{"attach", "--after"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := executeRuns(&out, worktree, append([]string{"attach"}, tt.args[1:]...)); code != runExitUsage {
				t.Fatalf("%v exit = %d, want usage 1, output:\n%s", tt.args, code, out.String())
			}
			if !strings.Contains(out.String(), "❌") {
				t.Fatalf("usage rejection printed no explicit error line:\n%s", out.String())
			}
		})
	}
}
