package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
// worktree's common-dir store without waiting for it to settle. It returns
// the controller so the caller can drain the fixture deterministically
// before teardown (release, settle, quiescence).
func startGatedRun(t *testing.T, worktree string, release chan struct{}) (agentrun.Identity, *execution.Controller) {
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
	return handle.RunID, controller
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
	runID, controller := startGatedRun(t, worktree, release)

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
	if host, err := captured.Provider().Host(); err != nil || host == nil {
		t.Fatalf("provider did not resolve a host: host=%v err=%v", host, err)
	}
	if got := captured.Snapshot().State; got != agentrun.StateRunning {
		t.Fatalf("initial snapshot state = %q, want the live running state", got)
	}
	if captured.Snapshot().RunID != string(runID) {
		t.Fatalf("initial snapshot run = %q, want %q", captured.Snapshot().RunID, string(runID))
	}

	// Teardown drain: the gated worker is still parked inside the adapter
	// when the assertions end; awaiting runs keep their worker alive BY
	// DESIGN. Release it deterministically here (the deferred close stays as
	// a once-guarded safety net), wait for the controller to settle the run
	// terminal, then require store quiescence so no settlement writer races
	// t.TempDir removal — the unlinkat "directory not empty" family.
	releaseOnce.Do(func() { close(release) })
	if _, err := observeUntilSettled(context.Background(), controller, runID); err != nil {
		t.Fatal(err)
	}
	awaitRunQuiescence(t, controller, runID)
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

// attachProviderFakeHost is a no-op execution.RepositoryHost carrying an
// identity plus exchange/release counters, so provider tests can assert
// which host a resolution handed out and whether its teardown ran. Its
// Close implements the same contract as daemon.RemoteHost.Close: teardown
// serializes behind any in-flight exchange (modeled here by the optional
// gate channels), so a release can only be recorded after that exchange has
// completed — never mid-flight.
type attachProviderFakeHost struct {
	id string

	// Gate plumbing for the adversarial lifecycle test: when gateOpen is
	// non-nil, Inspect blocks on it mid-exchange (a long exchange holding
	// the connection lock), announcing entry on entered and completion on
	// drained. Close announces its attempt on releaseAttempted, waits for
	// drained, and only then records the release.
	gateOpen         chan struct{}
	entered          chan struct{}
	drained          chan struct{}
	releaseAttempted chan struct{}
	enteredOnce      sync.Once
	drainedOnce      sync.Once
	attemptedOnce    sync.Once

	mu        sync.Mutex
	exchanges int
	releases  int
}

func (h *attachProviderFakeHost) Start(context.Context, execution.StartRequest) (execution.Handle, error) {
	return execution.Handle{}, nil
}

func (h *attachProviderFakeHost) Inspect(context.Context, execution.InspectRequest) (execution.Inspection, error) {
	h.mu.Lock()
	h.exchanges++
	h.mu.Unlock()
	if h.gateOpen != nil {
		h.enteredOnce.Do(func() { close(h.entered) })
		<-h.gateOpen // mid-exchange hold with the connection lock held
		h.drainedOnce.Do(func() { close(h.drained) })
	}
	return execution.Inspection{}, nil
}

func (h *attachProviderFakeHost) Subscribe(context.Context, execution.SubscribeRequest) (store.EventPage, error) {
	return store.EventPage{}, nil
}

func (h *attachProviderFakeHost) Apply(context.Context, execution.ApplyRequest) (execution.ApplyResult, error) {
	return execution.ApplyResult{}, nil
}

func (h *attachProviderFakeHost) Recover(context.Context, execution.RecoverRequest) (execution.Handle, error) {
	return execution.Handle{}, nil
}

func (h *attachProviderFakeHost) Retry(context.Context, execution.RetryRequest) (execution.Handle, error) {
	return execution.Handle{}, nil
}

// Close implements io.Closer with the RemoteHost ordering: announce the
// attempt, serialize behind the gated in-flight exchange if one is running,
// record the release exactly once.
func (h *attachProviderFakeHost) Close() error {
	if h.releaseAttempted != nil {
		h.attemptedOnce.Do(func() { close(h.releaseAttempted) })
	}
	if h.drained != nil {
		<-h.drained
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releases++
	return nil
}

func (h *attachProviderFakeHost) exchangeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exchanges
}

// teardown is the release callback handed to the provider; it delegates to
// Close so the release counter is identical whichever path invokes it.
func (h *attachProviderFakeHost) teardown() {
	_ = h.Close()
}

func (h *attachProviderFakeHost) releaseCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.releases
}

