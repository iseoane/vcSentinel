package presence

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// TestPresenceDeadChild is the helper-process entry point used to obtain a
// provably dead PID: re-executed with VAS_PRESENCE_HELPER_EXIT=1 it returns
// immediately, so its pid is fully reaped before the parent probes it.
func TestPresenceDeadChild(t *testing.T) {
	if os.Getenv("VAS_PRESENCE_HELPER_EXIT") != "1" {
		t.Skip("helper process for dead-pid probing only")
	}
}

// deadPID returns the pid of an exited-and-reaped child of this binary:
// reaping guarantees ESRCH on Unix and a released process object on Windows,
// so no platform skip is needed.
func deadPID(t *testing.T) int {
	cmd := exec.Command(os.Args[0], "-test.run=TestPresenceDeadChild$")
	cmd.Env = append(os.Environ(), "VAS_PRESENCE_HELPER_EXIT=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper child: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

// endpointRecord renders the exact JSON shape internal/daemon persists;
// writeEndpoint crafts it inside daemon.Dir(commonDir).
func endpointRecord(network, address string, pid int) string {
	return `{"network":"` + network + `","address":"` + address + `","pid":` +
		strconv.Itoa(pid) + `,"started_at":"2026-01-02T03:04:05Z","protocol_revision":1}`
}

func writeEndpoint(t *testing.T, commonDir, record string) {
	t.Helper()
	dir := daemon.Dir(commonDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "endpoint.json"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProbeClassifiesDaemonRecords(t *testing.T) {
	livePID := os.Getpid()
	dead := deadPID(t)
	// Matches the fixed started_at literal inside endpointRecord.
	startedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name     string
		record   string
		wantLive bool
	}{
		{"no daemon dir means stopped", "", false},
		{"record naming a dead pid means stopped",
			endpointRecord("unix", "/tmp/vas-dead.sock", dead), false},
		{"record naming this live process means live",
			endpointRecord("tcp", "127.0.0.1:4747", livePID), true},
		{"corrupt json record means stopped", "{not json", false},
		{"incomplete record without pid means stopped",
			endpointRecord("unix", "/tmp/vas-empty.sock", 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commonDir := t.TempDir()
			if tt.record != "" {
				writeEndpoint(t, commonDir, tt.record)
			}
			got := Probe(commonDir)
			if got.Live != tt.wantLive {
				t.Fatalf("Probe live = %v, want %v (%+v)", got.Live, tt.wantLive, got)
			}
			if !tt.wantLive && got != (Presence{}) {
				t.Fatalf("Stopped probe must be the zero value, got %+v", got)
			}
			if tt.wantLive && (got.PID != livePID || got.Network != "tcp" ||
				got.Address != "127.0.0.1:4747" || !got.StartedAt.Equal(startedAt)) {
				t.Fatalf("live fields mismatch: %+v", got)
			}
		})
	}
}

// admitRun creates one durable run through the exported store API and returns
// its id together with the job identity needed to drive further events.
func admitRun(t *testing.T, st *store.Store, candidate string) (string, agentrun.LogicalJob) {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate(candidate), agentrun.Prompt("presence fixture"), nil))
	runID := string(job.RunID())
	if err := st.CreateRun(job, store.RunPolicy{ID: "policy:test"}); err != nil {
		t.Fatalf("create run %s: %v", candidate, err)
	}
	return runID, job
}

func seedRun(t *testing.T, st *store.Store, candidate string) string {
	runID, _ := admitRun(t, st, candidate)
	return runID
}

func seedRunWithPolicyAt(t *testing.T, st *store.Store, candidate string,
	policy store.RunPolicy, at time.Time) string {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate(candidate), agentrun.Prompt("presence fixture"), nil))
	runID := string(job.RunID())
	if err := st.CreateRun(job, policy); err != nil {
		t.Fatalf("create run %s: %v", candidate, err)
	}
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	event, err := agentrun.NewNormalizedEvent(invocation, agentrun.StateCreated,
		agentrun.StateQueued, agentrun.DecisionStart, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendEvent(runID, event, 0); err != nil {
		t.Fatalf("append event: %v", err)
	}
	return runID
}

