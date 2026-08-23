package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// Ticket 09 slice 1: recovery classification over synthetic but fully valid
// streams built with the real AppendEvent machinery, plus deliberately
// tampered bytes for the corrupt classes. Every scan asserted here must be a
// pure read: the store's bytes are hashed before and after to prove it.

func scanTestJob(t *testing.T, candidate string) agentrun.LogicalJob {
	t.Helper()
	request := agentrun.NewRunRequest(agentrun.Candidate(candidate), agentrun.Prompt("recovery fixture"), nil)
	return agentrun.NewLogicalJob(request)
}

// buildScanRun admits one run and appends transitions through the real
// append machinery, returning its directory for byte-level tampering.
func buildScanRun(t *testing.T, s *Store, candidate string, transitions []streamTransition) (agentrun.LogicalJob, string) {
	t.Helper()
	job := scanTestJob(t, candidate)
	if err := s.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	appendStream(t, s, job, transitions)
	directory, err := s.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	return job, directory
}

func awaitDecisionTransition() streamTransition {
	return streamTransition{agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.DecisionNone}
}

func successTransition() streamTransition {
	return streamTransition{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete}
}

// overwriteSnapshot replaces state.json with a projection that lags the
// four-frame stream head by lag revisions, simulating a writer that committed
// events but died before replacing the snapshot.
func overwriteSnapshot(t *testing.T, directory, runID string, lag int) {
	t.Helper()
	data, err := marshalRecord(RunProjection{
		RunID: runID, Sequence: uint64(4 - lag), Revision: uint64(4 - lag),
		State: agentrun.StateRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// tamperFirstHash flips one hex character of the first content hash so the
// frame no longer matches its own content digest.
func tamperFirstHash(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	marker := "\"content_hash\":\""
	start := strings.Index(string(data), marker)
	if start < 0 {
		t.Fatal("test setup error: no content hash found")
	}
	digit := start + len(marker)
	replacement := byte('0')
	if data[digit] == '0' {
		replacement = '1'
	}
	data[digit] = replacement
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// appendInvalidTransitionFrame hand-crafts a fully self-consistent frame
// (its content hash is correct) whose lifecycle transition is impossible:
// a queued run can never succeed directly. This isolates the transition
// invariant from the hash invariant.
func appendInvalidTransitionFrame(t *testing.T, job agentrun.LogicalJob, directory string) {
	t.Helper()
	frame := EventFrame{
		Sequence: 2, Revision: 2, At: time.Unix(1700000009, 0).UTC(),
		RunID:        string(job.RunID()),
		JobID:        string(job.ID()),
		InvocationID: "invocation-broken", LineageID: "lineage-broken",
		From: agentrun.StateQueued, To: agentrun.StateSucceeded,
		Terminal: agentrun.TerminalSuccess,
	}
	frame.ContentHash = hashEventContent(frame.content())
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

// hashExecutionTree fingerprints every file under the executions directory so
// any write performed by a scan changes at least one digest.
func hashExecutionTree(t *testing.T, root string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digests[filepath.ToSlash(rel)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return digests
}

func TestScanRecoveriesClassifiesEveryNonTerminalRun(t *testing.T) {
	awaiting := append(append([]streamTransition{}, startSequence...), awaitDecisionTransition())
	running := append([]streamTransition{}, startSequence...)
	succeeded := append(append([]streamTransition{}, startSequence...), successTransition())
	orphaned := append(append([]streamTransition{}, startSequence...),
		streamTransition{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone})

	tests := []struct {
		name          string
		transitions   []streamTransition
		mutate        func(t *testing.T, s *Store, job agentrun.LogicalJob, directory string)
		wantClass     RecoveryClass
		wantHead      uint64
		wantReconcile bool
		wantReason    string
	}{
		{
			name:        "awaiting-decision head classifies recoverable",
			transitions: awaiting,
			wantClass:   RecoveryRecoverable,
			wantHead:    4,
			wantReason:  "awaiting-decision head with intact decision evidence",
		},
		{
			name:        "plain running head requires an operator decision",
			transitions: running,
			wantClass:   RecoveryOperatorRequired,
			wantHead:    3,
			wantReason:  "non-terminal running head cannot decide recovery",
		},
		{
			name:       "admitted run without events requires an operator decision",
			wantClass:  RecoveryOperatorRequired,
			wantHead:   0,
			wantReason: "stream records no events",
		},
		{
			name:          "escalation without settlement carries the orphaned-canceled view",
			transitions:   orphaned,
			wantClass:     RecoveryOrphanedCanceled,
			wantHead:      4,
			wantReconcile: true,
			wantReason:    "owner death during cancellation",
		},
		{
			name:        "terminal frame with lagging snapshot is unprojected",
			transitions: succeeded,
			mutate: func(t *testing.T, _ *Store, job agentrun.LogicalJob, directory string) {
				overwriteSnapshot(t, directory, string(job.RunID()), 1)
			},
			wantClass:  RecoveryTerminalUnprojected,
			wantHead:   4,
			wantReason: "persisted snapshot lags it (snapshot revision 3, stream head revision 4)",
		},
		{
			name:        "missing snapshot after a terminal frame is unprojected",
			transitions: succeeded,
			mutate: func(t *testing.T, _ *Store, _ agentrun.LogicalJob, directory string) {
				if err := os.Remove(filepath.Join(directory, "state.json")); err != nil {
					t.Fatal(err)
				}
			},
			wantClass:  RecoveryTerminalUnprojected,
			wantHead:   4,
			wantReason: "no persisted snapshot does",
		},
		{
			name:        "hash-chain break reports corruption with the exact defect",
			transitions: running,
			mutate: func(t *testing.T, _ *Store, _ agentrun.LogicalJob, directory string) {
				tamperFirstHash(t, directory)
			},
			wantClass:  RecoveryCorrupt,
			wantHead:   0,
			wantReason: "content hash mismatch",
		},
		{
			name:        "invalid transition reports corruption with the exact defect",
			transitions: []streamTransition{startSequence[0]},
			mutate: func(t *testing.T, _ *Store, job agentrun.LogicalJob, directory string) {
				appendInvalidTransitionFrame(t, job, directory)
			},
			wantClass:  RecoveryCorrupt,
			wantHead:   0,
			wantReason: "invalid lifecycle transition",
		},
		{
			name:        "truncated plain tail reports corruption with the exact defect",
			transitions: running,
			mutate: func(t *testing.T, _ *Store, _ agentrun.LogicalJob, directory string) {
				truncateLastNewline(t, directory)
			},
			wantClass: RecoveryCorrupt,
			// The unusable tail frame is excluded from the verified
			// evidence: the head counts complete frames only.
			wantHead:   2,
			wantReason: "incomplete final execution event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NuevoStore(t.TempDir())
			job, directory := buildScanRun(t, s, "candidate:"+tt.name, tt.transitions)
			runID := string(job.RunID())
			if tt.mutate != nil {
				tt.mutate(t, s, job, directory)
			}

			executionsRoot := filepath.Dir(directory)
			before := hashExecutionTree(t, executionsRoot)
			entries, err := ScanRecoveries(s)
			if err != nil {
				t.Fatalf("ScanRecoveries() error = %v", err)
			}
			after := hashExecutionTree(t, executionsRoot)

			if len(entries) != 1 {
				t.Fatalf("ScanRecoveries() entries = %d (%+v), want exactly the classified run", len(entries), entries)
			}
			entry := entries[0]
			if entry.RunID != runID {
				t.Fatalf("entry run = %s, want %s", entry.RunID, runID)
			}
			if entry.Class != tt.wantClass {
				t.Fatalf("class = %s, want %s (reason %s)", entry.Class, tt.wantClass, entry.Reason)
			}
			if entry.HeadSequence != tt.wantHead {
				t.Fatalf("head sequence = %d, want %d", entry.HeadSequence, tt.wantHead)
			}
			if entry.Reconciled != tt.wantReconcile {
				t.Fatalf("reconciled = %t, want %t", entry.Reconciled, tt.wantReconcile)
			}
			if !strings.Contains(entry.Reason, tt.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", entry.Reason, tt.wantReason)
			}

			// Byte stability: scanning is read-only on every path.
			beforeJSON, marshalErr := json.Marshal(before)
			afterJSON, marshalErr2 := json.Marshal(after)
			if marshalErr != nil || marshalErr2 != nil {
				t.Fatalf("digest marshaling failed: %v / %v", marshalErr, marshalErr2)
			}
			if string(beforeJSON) != string(afterJSON) {
				t.Fatalf("scan mutated the store:\nbefore %s\nafter  %s", beforeJSON, afterJSON)
			}
		})
	}
}

// TestScanRecoveriesCorruptTailCarriesExactErrorText pins that the corrupt
// reason is not a paraphrase: it is the underlying error's own text.
func TestScanRecoveriesCorruptTailCarriesExactErrorText(t *testing.T) {
	s := NuevoStore(t.TempDir())
	job, directory := buildScanRun(t, s, "candidate:tail", startSequence)
	truncateLastNewline(t, directory)

	entries, err := ScanRecoveries(s)
	if err != nil {
		t.Fatalf("ScanRecoveries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one corrupt entry", entries)
	}
	want := IncompleteEventTailError{RunID: string(job.RunID())}.Error()
	if entries[0].Class != RecoveryCorrupt || entries[0].Reason != want {
		t.Fatalf("entry = %s/%q, want corrupt with exact text %q", entries[0].Class, entries[0].Reason, want)
	}
}

// TestScanRecoveriesExcludesSettledRunsAndIncludesTheRest proves the scan
// surfaces only non-terminal work: healthy terminal projections never appear.
func TestScanRecoveriesExcludesSettledRunsAndIncludesTheRest(t *testing.T) {
	s := NuevoStore(t.TempDir())
	settledJob, _ := buildScanRun(t, s, "candidate:settled",
		append(append([]streamTransition{}, startSequence...), successTransition()))
	buildScanRun(t, s, "candidate:awaiting",
		append(append([]streamTransition{}, startSequence...), awaitDecisionTransition()))
	buildScanRun(t, s, "candidate:running", startSequence)

	entries, err := ScanRecoveries(s)
	if err != nil {
		t.Fatalf("ScanRecoveries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want exactly the two non-terminal runs", entries)
	}
	for _, entry := range entries {
		if entry.RunID == string(settledJob.RunID()) {
			t.Fatalf("settled run %s leaked into the recovery scan", entry.RunID)
		}
		switch entry.Class {
		case RecoveryRecoverable, RecoveryOperatorRequired:
		default:
			t.Fatalf("run %s classified %s, want recoverable or operator-required", entry.RunID, entry.Class)
		}
	}
}

// TestScanRecoveriesEmptyStoreReturnsNoEntries pins the empty-store contract:
// zero entries and no error, so the CLI can exit 0 without special cases.
func TestScanRecoveriesEmptyStoreReturnsNoEntries(t *testing.T) {
	s := NuevoStore(t.TempDir())
	entries, err := ScanRecoveries(s)
	if err != nil {
		t.Fatalf("ScanRecoveries() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none for an empty store", entries)
	}
}

// TestClassifyRecoveryIsTotalAndNeverPanics proves the classifier maps every
// evidence shape, including unknown or broken ones, to exactly one class.
func TestClassifyRecoveryIsTotalAndNeverPanics(t *testing.T) {
	tests := []struct {
		name      string
		evidence  recoveryEvidence
		wantClass RecoveryClass
	}{
		{
			name:      "zero evidence defaults to operator-required",
			wantClass: RecoveryOperatorRequired,
		},
		{
			name:      "unknown scan failure still classifies as corrupt",
			evidence:  recoveryEvidence{scanErr: errors.New("mystery failure")},
			wantClass: RecoveryCorrupt,
		},
		{
			name:      "unknown tail failure still classifies as corrupt",
			evidence:  recoveryEvidence{tailErr: errors.New("mystery tail"), frames: []EventFrame{{Sequence: 7}}},
			wantClass: RecoveryCorrupt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := classifyRecovery("run-id", tt.evidence)
			if entry.Class != tt.wantClass {
				t.Fatalf("class = %s, want %s", entry.Class, tt.wantClass)
			}
			if entry.Reason == "" {
				t.Fatal("every verdict must name its evidence or the missing piece")
			}
		})
	}
}