// TestAttachHostProviderCachesAcrossConcurrentResolutions pins the JD fix:
// the observe and action command goroutines resolve their host concurrently,
// so Host must hand back the SAME healthy host on every resolution without
// releasing anything — neither operation's connection may be closed under an
// in-flight exchange. Replacement happens only after Reset: the next Host
// redials exactly once and tears down the already-failed replaced host, and
// Close still releases whatever the session ends holding.
func TestAttachHostProviderCachesAcrossConcurrentResolutions(t *testing.T) {
	live := &attachProviderFakeHost{id: "live"}
	replacement := &attachProviderFakeHost{id: "replacement"}

	var mu sync.Mutex
	dials := 0
	dial := func() (execution.RepositoryHost, func()) {
		mu.Lock()
		defer mu.Unlock()
		dials++
		if dials > 1 {
			t.Errorf("provider dialed %d times; caching allows exactly one redial per Reset", dials)
			return replacement, func() {}
		}
		return replacement, replacement.teardown
	}
	provider := &attachHostProvider{dial: dial, current: attachHostLease{host: live, release: live.teardown}}

	// Concurrent observe + action resolution pattern: both goroutines resolve
	// while the session is attached.
	const resolvers = 2
	resolved := make([]execution.RepositoryHost, resolvers)
	var wg sync.WaitGroup
	for i := range resolved {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			host, err := provider.Host()
			if err != nil {
				t.Errorf("resolution %d failed: %v", slot, err)
				return
			}
			resolved[slot] = host
		}(i)
	}
	wg.Wait()
	for slot, host := range resolved {
		if host != execution.RepositoryHost(live) {
			t.Fatalf("concurrent resolution %d returned %v, want the cached live host", slot, host)
		}
	}
	// Both exchanges complete against the live double while nothing is ever
	// released underneath them.
	for i := 0; i < resolvers; i++ {
		if _, err := live.Inspect(context.Background(), execution.InspectRequest{}); err != nil {
			t.Fatalf("exchange %d failed: %v", i, err)
		}
	}
	if got := live.exchangeCount(); got != resolvers {
		t.Fatalf("live exchanges = %d, want %d", got, resolvers)
	}
	mu.Lock()
	if dials != 0 {
		mu.Unlock()
		t.Fatalf("healthy-session resolutions dialed %d times, want 0", dials)
	}
	mu.Unlock()
	if got := live.releaseCount(); got != 0 {
		t.Fatalf("live host was released %d times during healthy resolutions, want 0", got)
	}
	if got := replacement.releaseCount(); got != 0 {
		t.Fatalf("untouched replacement released %d times before any Reset", got)
	}

	// Failure/reconnect path: Reset marks the live host dead, and the next
	// resolution redials exactly once, releasing the known-dead host once.
	provider.Reset()
	next, err := provider.Host()
	if err != nil || next != execution.RepositoryHost(replacement) {
		t.Fatalf("post-Reset resolution = %v (err %v), want the freshly dialed replacement", next, err)
	}
	if got := live.releaseCount(); got != 1 {
		t.Fatalf("replaced live host released %d times, want exactly 1", got)
	}
	mu.Lock()
	gotDials := dials
	mu.Unlock()
	if gotDials != 1 {
		t.Fatalf("reconnect redialed %d times, want exactly 1", gotDials)
	}

	// The fresh host is now the cached one: repeated resolutions reuse it
	// without dialing or releasing anything.
	for i := 0; i < 2; i++ {
		host, err := provider.Host()
		if err != nil || host != execution.RepositoryHost(replacement) {
			t.Fatalf("cached resolution %d = %v (err %v), want the replacement", i, host, err)
		}
	}
	if _, err := replacement.Inspect(context.Background(), execution.InspectRequest{}); err != nil {
		t.Fatalf("exchange over the replacement failed: %v", err)
	}
	if got := replacement.exchangeCount(); got != 1 {
		t.Fatalf("replacement exchanges = %d, want 1", got)
	}

	// Program teardown releases everything exactly once and stays idempotent.
	provider.Close()
	if got := replacement.releaseCount(); got != 1 {
		t.Fatalf("teardown released the replacement %d times, want exactly 1", got)
	}
	provider.Close()
	if got := replacement.releaseCount(); got != 1 {
		t.Fatalf("second Close re-released the replacement, want idempotent teardown")
	}
}

