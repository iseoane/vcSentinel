package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// Ticket 10 slice 2: `vcsentinel runs recover --repair <id>` rebuilds the
// lagging state snapshot of one terminal-unprojected run from its verified
// stream. These tests drive the CLI surface over synthetic stores built with
// the real append machinery and pin the exit-code contract: 0 repaired,
// 1 usage, 2 unknown run, 4 refusal classes that are invalid actions right
// now, 5 corruption or infrastructure failure.

type fixtureTransition = struct {
	from     agentrun.LifecycleState
	to       agentrun.LifecycleState
	decision agentrun.Decision
}

func successExtra() []fixtureTransition {
	return []fixtureTransition{
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete},
	}
}

func awaitingExtra() []fixtureTransition {
	return []fixtureTransition{
		{agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.DecisionNone},
	}
}

func terminatingExtra() []fixtureTransition {
	return []fixtureTransition{
		{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone},
	}
}

// snapshotPathFor locates the persisted state.json of one run so tests can
// simulate the crash window by removing the snapshot after a real append.
func snapshotPathFor(root, runID string) string {
	return filepath.Join(root, "vcsentinel", "executions", "v1", runID, "state.json")
}

func TestRunsRecoverRepairRestoresUnprojectedRun(t *testing.T) {
	root := t.TempDir()
	backing := store.NewStore(root)
	runID := string(appendReconciledFixtureStream(t, backing, "candidate:repair-me", successExtra()))
	snapshotPath := snapshotPathFor(root, runID)
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := repairRunProjection(&out, backing, runID, false); code != runExitSuccess {
		t.Fatalf("repair exit = %d, output:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "terminal_unprojected") || !strings.Contains(text, "settled") {
		t.Fatalf("repair output did not print the before/after class transition:\n%s", text)
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot was not restored: %v", err)
	}
	entries, scanErr := store.ScanRecoveries(backing)
	if scanErr != nil || len(entries) != 0 {
		t.Fatalf("post-repair scan = %+v, error = %v; want nothing left to recover", entries, scanErr)
	}

	// A second crash-window run proves the JSON shape of a successful repair.
	jsonRunID := string(appendReconciledFixtureStream(t, backing, "candidate:repair-me-json", successExtra()))
	if err := os.Remove(snapshotPathFor(root, jsonRunID)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := repairRunProjection(&out, backing, jsonRunID, true); code != runExitSuccess {
		t.Fatalf("JSON repair exit = %d, output:\n%s", code, out.String())
	}
	var decoded runsRepairOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RunID != jsonRunID || decoded.ClassBefore != "terminal_unprojected" ||
		decoded.ClassAfter != "settled" || !decoded.Rewritten {
		t.Fatalf("JSON repair result = %+v, want the rewritten settled transition", decoded)
	}

	// The repaired run is settled: a repeat repair refuses as an invalid
	// action instead of rewriting anything.
	refusalOut := &bytes.Buffer{}
	if code := repairRunProjection(refusalOut, backing, runID, false); code != runExitInvalidState {
		t.Fatalf("repeat repair exit = %d, output:\n%s", code, refusalOut.String())
	}
	if !strings.Contains(refusalOut.String(), "class settled") {
		t.Fatalf("repeat refusal did not name its class:\n%s", refusalOut.String())
	}
}

func TestRunsRecoverRepairRefusesNonUnprojectedClasses(t *testing.T) {
	corruptRoot := t.TempDir()
	corruptBacking := store.NewStore(corruptRoot)
	corruptRunID := string(appendReconciledFixtureStream(t, corruptBacking, "candidate:cli-corrupt", nil))
	eventsPath := filepath.Join(corruptRoot, "vcsentinel", "executions", "v1", corruptRunID, "events.jsonl")
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := "\"content_hash\":\""
	start := strings.Index(string(data), marker)
	if start < 0 {
		t.Fatal("test setup error: no content hash found")
	}
	digit := start + len(marker)
	if data[digit] == '0' {
		data[digit] = '1'
	} else {
		data[digit] = '0'
	}
	if err := os.WriteFile(eventsPath, data, 0600); err != nil {
		t.Fatal(err)
	}

	awaitingRoot := t.TempDir()
	awaitingBacking := store.NewStore(awaitingRoot)
	awaitingRunID := string(appendReconciledFixtureStream(t, awaitingBacking, "candidate:cli-awaiting", awaitingExtra()))

	orphanedRoot := t.TempDir()
	orphanedBacking := store.NewStore(orphanedRoot)
	orphanedRunID := string(appendReconciledFixtureStream(t, orphanedBacking, "candidate:cli-orphaned", terminatingExtra()))

	tests := []struct {
		name    string
		backing *store.Store
		runID   string
		want    int
		wantIn  string
	}{
		{
			name:    "corrupt stream exits infrastructure",
			backing: corruptBacking, runID: corruptRunID,
			want:   runExitInfrastructure,
			wantIn: "class corrupt",
		},
		{
			name:    "awaiting-decision head exits invalid state",
			backing: awaitingBacking, runID: awaitingRunID,
			want:   runExitInvalidState,
			wantIn: "class recoverable",
		},
		{
			name:    "orphaned-canceled verdict exits invalid state",
			backing: orphanedBacking, runID: orphanedRunID,
			want:   runExitInvalidState,
			wantIn: "class orphaned_canceled",
		},
		{
			name:    "unknown run exits not-found",
			backing: store.NewStore(t.TempDir()), runID: "missing-run-id",
			want:   runExitRunNotFound,
			wantIn: "does not exist",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := repairRunProjection(&out, tt.backing, tt.runID, false); code != tt.want {
				t.Fatalf("repair exit = %d, want %d, output:\n%s", code, tt.want, out.String())
			}
			if !strings.Contains(out.String(), tt.wantIn) {
				t.Fatalf("refusal output = %q, want it to contain %q", out.String(), tt.wantIn)
			}
		})
	}
}

func TestRunsRecoverRepairUsageErrors(t *testing.T) {
	worktree := t.TempDir()
	tests := [][]string{
		{"recover", "--repair"},                                   // missing value
		{"recover", "--repair", ""},                               // empty identity
		{"recover", "--repair", "some-run", "--run", "other-run"}, // ambiguous
	}
	for _, args := range tests {
		var out bytes.Buffer
		if code := executeRunsRecover(&out, worktree, args[1:]); code != runExitUsage {
			t.Fatalf("executeRunsRecover(%v) exit = %d, want usage 1, output:\n%s", args, code, out.String())
		}
		if !strings.Contains(out.String(), "❌") {
			t.Fatalf("usage output = %q, want an explicit error line", out.String())
		}
	}
}
