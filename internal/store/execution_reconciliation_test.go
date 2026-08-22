package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// The tests below drive restart reconciliation (ticket 08 slice 3) over
// synthetic but fully valid streams built with the real append machinery:
// every frame is hash-chained and passes the strict validator, so the
// classification rules are proven against exactly the bytes production
// writes.

type streamTransition struct {
	from     agentrun.LifecycleState
	to       agentrun.LifecycleState
	decision agentrun.Decision
}

var startSequence = []streamTransition{
	{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
	{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
	{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
}

func appendStream(t *testing.T, s *Store, job agentrun.LogicalJob, transitions []streamTransition) {
	t.Helper()
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	for revision, transition := range transitions {
		event, err := agentrun.NewNormalizedEvent(invocation, transition.from, transition.to, transition.decision, time.Unix(1700000000, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendEvent(string(job.RunID()), event, uint64(revision)); err != nil {
			t.Fatalf("AppendEvent(%s->%s) error = %v", transition.from, transition.to, err)
		}
	}
}

// truncateLastNewline simulates the crash window between flushing a complete
// final frame and its trailing newline byte.
func truncateLastNewline(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("test setup error: stream does not end with a newline")
	}
	if err := os.WriteFile(path, data[:len(data)-1], 0600); err != nil {
		t.Fatal(err)
	}
}

// appendPartialJSONTail simulates a crash in the middle of writing the next
// record: unparseable final bytes with no complete frame.
func appendPartialJSONTail(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte(`{"sequence":`)); err != nil {
		t.Fatal(err)
	}
}

func readExecutionFileBytes(t *testing.T, s *Store, runID, name string) []byte {
	t.Helper()
	directory, err := s.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReadReconciledProjectionClassifiesOwnerDeathDuringCancellation(t *testing.T) {
	tests := []struct {
		name          string
		transitions   []streamTransition
		truncateTail  bool
		wantState     agentrun.LifecycleState
		wantTerminal  agentrun.TerminalClass
		wantOrphaned  bool
		wantTailError bool
	}{
		{
			name:         "full success control projects untouched",
			transitions:  append(append([]streamTransition{}, startSequence...), streamTransition{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete}),
			wantState:    agentrun.StateSucceeded,
			wantTerminal: agentrun.TerminalSuccess,
		},
		{
			name: "settled canceled control keeps the authoritative settlement",
			transitions: append(append([]streamTransition{}, startSequence...),
				streamTransition{agentrun.StateRunning, agentrun.StateCanceled, agentrun.DecisionAbort}),
			wantState:    agentrun.StateCanceled,
			wantTerminal: agentrun.TerminalCancellation,
		},
		{
			// Plain running heads carry no cancellation evidence at all:
			// their recovery classification belongs to R8 and must never be
			// misclassified as canceled here.
			name:         "plain running head stays non-terminal pending recovery",
			transitions:  startSequence,
			wantState:    agentrun.StateRunning,
			wantTerminal: agentrun.TerminalNone,
		},
		{
			name: "terminating then nothing classifies canceled-orphaned",
			transitions: append(append([]streamTransition{}, startSequence...),
				streamTransition{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone}),
			wantState:    agentrun.StateCanceled,
			wantTerminal: agentrun.TerminalCancellation,
			wantOrphaned: true,
		},
		{
			name: "terminated then nothing classifies canceled-orphaned",
			transitions: append(append([]streamTransition{}, startSequence...),
				streamTransition{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone},
				streamTransition{agentrun.StateTerminating, agentrun.StateTerminated, agentrun.DecisionNone}),
			wantState:    agentrun.StateCanceled,
			wantTerminal: agentrun.TerminalCancellation,
			wantOrphaned: true,
		},
		{
			// The writer crashed after flushing a valid hash-chained escalation
			// frame but before its newline: still owner death during
			// cancellation, classified honestly without repairing any byte.
			name:         "valid truncated terminating tail classifies canceled-orphaned",
			transitions:  append(append([]streamTransition{}, startSequence...), streamTransition{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone}),
			truncateTail: true,
			wantState:    agentrun.StateCanceled,
			wantTerminal: agentrun.TerminalCancellation,
			wantOrphaned: true,
		},
		{
			// A truncated plain head proves nothing about cancellation; the
			// incomplete-tail failure (and `runs recover`) stays exact.
			name:          "truncated running tail keeps failing closed as incomplete",
			transitions:   startSequence,
			truncateTail:  true,
			wantTailError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NuevoStore(t.TempDir())
			job := testJob()
			if err := s.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
				t.Fatal(err)
			}
			appendStream(t, s, job, tt.transitions)
			directory, err := s.executionDir(string(job.RunID()))
			if err != nil {
				t.Fatal(err)
			}
			if tt.truncateTail {
				truncateLastNewline(t, directory)
			}
			runID := string(job.RunID())
			eventsBefore := readExecutionFileBytes(t, s, runID, "events.jsonl")
			stateBefore := readExecutionFileBytes(t, s, runID, "state.json")

			read := func() *RunProjection {
				t.Helper()
				projection, readErr := s.ReadReconciledProjection(runID)
				if tt.wantTailError {
					var tailErr IncompleteEventTailError
					if !errors.As(readErr, &tailErr) {
						t.Fatalf("ReadReconciledProjection() error = %v, want IncompleteEventTailError", readErr)
					}
					return nil
				}
				if readErr != nil {
					t.Fatalf("ReadReconciledProjection() error = %v", readErr)
				}
				return projection
			}

			first := read()
			second := read()
			// Derived status must be identical on every observation of the
			// same bytes: reconciliation is a pure derivation, never an
			// accumulating write.
			if (first == nil) != (second == nil) {
				t.Fatalf("repeated reads disagree on failure behavior: %v vs %v", first, second)
			}
			if first != nil && *first != *second {
				t.Fatalf("repeated reads differ: %+v vs %+v", *first, *second)
			}
			if first == nil {
				return
			}
			lastRevision := uint64(len(tt.transitions))
			if first.Revision != lastRevision || first.Sequence != lastRevision {
				t.Fatalf("projection sequence/revision = %d/%d, want %d", first.Sequence, first.Revision, lastRevision)
			}
			if first.State != tt.wantState || first.Terminal != tt.wantTerminal {
				t.Fatalf("projection state/terminal = %s/%s, want %s/%s", first.State, first.Terminal, tt.wantState, tt.wantTerminal)
			}
			if first.OrphanedCancellation != tt.wantOrphaned {
				t.Fatalf("OrphanedCancellation = %t, want %t", first.OrphanedCancellation, tt.wantOrphaned)
			}

			// Byte stability: reconciliation never rewrites the stream or its
			// persisted projection.
			if !bytes.Equal(readExecutionFileBytes(t, s, runID, "events.jsonl"), eventsBefore) {
				t.Fatal("reconciled reads mutated events.jsonl; the stream must stay append-only and untouched")
			}
			if !bytes.Equal(readExecutionFileBytes(t, s, runID, "state.json"), stateBefore) {
				t.Fatal("reconciled reads mutated state.json; derived views are read-time only")
			}
		})
	}
}

// TestReadReconciledProjectionPartialJSONTailStaysIncomplete proves that an
// unparseable final record over an escalating stream still fails closed:
// without the complete frame there is no trustworthy evidence to classify.
func TestReadReconciledProjectionPartialJSONTailStaysIncomplete(t *testing.T) {
	s := NuevoStore(t.TempDir())
	job := testJob()
	if err := s.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	appendStream(t, s, job, append(append([]streamTransition{}, startSequence...),
		streamTransition{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone}))
	directory, err := s.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	appendPartialJSONTail(t, directory)

	if _, err := s.ReadReconciledProjection(string(job.RunID())); !errors.As(err, new(IncompleteEventTailError)) {
		t.Fatalf("ReadReconciledProjection() error = %v, want IncompleteEventTailError for an unparseable tail", err)
	}
}

// TestReconcileProjectionLeavesPersistedSerializationUnchanged pins the
// additive-compatibility contract: projectionFor output serializes to exactly
// the same bytes it always did, so writers keep producing legacy-compatible
// state.json files while only readers derive the reconciled view.
func TestReconcileProjectionLeavesPersistedSerializationUnchanged(t *testing.T) {
	frames := make([]EventFrame, 0)
	raw := projectionFor("run-id", frames)
	reconciled := reconcileProjection("run-id", frames)
	rawBytes, err1 := marshalRecord(raw)
	reconciledBytes, err2 := marshalRecord(reconciled)
	if err1 != nil || err2 != nil {
		t.Fatalf("marshal errors: %v / %v", err1, err2)
	}
	if !bytes.Equal(rawBytes, reconciledBytes) {
		t.Fatalf("serialization drifted:\n%s\n%s", rawBytes, reconciledBytes)
	}
	if raw.OrphanedCancellation || reconciled.OrphanedCancellation {
		t.Fatal("writer-derived projections must never carry the reconciliation verdict")
	}
}
