package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Ticket 10 slice 1: `sentinel runs recover` without --run is an explicit
// read-only scan listing every non-terminal run with its evidence-based
// class. These tests drive the CLI surface over synthetic stores built with
// the real append machinery and pin the exit-code contract: 0 when nothing
// requires attention, 4 when an entry requires an operator decision.

func TestRunsRecoverScanExitsZeroOnEmptyStore(t *testing.T) {
	backing := store.NewStore(t.TempDir())

	var out bytes.Buffer
	if code := listRecoveries(&out, backing, false); code != runExitSuccess {
		t.Fatalf("listRecoveries(empty) exit = %d, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "No non-terminal durable runs") {
		t.Fatalf("empty scan output = %q, want an explicit nothing-to-recover line", out.String())
	}

	out.Reset()
	if code := listRecoveries(&out, backing, true); code != runExitSuccess {
		t.Fatalf("JSON listRecoveries(empty) exit = %d", code)
	}
	var decoded runsRecoveryOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Recoveries) != 0 {
		t.Fatalf("JSON recoveries = %+v, want an empty array", decoded.Recoveries)
	}
}

func TestRunsRecoverScanListsClassesAndExitCodes(t *testing.T) {
	backing := store.NewStore(t.TempDir())
	awaiting := appendReconciledFixtureStream(t, backing, "candidate:awaiting", []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.DecisionNone},
	})
	orphaned := appendReconciledFixtureStream(t, backing, "candidate:orphaned", []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateRunning, agentrun.StateTerminating, agentrun.DecisionNone},
	})
	settled := appendReconciledFixtureStream(t, backing, "candidate:settled", []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete},
	})

	var out bytes.Buffer
	if code := listRecoveries(&out, backing, false); code != runExitSuccess {
		t.Fatalf("listRecoveries exit = %d without operator-required entries, output:\n%s", code, out.String())
	}
	text := out.String()
	if !strings.Contains(text, "class=recoverable") || !strings.Contains(text, string(awaiting)) {
		t.Fatalf("scan did not surface the recoverable run:\n%s", text)
	}
	if !strings.Contains(text, "class=orphaned_canceled") || !strings.Contains(text, "(reconciled)") {
		t.Fatalf("scan did not surface the reconciled orphaned-canceled run:\n%s", text)
	}
	if strings.Contains(text, string(settled)) {
		t.Fatalf("settled run %s leaked into the recovery scan:\n%s", settled, text)
	}

	out.Reset()
	if code := listRecoveries(&out, backing, true); code != runExitSuccess {
		t.Fatalf("JSON listRecoveries exit = %d", code)
	}
	var decoded runsRecoveryOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Recoveries) != 2 {
		t.Fatalf("JSON recoveries = %+v, want exactly the two non-terminal runs", decoded.Recoveries)
	}
	classes := map[string]string{}
	for _, row := range decoded.Recoveries {
		classes[row.RunID] = row.Class
	}
	if classes[string(awaiting)] != "recoverable" || classes[string(orphaned)] != "orphaned_canceled" {
		t.Fatalf("JSON classes = %v, want recoverable and orphaned_canceled", classes)
	}

	// A plain running head cannot decide recovery on its own: the scan must
	// surface it as operator-required and exit 4 so automation notices.
	running := appendReconciledFixtureStream(t, backing, "candidate:running", nil)

	out.Reset()
	if code := listRecoveries(&out, backing, false); code != runExitInvalidState {
		t.Fatalf("listRecoveries exit = %d with an operator-required entry, output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "class=operator_required") || !strings.Contains(out.String(), string(running)) {
		t.Fatalf("scan did not surface the operator-required run:\n%s", out.String())
	}

	out.Reset()
	if code := listRecoveries(&out, backing, true); code != runExitInvalidState {
		t.Fatalf("JSON listRecoveries exit = %d with an operator-required entry", code)
	}
}