// admitRunWithOperation admits one durable run carrying an operator-facing
// operation label; an empty label produces the legacy-shaped policy bytes.
func admitRunWithOperation(t *testing.T, st *store.Store, candidate, operation string) string {
	t.Helper()
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate(candidate), agentrun.Prompt("presence fixture"), nil))
	runID := string(job.RunID())
	if err := st.CreateRun(job, store.RunPolicy{ID: "policy:test", Operation: operation}); err != nil {
		t.Fatalf("create run %s: %v", candidate, err)
	}
	return runID
}

// TestRecentRunsSurfacesOperationsAndDegradesSoftly pins slice 14's contract:
// labeled runs surface their admitted operation through RecentRuns, legacy
// records read back empty, and an unreadable policy degrades SOFTLY — the
// whole listing must survive with that one summary's label emptied out,
// because a cosmetic metadata miss can never fail the activity pane.
func TestRecentRunsSurfacesOperationsAndDegradesSoftly(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	reviewID := admitRunWithOperation(t, st, "candidate:labeled-review", "review")
	gateID := admitRunWithOperation(t, st, "candidate:labeled-gate", "gate pre-push")
	bareID := admitRunWithOperation(t, st, "candidate:legacy-bare", "")

	byID := func(summaries []RunSummary) map[string]RunSummary {
		indexed := make(map[string]RunSummary, len(summaries))
		for _, s := range summaries {
			indexed[s.RunID] = s
		}
		return indexed
	}

	summaries, err := RecentRuns(commonDir, 10)
	if err != nil || len(summaries) != 3 {
		t.Fatalf("want all three seeded runs, got %#v, %v", summaries, err)
	}
	labels := byID(summaries)
	if labels[reviewID].Operation != "review" {
		t.Fatalf("review run operation = %q, want %q", labels[reviewID].Operation, "review")
	}
	if labels[gateID].Operation != "gate pre-push" {
		t.Fatalf("gate run operation = %q, want %q", labels[gateID].Operation, "gate pre-push")
	}
	if labels[bareID].Operation != "" {
		t.Fatalf("legacy run operation = %q, want empty", labels[bareID].Operation)
	}

	corruptErr := os.WriteFile(filepath.Join(commonDir, "vas-sentinel", "executions", "v1", gateID, "policy.json"),
		[]byte(`{"worktree":"/hidden"}`), 0600)
	if corruptErr != nil {
		t.Fatal(corruptErr)
	}
	summaries, err = RecentRunsForWorktrees(commonDir, 10, []string{"/hidden"})
	if err != nil || len(summaries) != 3 {
		t.Fatalf("a corrupt policy must degrade softly, got %#v, %v", summaries, err)
	}
	labels = byID(summaries)
	if labels[gateID].Operation != "" || labels[gateID].Worktree != "" {
		t.Fatalf("malformed policy metadata = (%q,%q), want empty", labels[gateID].Operation, labels[gateID].Worktree)
	}
	if labels[gateID].ControlDisabledReason != policyReadControlDisabledReason {
		t.Fatalf("malformed policy control reason = %q, want %q", labels[gateID].ControlDisabledReason, policyReadControlDisabledReason)
	}
	if labels[reviewID].Operation != "review" || labels[bareID].Operation != "" {
		t.Fatalf("the other summaries must survive the degraded listing untouched: %+v", labels)
	}
}

// seedFailedRun drives a fresh run to terminal failed through exported store
// APIs only (CreateRun, AppendEvent, AppendTerminalEvent): no adapter involved.
func seedFailedRun(t *testing.T, st *store.Store, candidate string) string {
	return seedFailedRunAt(t, st, candidate, time.Unix(1700000000, 0).UTC())
}