// waitSignal fails the test unless ch closes within the budget.
func waitSignal(t *testing.T, ch chan struct{}, timeoutMessage string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(timeoutMessage)
	}
}

// TestAttachHostProviderReleaseWaitsForInFlightExchange pins the adversarial
// sequence the healthy-path test above cannot express: exchange A starts on
// host1 and blocks mid-exchange, a concurrent failing poll poisons it via
// Reset, and the redialing Host resolves host2 while releasing host1 ONLY
// after A completes. The fake's Close mirrors daemon.RemoteHost.Close (it
// serializes behind the in-flight exchange's connection lock), so the test
// proves three structural properties: no release is recorded while A is in
// flight, A's result is delivered intact, and the release lands exactly once
// after completion — with host1 permanently unresolvable either way.
func TestAttachHostProviderReleaseWaitsForInFlightExchange(t *testing.T) {
	live := &attachProviderFakeHost{
		id:               "live",
		gateOpen:         make(chan struct{}),
		entered:          make(chan struct{}),
		drained:          make(chan struct{}),
		releaseAttempted: make(chan struct{}),
	}
	replacement := &attachProviderFakeHost{id: "replacement"}

	var mu sync.Mutex
	dials := 0
	dial := func() (execution.RepositoryHost, func()) {
		mu.Lock()
		defer mu.Unlock()
		dials++
		return replacement, replacement.teardown
	}
	provider := &attachHostProvider{dial: dial, current: attachHostLease{host: live, release: live.teardown}}

	// Exchange A: resolve host1 and block mid-exchange on it.
	exchangeA := make(chan error, 1)
	go func() {
		host, err := provider.Host()
		if err != nil {
			t.Errorf("exchange A resolution failed: %v", err)
			exchangeA <- err
			return
		}
		_, err = host.Inspect(context.Background(), execution.InspectRequest{})
		exchangeA <- err
	}()
	waitSignal(t, live.entered, "exchange A never entered its gated window")

	// A concurrent poll failure poisons host1 while A is still in flight.
	provider.Reset()

	// The redialing resolution must hand out host2 and engage host1's
	// deferred release — which then has to serialize behind A.
	redialed := make(chan execution.RepositoryHost, 1)
	go func() {
		host, err := provider.Host()
		if err != nil {
			t.Errorf("redialing resolution failed: %v", err)
			return
		}
		redialed <- host
	}()
	waitSignal(t, live.releaseAttempted, "deferred release of the poisoned host never engaged")
	if got := live.releaseCount(); got != 0 {
		t.Fatalf("poisoned host recorded %d releases while exchange A was in flight, want 0", got)
	}
	mu.Lock()
	gotDials := dials
	mu.Unlock()
	if gotDials != 1 {
		t.Fatalf("recovery dialed %d times, want exactly 1", gotDials)
	}

	// Completing A unblocks everything: its result arrives intact, the
	// redialing resolution returns host2 (which happens strictly after the
	// deferred release finished), and the release is recorded exactly once.
	close(live.gateOpen)
	var host2 execution.RepositoryHost
	select {
	case host2 = <-redialed:
	case <-time.After(5 * time.Second):
		t.Fatal("redialing resolution never returned after exchange A completed")
	}
	select {
	case err := <-exchangeA:
		if err != nil {
			t.Fatalf("exchange A failed: %v — the deferred release harmed an in-flight exchange", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exchange A never delivered its result after its gate opened")
	}
	if host2 != execution.RepositoryHost(replacement) {
		t.Fatalf("post-Reset resolution = %v, want the freshly dialed replacement", host2)
	}
	if got := live.releaseCount(); got != 1 {
		t.Fatalf("poisoned host released %d times post-completion, want exactly 1", got)
	}

	// Structural poisoning: host1 can never be handed out again, and the
	// fresh host is cached without further dials.
	for i := 0; i < 2; i++ {
		host, err := provider.Host()
		if err != nil || host != execution.RepositoryHost(replacement) {
			t.Fatalf("post-recovery resolution %d = %v (err %v), want the cached replacement", i, host, err)
		}
	}
	mu.Lock()
	gotDials = dials
	mu.Unlock()
	if gotDials != 1 {
		t.Fatalf("healthy resolutions re-dialed: %d dials total, want 1", gotDials)
	}
	if got := live.exchangeCount(); got != 1 {
		t.Fatalf("host1 exchanges = %d, want exactly the single completed exchange A", got)
	}
}
