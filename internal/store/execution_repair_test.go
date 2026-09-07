package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// Ticket 10 slice 2: deterministic projection rebuild and repair of
// terminal-but-unprojected runs. Every test builds real streams through the
// append machinery, simulates or refuses the exact crash window, and proves
// byte-level guarantees: repair writes only state.json, only through the
// production atomic-write path, and replaying a healthy stream reproduces the
// persisted snapshot byte for byte.

// digestsWithoutLockFiles drops the advisory .events.lock files from a tree
// fingerprint: repair legitimately takes (and leaves) lock files, so zero-
// write proofs must compare everything else.
func digestsWithoutLockFiles(digests map[string]string) map[string]string {
	filtered := make(map[string]string, len(digests))
	for path, digest := range digests {
		if filepath.Base(path) == ".events.lock" {
			continue
		}
		filtered[path] = digest
	}
	return filtered
}

func removeSnapshot(t *testing.T, directory string) {
	t.Helper()
	if err := os.Remove(filepath.Join(directory, "state.json")); err != nil {
		t.Fatal(err)
	}
}

func readEventsBytes(t *testing.T, directory string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertRefusedWithReason(t *testing.T, err error, wantClass RecoveryClass, wantReason string) RecoveryNotRepairableError {
	t.Helper()
	var refusal RecoveryNotRepairableError
	if !errors.As(err, &refusal) {
		t.Fatalf("repair error = %v, want a RecoveryNotRepairableError", err)
	}
	if !errors.Is(err, ErrRecoveryNotRepairable) {
		t.Fatalf("refusal does not unwrap to ErrRecoveryNotRepairable: %v", err)
	}
	if refusal.Class != wantClass {
		t.Fatalf("refusal class = %s, want %s", refusal.Class, wantClass)
	}
	if !strings.Contains(refusal.Reason, wantReason) {
		t.Fatalf("refusal reason = %q, want it to contain %q", refusal.Reason, wantReason)
	}
	return refusal
}

// TestRepairRecoversCrashWindowBeforeSnapshot simulates the real crash:
// AppendEvent committed the terminal frame, then the process died before
// writeProjection replaced state.json. Repair must transition the class from
// terminal_unprojected to settled and leave state.json byte-equal to what a
// normal append would have written — proven against an identical healthy run
// in a second store built from the same candidate identity.
func TestRepairRecoversCrashWindowBeforeSnapshot(t *testing.T) {
	transitions := append(append([]streamTransition{}, startSequence...), successTransition())
	s := NewStore(t.TempDir())
	job, directory := buildScanRun(t, s, "candidate:crash-window", transitions)
	runID := string(job.RunID())
	eventsBefore := readEventsBytes(t, directory)
	removeSnapshot(t, directory)

	entries, err := ScanRecoveries(s)
	if err != nil || len(entries) != 1 || entries[0].Class != RecoveryTerminalUnprojected {
		t.Fatalf("pre-repair scan = %+v, error = %v; want one terminal-unprojected entry", entries, err)
	}

	result, err := RepairTerminalUnprojected(s, runID)
	if err != nil {
		t.Fatalf("RepairTerminalUnprojected() error = %v", err)
	}
	if result.ClassBefore != RecoveryTerminalUnprojected || result.ClassAfter != RecoverySettled {
		t.Fatalf("class transition = %s → %s, want terminal_unprojected → settled", result.ClassBefore, result.ClassAfter)
	}
	if !result.Rewritten {
		t.Fatal("repair claimed no rewrite for a missing snapshot")
	}

	healthy := NewStore(t.TempDir())
	healthyJob, healthyDir := buildScanRun(t, healthy, "candidate:crash-window", transitions)
	if healthyJob.RunID() != job.RunID() {
		t.Fatalf("identity derivation is not deterministic: %s vs %s", healthyJob.RunID(), job.RunID())
	}
	want, err := os.ReadFile(filepath.Join(healthyDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("repaired snapshot differs from a normal append's bytes:\ngot  %s\nwant %s", got, want)
	}
	if !bytes.Equal(readEventsBytes(t, directory), eventsBefore) {
		t.Fatal("repair modified the append-only event log")
	}

	afterEntries, err := ScanRecoveries(s)
	if err != nil || len(afterEntries) != 0 {
		t.Fatalf("post-repair scan = %+v, error = %v; want nothing left to recover", afterEntries, err)
	}
}

// TestRepairRecoversStaleSnapshotCrashWindow covers the second shape of the
// same crash window: every append succeeded, but the persisted state.json is
// an older valid copy taken before the terminal frame, so the stream head is
// terminal while the snapshot lags. The stale snapshot is built through real
// appends only — captured after the running head, then restored over the
// post-terminal snapshot. Repair must classify the run as
// terminal_unprojected, rewrite it, and produce bytes equal to a normal full
// append sequence in a twin store.
func TestRepairRecoversStaleSnapshotCrashWindow(t *testing.T) {
	startOnly := append([]streamTransition{}, startSequence...)
	full := append(append([]streamTransition{}, startSequence...), successTransition())
	s := NewStore(t.TempDir())
	job, directory := buildScanRun(t, s, "candidate:stale-snapshot", startOnly)
	runID := string(job.RunID())

	staleSnapshot, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	// appendStream pins revisions to its own loop index, so the crash-window
	// tail appends its single terminal frame at the live head explicitly.
	tail := successTransition()
	invocation, invocationErr := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if invocationErr != nil {
		t.Fatal(invocationErr)
	}
	tailEvent, tailErr := agentrun.NewNormalizedEvent(invocation, tail.from, tail.to,
		tail.decision, time.Unix(1700000000, 0).UTC())
	if tailErr != nil {
		t.Fatal(tailErr)
	}
	if _, err := s.AppendEvent(runID, tailEvent, uint64(len(startSequence))); err != nil {
		t.Fatalf("AppendEvent(%s->%s) error = %v", tail.from, tail.to, err)
	}
	eventsBefore := readEventsBytes(t, directory)
	if err := os.WriteFile(filepath.Join(directory, "state.json"), staleSnapshot, 0600); err != nil {
		t.Fatal(err)
	}

	entries, err := ScanRecoveries(s)
	if err != nil || len(entries) != 1 || entries[0].Class != RecoveryTerminalUnprojected {
		t.Fatalf("pre-repair scan = %+v, error = %v; want one terminal-unprojected entry", entries, err)
	}

	result, err := RepairTerminalUnprojected(s, runID)
	if err != nil {
		t.Fatalf("RepairTerminalUnprojected() error = %v", err)
	}
	if result.ClassBefore != RecoveryTerminalUnprojected || result.ClassAfter != RecoverySettled {
		t.Fatalf("class transition = %s → %s, want terminal_unprojected → settled", result.ClassBefore, result.ClassAfter)
	}
	if !result.Rewritten {
		t.Fatal("repair claimed no rewrite for a stale snapshot")
	}

	healthy := NewStore(t.TempDir())
	healthyJob, healthyDir := buildScanRun(t, healthy, "candidate:stale-snapshot", full)
	if healthyJob.RunID() != job.RunID() {
		t.Fatalf("identity derivation is not deterministic: %s vs %s", healthyJob.RunID(), job.RunID())
	}
	want, err := os.ReadFile(filepath.Join(healthyDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("repaired snapshot differs from a normal append's bytes:\ngot  %s\nwant %s", got, want)
	}
	if !bytes.Equal(readEventsBytes(t, directory), eventsBefore) {
		t.Fatal("repair modified the append-only event log")
	}

	afterEntries, err := ScanRecoveries(s)
	if err != nil || len(afterEntries) != 0 {
		t.Fatalf("post-repair scan = %+v, error = %v; want nothing left to recover", afterEntries, err)
	}
}

// TestReplayMatchesHealthySnapshotByteForByte is the determinism proof hook:
// for a fully projected healthy stream, ReplayProjection over the validated
// events must reproduce the on-disk state.json exactly, and running the
// production rebuild must be a byte-level no-op.
func TestReplayMatchesHealthySnapshotByteForByte(t *testing.T) {
	s := NewStore(t.TempDir())
	job, directory := buildScanRun(t, s, "candidate:determinism",
		append(append([]streamTransition{}, startSequence...), successTransition()))
	runID := string(job.RunID())

	disk, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadEvents(runID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ReplayProjection(runID, page.Events)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(disk, replayed) {
		t.Fatalf("replay diverges from disk:\ndisk    %s\nreplay  %s", disk, replayed)
	}

	before := digestsWithoutLockFiles(hashExecutionTree(t, filepath.Dir(directory)))
	if err := s.RebuildProjection(runID); err != nil {
		t.Fatal(err)
	}
	reread, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reread, disk) {
		t.Fatalf("production rebuild rewrote different bytes:\nbefore %s\nafter  %s", disk, reread)
	}
	after := digestsWithoutLockFiles(hashExecutionTree(t, filepath.Dir(directory)))
	beforeJSON := marshalRecordOrDie(t, before)
	afterJSON := marshalRecordOrDie(t, after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("rebuild mutated the store tree:\nbefore %s\nafter  %s", beforeJSON, afterJSON)
	}
}

func marshalRecordOrDie(t *testing.T, value any) []byte {
	t.Helper()
	data, err := marshalRecord(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestRepairRefusesCorruptStreamWithZeroWrites pins that a hash-chain break
// is reported with its exact defect and that repair performs no write at all.
func TestRepairRefusesCorruptStreamWithZeroWrites(t *testing.T) {
	s := NewStore(t.TempDir())
	_, directory := buildScanRun(t, s, "candidate:corrupt", startSequence)
	tamperFirstHash(t, directory)
	root := filepath.Dir(directory)
	before := digestsWithoutLockFiles(hashExecutionTree(t, root))

	_, err := RepairTerminalUnprojected(s, "nonexistent-run")
	if !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("unknown run error = %v, want ErrExecutionNotFound", err)
	}

	job := scanTestJob(t, "candidate:corrupt")
	_, err = RepairTerminalUnprojected(s, string(job.RunID()))
	assertRefusedWithReason(t, err, RecoveryCorrupt, "content hash mismatch")

	after := digestsWithoutLockFiles(hashExecutionTree(t, root))
	beforeJSON := marshalRecordOrDie(t, before)
	afterJSON := marshalRecordOrDie(t, after)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("corrupt refusal wrote to the store:\nbefore %s\nafter  %s", beforeJSON, afterJSON)
	}
}

// TestRepairRefusesNonUnprojectedClassesWithExactReasons covers the remaining
// classifier verdicts: orphaned-canceled, awaiting-decision recoverable, and
// operator-required heads all refuse with their reason and never write.
func TestRepairRefusesNonUnprojectedClassesWithExactReasons(t *testing.T) {
	tests := []struct {
		name        string
		transitions []streamTransition
		wantClass   RecoveryClass
		wantReason  string
	}{
		{
			name: "orphaned-canceled escalation stays final",
			transitions: append(append([]streamTransition{}, startSequence...),
				streamTransition{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone}),
			wantClass:  RecoveryOrphanedCanceled,
			wantReason: "owner death during cancellation",
		},
		{
			name: "awaiting-decision head belongs to resume not repair",
			transitions: append(append([]streamTransition{}, startSequence...),
				awaitDecisionTransition()),
			wantClass:  RecoveryRecoverable,
			wantReason: "awaiting-decision head",
		},
		{
			name:        "running head requires an operator decision",
			transitions: startSequence,
			wantClass:   RecoveryOperatorRequired,
			wantReason:  "outcome unknown",
		},
		{
			name: "already settled terminal stream needs no repair",
			transitions: append(append([]streamTransition{}, startSequence...),
				successTransition()),
			wantClass:  RecoverySettled,
			wantReason: "no recovery work applies",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewStore(t.TempDir())
			job, directory := buildScanRun(t, s, "candidate:"+tt.name, tt.transitions)
			root := filepath.Dir(directory)
			before := digestsWithoutLockFiles(hashExecutionTree(t, root))

			_, err := RepairTerminalUnprojected(s, string(job.RunID()))
			assertRefusedWithReason(t, err, tt.wantClass, tt.wantReason)

			after := digestsWithoutLockFiles(hashExecutionTree(t, root))
			beforeJSON := marshalRecordOrDie(t, before)
			afterJSON := marshalRecordOrDie(t, after)
			if string(beforeJSON) != string(afterJSON) {
				t.Fatalf("refusal wrote to the store:\nbefore %s\nafter  %s", beforeJSON, afterJSON)
			}
		})
	}
}

// TestRepairUnderHeldLockFailsCleanlyWithoutPartialWrites shrinks the lock
// wait, holds the execution lock in-process, and proves the contention error
// path fails cleanly with no partial snapshot. After release, the same repair
// succeeds.
func TestRepairUnderHeldLockFailsCleanlyWithoutPartialWrites(t *testing.T) {
	s := NewStore(t.TempDir())
	job, directory := buildScanRun(t, s, "candidate:contended",
		append(append([]streamTransition{}, startSequence...), successTransition()))
	runID := string(job.RunID())
	snapshotPath := filepath.Join(directory, "state.json")
	removeSnapshot(t, directory)

	oldWait := eventLockWait
	eventLockWait = 50 * time.Millisecond
	t.Cleanup(func() { eventLockWait = oldWait })
	lock, err := acquireExecutionLock(filepath.Join(directory, ".events.lock"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, repairErr := RepairTerminalUnprojected(s, runID)
	closeErr := lock.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if repairErr == nil {
		t.Fatal("repair acquired a lock held by another owner")
	}
	if !strings.Contains(repairErr.Error(), "timeout acquiring execution event lock") {
		t.Fatalf("contention error = %v, want the lock-timeout failure", repairErr)
	}
	if _, statErr := os.Stat(snapshotPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial snapshot appeared during contention: stat error = %v", statErr)
	}

	result, err := RepairTerminalUnprojected(s, runID)
	if err != nil {
		t.Fatalf("repair after release error = %v", err)
	}
	if result.ClassAfter != RecoverySettled {
		t.Fatalf("post-contention class = %s, want settled", result.ClassAfter)
	}
}