func seedFailedRunAt(t *testing.T, st *store.Store, candidate string, base time.Time) string {
	t.Helper()
	runID, job := admitRun(t, st, candidate)
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	for revision, step := range [...]struct{ from, to agentrun.LifecycleState }{
		{agentrun.StateCreated, agentrun.StateQueued},
		{agentrun.StateQueued, agentrun.StateAdmitted},
		{agentrun.StateAdmitted, agentrun.StateRunning},
	} {
		event, err := agentrun.NewNormalizedEvent(invocation, step.from, step.to,
			agentrun.DecisionStart, base.Add(time.Duration(revision)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendEvent(runID, event, uint64(revision)); err != nil {
			t.Fatalf("append event %d: %v", revision, err)
		}
	}
	at := base.Add(10 * time.Second)
	event, err := agentrun.NewNormalizedEvent(invocation, agentrun.StateRunning,
		agentrun.StateFailed, agentrun.DecisionNone, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendTerminalEvent(runID, event, 3, store.AttemptOutcome{
		RunID:        runID,
		JobID:        string(job.ID()),
		InvocationID: string(invocation.InvocationID()),
		LineageID:    string(invocation.LineageIdentity()),
		Class:        agentrun.OutcomeFailure,
		Error:        "presence fixture failure",
		At:           at,
	}); err != nil {
		t.Fatalf("append terminal event: %v", err)
	}
	return runID
}

func TestRecentRunsPrioritizesStateThenProjectionTime(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	olderStartedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	newerStartedAt := time.Date(2026, 1, 2, 3, 5, 5, 0, time.UTC)
	olderID := seedFailedRunAt(t, st, "candidate:presence-fixture-003",
		olderStartedAt)
	newerID := seedFailedRunAt(t, st, "candidate:presence-fixture-000",
		newerStartedAt)
	zeroID := seedRun(t, st, "candidate:presence-zero")
	if newerID >= olderID {
		t.Fatalf("fixture newer run id %s must sort before older run id %s", newerID, olderID)
	}

	summaries, err := RecentRuns(commonDir, 10)
	if err != nil || len(summaries) != 3 {
		t.Fatalf("want all three seeded runs, got %#v, %v", summaries, err)
	}
	byID := make(map[string]RunSummary, len(summaries))
	for _, summary := range summaries {
		byID[summary.RunID] = summary
	}
	if s := byID[zeroID]; s.State != agentrun.StateCreated || s.Revision != 0 {
		t.Fatalf("zero-time run = %+v, want state created revision 0", s)
	}
	// Three non-terminal events plus the terminal event land on revision 4.
	for _, id := range []string{olderID, newerID} {
		s := byID[id]
		if s.State != agentrun.StateFailed || s.Revision != 4 {
			t.Fatalf("failed run = %+v, want state failed revision 4", s)
		}
		if s.Reason != "presence fixture failure" {
			t.Fatalf("failed run reason = %q, want terminal event error", s.Reason)
		}
	}

	// RecentRuns copies projection timestamps verbatim and orders each lifecycle
	// group timestamped newest-first. A zero timestamp is deterministic but never
	// outranks a timestamped run, and it is not replaced with a time derived from
	// the id.
	for _, tc := range []struct {
		id      string
		at      time.Time
		started time.Time
		zero    bool
	}{
		{id: olderID, at: olderStartedAt.Add(10 * time.Second), started: olderStartedAt},
		{id: newerID, at: newerStartedAt.Add(10 * time.Second), started: newerStartedAt},
		{id: zeroID, zero: true},
	} {
		if got := byID[tc.id].UpdatedAt; !got.Equal(tc.at) || got.IsZero() != tc.zero {
			t.Fatalf("run %s UpdatedAt = %v, want %v (zero=%v)", tc.id, got, tc.at, tc.zero)
		}
		if got := byID[tc.id].StartedAt; !got.Equal(tc.started) || got.IsZero() != tc.zero {
			t.Fatalf("run %s StartedAt = %v, want %v (zero=%v)", tc.id, got, tc.started, tc.zero)
		}
	}

	if summaries[0].RunID != zeroID || summaries[1].RunID != newerID || summaries[2].RunID != olderID {
		t.Fatalf("active runs must precede terminal runs, with zero-time last in their group, got %#v", summaries)
	}
	summaries, err = RecentRuns(commonDir, 1)
	if err != nil || len(summaries) != 1 || summaries[0].RunID != zeroID {
		t.Fatalf("limit 1 must yield the highest-priority active run %s, got %#v, %v", zeroID, summaries, err)
	}
}

func TestRecentRunsBreaksProjectionTimeTiesByRunID(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	leftID := seedFailedRunAt(t, st, "candidate:presence-tie-left", base)
	rightID := seedFailedRunAt(t, st, "candidate:presence-tie-right", base)
	wantFirst, wantSecond := leftID, rightID
	if wantSecond < wantFirst {
		wantFirst, wantSecond = wantSecond, wantFirst
	}

	summaries, err := RecentRuns(commonDir, 2)
	if err != nil || len(summaries) != 2 || summaries[0].RunID != wantFirst || summaries[1].RunID != wantSecond {
		t.Fatalf("equal projection times must sort by RunID, got %#v, %v", summaries, err)
	}
}

func TestRecentRunsSuppressesChildrenBeforeGlobalQuota(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	parentID := seedRunWithPolicyAt(t, st, "activity-parent", store.RunPolicy{
		ID: "policy:activity",
	}, base)
	for i := 0; i < 11; i++ {
		seedRunWithPolicyAt(t, st, "activity-child-"+strconv.Itoa(i), store.RunPolicy{
			ID: "policy:activity", ParentRunID: parentID,
		}, base.Add(time.Duration(i+1)*time.Minute))
	}

	summaries, err := RecentRuns(commonDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].RunID != parentID {
		t.Fatalf("child runs must be suppressed before quota selection: %#v", summaries)
	}
}

func TestRecentRunsKeepsChildWithMissingParentVisible(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	orphanID := seedRunWithPolicyAt(t, st, "activity-orphan", store.RunPolicy{
		ID: gateRootPolicyID, ParentRunID: "missing-parent",
	}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	summaries, err := RecentRuns(commonDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].RunID != orphanID {
		t.Fatalf("child with a missing parent must remain visible: %#v", summaries)
	}
	if summaries[0].ControlDisabledReason != "" {
		t.Fatalf("orphan gate child control reason = %q, want empty", summaries[0].ControlDisabledReason)
	}
}

func TestRecentRunsPlacesActiveRunsBeforeTerminalRuns(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	activeID := seedRunWithPolicyAt(t, st, "activity-active", store.RunPolicy{
		ID: "policy:activity",
	}, base)
	zeroID := seedRun(t, st, "activity-zero")
	olderTerminalID := seedFailedRunAt(t, st, "activity-terminal-older", base.Add(time.Hour))
	newerTerminalID := seedFailedRunAt(t, st, "activity-terminal-newer", base.Add(2*time.Hour))

	summaries, err := RecentRuns(commonDir, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{activeID, zeroID, newerTerminalID, olderTerminalID}
	if len(summaries) != len(want) {
		t.Fatalf("got %d summaries, want %d: %#v", len(summaries), len(want), summaries)
	}
	for i, id := range want {
		if summaries[i].RunID != id {
			t.Errorf("summary[%d] = %s, want %s; summaries=%#v", i, summaries[i].RunID, id, summaries)
		}
	}
}

func TestRecentRunsCollapsesGateChildrenAndKeepsWorktreeQuota(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mainPath := filepath.Join(t.TempDir(), "main")
	featurePath := filepath.Join(t.TempDir(), "feature")
	rootID := seedRunWithPolicyAt(t, st, "gate-root", store.RunPolicy{
		ID: gateRootPolicyID, Operation: "gate pre-push", Worktree: mainPath,
	}, base.Add(20*time.Minute))
	childID := seedRunWithPolicyAt(t, st, "gate-child", store.RunPolicy{
		ID: gateRootPolicyID, ParentRunID: rootID, Worktree: featurePath,
	}, base.Add(30*time.Minute))
	featureID := seedRunWithPolicyAt(t, st, "feature-new", store.RunPolicy{
		ID: "policy:review", Operation: "review", Worktree: featurePath,
	}, base.Add(10*time.Minute))
	seedRunWithPolicyAt(t, st, "feature-old", store.RunPolicy{
		ID: "policy:review", Operation: "review", Worktree: featurePath,
	}, base.Add(9*time.Minute))

	summaries, err := RecentRunsForWorktrees(commonDir, 1, []string{featurePath})
	if err != nil || len(summaries) != 2 {
		t.Fatalf("gate root plus selected worktree run = %#v, %v", summaries, err)
	}
	if summaries[0].RunID != rootID || summaries[0].ControlDisabledReason != gateRootControlDisabledReason {
		t.Fatalf("root summary = %+v, want aggregate control disabled", summaries[0])
	}
	if summaries[1].RunID != featureID || summaries[1].ControlDisabledReason != "" {
		t.Fatalf("selected worktree summary = %+v, want ordinary actionable run", summaries[1])
	}
	for _, summary := range summaries {
		if summary.RunID == childID {
			t.Fatalf("suppressed gate child leaked into summaries: %#v", summaries)
		}
	}
}

func TestRecentRunsStopsPolicyScanAfterQuotasAreFilled(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	worktree := filepath.Join(t.TempDir(), "worktree")
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 3; i++ {
		seedRunWithPolicyAt(t, st, "scan-"+strconv.Itoa(i), store.RunPolicy{
			ID: "policy:test", Worktree: worktree,
		}, base.Add(time.Duration(3-i)*time.Minute))
	}
	var inspected []string
	previous := readRunPolicy
	readRunPolicy = func(st *store.Store, runID string) (store.RunPolicy, error) {
		inspected = append(inspected, runID)
		return st.ReadRunPolicy(runID)
	}
	t.Cleanup(func() { readRunPolicy = previous })

	if _, err := RecentRunsForWorktrees(commonDir, 1, []string{worktree}); err != nil {
		t.Fatal(err)
	}
	if len(inspected) != 1 {
		t.Fatalf("satisfied quotas opened %d policy records, want 1: %v", len(inspected), inspected)
	}
}

func TestRecentRunsSurfacesHistoricalProjectionCorruption(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NewStore(commonDir)
	historicalID := seedRun(t, st, "candidate:presence-corrupt-history")
	seedFailedRun(t, st, "candidate:presence-corrupt-history-newer")
	statePath := filepath.Join(commonDir, "vas-sentinel", "executions", "v1", historicalID, "state.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := RecentRuns(commonDir, 1)
	if !errors.Is(err, store.ErrProjectionCorrupt) {
		t.Fatalf("historical projection corruption error = %v, want %v", err, store.ErrProjectionCorrupt)
	}
}

func TestRecentRunsEmptyStoreAndDegenerateLimits(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		summaries, err := RecentRuns(t.TempDir(), 5)
		if err != nil || summaries == nil || len(summaries) != 0 {
			t.Fatalf("want empty non-nil slice and nil error, got %#v, %v", summaries, err)
		}
	})
	for _, limit := range []int{0, -3} {
		t.Run("limit "+strconv.Itoa(limit), func(t *testing.T) {
			summaries, err := RecentRuns(t.TempDir(), limit)
			if err != nil || summaries == nil || len(summaries) != 0 {
				t.Fatalf("want empty non-nil slice and nil error, got %#v, %v", summaries, err)
			}
		})
	}
}

func TestRecentRunsAcceptsHugeLimitWithEmptyStore(t *testing.T) {
	t.Run("empty history", func(t *testing.T) {
		limit := int(^uint(0) >> 1)
		summaries, err := RecentRunsForWorktrees(t.TempDir(), limit, nil)
		if err != nil || summaries == nil || len(summaries) != 0 {
			t.Fatalf("huge limit on an empty store = %#v, %v; want an empty non-nil slice", summaries, err)
		}
	})

	t.Run("small limit does not preallocate full history", func(t *testing.T) {
		commonDir := t.TempDir()
		st := store.NewStore(commonDir)
		base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		const historySize = 64
		for i := 0; i < historySize; i++ {
			seedRunWithPolicyAt(t, st, "capacity-history-"+strconv.Itoa(i), store.RunPolicy{
				ID: "policy:test",
			}, base.Add(time.Duration(i)*time.Second))
		}

		summaries, err := RecentRunsForWorktrees(commonDir, 1, nil)
		if err != nil || len(summaries) != 1 {
			t.Fatalf("small-limit history result = %#v, %v; want one summary", summaries, err)
		}
		if got := cap(summaries); got != 1 {
			t.Fatalf("small-limit result capacity = %d, want 1 despite %d historical summaries", got, historySize)
		}
	})
}
