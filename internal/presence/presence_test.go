package presence

import (
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

// seedFailedRun drives a fresh run to terminal failed through exported store
// APIs only (CreateRun, AppendEvent, AppendTerminalEvent): no adapter involved.
func seedFailedRun(t *testing.T, st *store.Store, candidate string) string {
	t.Helper()
	runID, job := admitRun(t, st, candidate)
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1700000000, 0).UTC()
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

func TestRecentRunsReportsStateRevisionAndLimitTail(t *testing.T) {
	commonDir := t.TempDir()
	st := store.NuevoStore(commonDir)
	createdID := seedRun(t, st, "candidate:presence-created")
	failedID := seedFailedRun(t, st, "candidate:presence-failed")

	summaries, err := RecentRuns(commonDir, 10)
	if err != nil || len(summaries) != 2 {
		t.Fatalf("want both seeded runs, got %#v, %v", summaries, err)
	}
	byID := make(map[string]RunSummary, len(summaries))
	for _, summary := range summaries {
		byID[summary.RunID] = summary
	}
	if s := byID[createdID]; s.State != agentrun.StateCreated || s.Revision != 0 {
		t.Fatalf("created run = %+v, want state created revision 0", s)
	}
	// Three non-terminal events plus the terminal event land on revision 4.
	if s := byID[failedID]; s.State != agentrun.StateFailed || s.Revision != 4 {
		t.Fatalf("failed run = %+v, want state failed revision 4", s)
	}

	// UpdatedAt is copied verbatim from the stored projection: the seeded
	// terminal event carries a real timestamp, while a run that never left
	// the created state has no event frames and therefore none at all.
	storedCreated, err := st.ReadProjection(createdID)
	if err != nil {
		t.Fatalf("read created projection: %v", err)
	}
	storedFailed, err := st.ReadProjection(failedID)
	if err != nil {
		t.Fatalf("read failed projection: %v", err)
	}
	if !byID[createdID].UpdatedAt.Equal(storedCreated.UpdatedAt) || !storedCreated.UpdatedAt.IsZero() {
		t.Fatalf("created UpdatedAt = %v, want the stored zero value", byID[createdID].UpdatedAt)
	}
	if !byID[failedID].UpdatedAt.Equal(storedFailed.UpdatedAt) || storedFailed.UpdatedAt.IsZero() {
		t.Fatalf("failed UpdatedAt = %v, want the stored terminal-event time %v",
			byID[failedID].UpdatedAt, storedFailed.UpdatedAt)
	}

	// Run ids are content hashes without time information, so the documented
	// deterministic choice for a tight limit is the lexicographic tail of the
	// sorted identifier list.
	expectedTail := createdID
	if failedID > expectedTail {
		expectedTail = failedID
	}
	summaries, err = RecentRuns(commonDir, 1)
	if err != nil || len(summaries) != 1 || summaries[0].RunID != expectedTail {
		t.Fatalf("limit 1 must yield only tail id %s, got %#v, %v", expectedTail, summaries, err)
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
