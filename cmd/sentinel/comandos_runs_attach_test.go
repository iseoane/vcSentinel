package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Ticket 17 slice 1: `runs attach` CLI surface. List mode walks the store
// directly, snapshot mode must survive a REAL daemon socket (wire parity),
// and follow mode settles at the terminal state with exit 0.

// gatedAdapter keeps its run in StateRunning until release closes, giving
// follow mode a stable in-flight target that then settles quickly.
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

// syncBuffer is an io.Writer safe to read while the command goroutine writes,
// so the test can watch the live snapshot before releasing the adapter.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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

// inspectCountingHost wraps a RepositoryHost and counts Inspect calls, so a
// test can know how many follow polls ran without depending on wall-clock
// timing of the 250ms poll interval.
type inspectCountingHost struct {
	execution.RepositoryHost

	mu       sync.Mutex
	inspects int
}

func (h *inspectCountingHost) Inspect(ctx context.Context, request execution.InspectRequest) (execution.Inspection, error) {
	h.mu.Lock()
	h.inspects++
	h.mu.Unlock()
	return h.RepositoryHost.Inspect(ctx, request)
}

func (h *inspectCountingHost) inspectCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inspects
}

// followFixture carries everything the direct runsAttachView loop tests need:
// an output buffer holding the initial rendered snapshot, a counting host,
// and the collector already primed with the initial observation.
type followFixture struct {
	out       *syncBuffer
	host      *inspectCountingHost
	identity  agentrun.Identity
	principal string
	collector *attach.ReplayCollector
	view      attach.RunView
	closeHost func()
}

// startFollowFixture starts one gated running run against a real store,
// takes the initial snapshot through the production observe path, renders it
// exactly like executeRunsAttach does, and returns the fixture.
func startFollowFixture(t *testing.T, release chan struct{}) followFixture {
	t.Helper()
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := startGatedRun(t, worktree, release)
	_, controller, err := buildReadonlyController(worktree)
	if err != nil {
		t.Fatal(err)
	}
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	principal, err := resolveRunsPrincipal()
	if err != nil {
		closeRemote()
		t.Fatal(err)
	}
	counting := &inspectCountingHost{RepositoryHost: host}
	collector := attach.NewReplayCollector(0)
	view, _, err := observeAttachSnapshot(context.Background(), counting, runID, principal, collector)
	if err != nil {
		closeRemote()
		t.Fatal(err)
	}
	out := &syncBuffer{}
	renderRunView(out, view)
	return followFixture{
		out: out, host: counting, identity: runID, principal: principal,
		collector: collector, view: view, closeHost: closeRemote,
	}
}

// TestRunsAttachFollowUnchangedPollDoesNotReprint pins the reprint contract:
// polls whose cursor did not move rebuild an identical view and must not
// reprint the snapshot.
func TestRunsAttachFollowUnchangedPollDoesNotReprint(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	fixture := startFollowFixture(t, release)
	defer fixture.closeHost()
	defer closeRelease()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- followRunsView(ctx, fixture.out, fixture.host, fixture.identity, fixture.principal, fixture.collector, fixture.view)
	}()

	// The initial snapshot consumed one Inspect; two more prove at least two
	// unchanged follow polls completed on a run that cannot change yet.
	deadline := time.Now().Add(10 * time.Second)
	for fixture.host.inspectCount() < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("follow never completed two unchanged polls:\n%s", fixture.out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if renders := strings.Count(fixture.out.String(), "🔎 Run"); renders != 1 {
		t.Fatalf("unchanged polls produced %d render(s), want only the initial one:\n%s", renders, fixture.out.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != runExitSuccess {
			t.Fatalf("follow exit = %d after cancel, want clean success:\n%s", code, fixture.out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("follow loop never returned after cancellation:\n%s", fixture.out.String())
	}
	if renders := strings.Count(fixture.out.String(), "🔎 Run"); renders != 1 {
		t.Fatalf("render count moved to %d across unchanged polls:\n%s", renders, fixture.out.String())
	}
}

// TestRunsAttachFollowContextCancelDetachesCleanly proves the detach path the
// usage text promises: cancelling the loop context exits 0 with the last
// render still showing the live in-flight state.
func TestRunsAttachFollowContextCancelDetachesCleanly(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	fixture := startFollowFixture(t, release)
	defer fixture.closeHost()
	defer closeRelease()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- followRunsView(ctx, fixture.out, fixture.host, fixture.identity, fixture.principal, fixture.collector, fixture.view)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(fixture.out.String(), "state running") {
		if time.Now().After(deadline) {
			t.Fatalf("follow never rendered the running snapshot:\n%s", fixture.out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case code := <-done:
		if code != runExitSuccess {
			t.Fatalf("detached follow exit = %d, want clean success:\n%s", code, fixture.out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("follow loop never returned after the detach signal:\n%s", fixture.out.String())
	}
	final := fixture.out.String()
	if !strings.Contains(final, "state running") || !strings.Contains(final, "⏳ still in flight") {
		t.Fatalf("final render lost the live in-flight state at detach:\n%s", final)
	}
	if renders := strings.Count(final, "🔎 Run"); renders != 1 {
		t.Fatalf("detach produced %d render(s), want the single pre-detach snapshot:\n%s", renders, final)
	}
}

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

// TestRunsAttachFollowSettlesAtTerminalState pins the live loop: initial
// snapshot while running, a full reprint when the run settles, terminal
// freeze, and clean exit 0.
func TestRunsAttachFollowSettlesAtTerminalState(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	runID := startGatedRun(t, worktree, release)
	t.Cleanup(closeRelease)

	out := &syncBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- executeRuns(out, worktree, []string{"attach", "--run", string(runID), "--follow"})
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "state running") {
		if time.Now().After(deadline) {
			t.Fatalf("follow never rendered the running snapshot:\n%s", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}

	closeRelease()
	select {
	case code := <-done:
		if code != runExitSuccess {
			t.Fatalf("follow exit = %d, want clean success:\n%s", code, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("follow loop never returned after the run settled:\n%s", out.String())
	}
	final := out.String()
	if !strings.Contains(final, "state succeeded") || !strings.Contains(final, "🏁 outcome success") {
		t.Fatalf("final render lost the terminal view:\n%s", final)
	}
	// The full snapshot must be re-rendered on change: once while running,
	// again after settlement.
	if renders := strings.Count(final, "🔎 Run"); renders < 2 {
		t.Fatalf("snapshot re-rendered %d time(s), want at least the running and terminal renders:\n%s", renders, final)
	}
}
