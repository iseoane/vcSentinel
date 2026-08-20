package agentrun_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

func TestCanonicalizationOwnsInputsAndUsesDomainSeparatedHashes(t *testing.T) {
	attributes := map[string]string{"mode": "read", "scope": "snapshot"}
	candidates := []agentrun.Capability{
		agentrun.NewCapability("filesystem", attributes),
		agentrun.NewCapability("network", map[string]string{"host": "none"}),
	}
	request := agentrun.NewRunRequest("commit:abc", "review prompt", candidates)
	attributes["mode"] = "write"
	candidates[0] = agentrun.NewCapability("mutated", nil)
	other := agentrun.NewRunRequest("commit:abc", "review prompt", []agentrun.Capability{
		agentrun.NewCapability("network", map[string]string{"host": "none"}),
		agentrun.NewCapability("filesystem", map[string]string{"scope": "snapshot", "mode": "read"}),
	})
	if request.Canonical() != other.Canonical() || request.Identity() != other.Identity() {
		t.Fatal("equivalent requests must have identical canonical output and identity")
	}
	for _, id := range []agentrun.Identity{
		agentrun.CandidateIdentity("x"), agentrun.PromptIdentity("x"),
		candidates[1].Identity(), request.Identity(),
	} {
		if len(id) != 64 {
			t.Fatalf("identity length = %d, want SHA-256 hex", len(id))
		}
	}
	if agentrun.CandidateIdentity("x") == agentrun.PromptIdentity("x") {
		t.Fatal("candidate and prompt identities must use separate hash domains")
	}
}

func TestLifecycleTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from, to agentrun.LifecycleState
		valid    bool
		terminal agentrun.TerminalClass
	}{
		{"created queues", agentrun.StateCreated, agentrun.StateQueued, true, agentrun.TerminalNone},
		{"running waits", agentrun.StateRunning, agentrun.StateAwaitingDecision, true, agentrun.TerminalNone},
		{"waiting succeeds", agentrun.StateAwaitingDecision, agentrun.StateSucceeded, true, agentrun.TerminalSuccess},
		{"running cancels", agentrun.StateRunning, agentrun.StateCanceled, true, agentrun.TerminalCancellation},
		{"created cannot run", agentrun.StateCreated, agentrun.StateRunning, false, agentrun.TerminalNone},
		{"terminal cannot restart", agentrun.StateSucceeded, agentrun.StateRunning, false, agentrun.TerminalNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentrun.Transition(tc.from, tc.to); (got == nil) != tc.valid {
				t.Fatalf("transition error = %v, valid = %t", got, tc.valid)
			}
			if got := tc.to.TerminalClass(); got != tc.terminal {
				t.Fatalf("terminal class = %q, want %q", got, tc.terminal)
			}
		})
	}
}

func TestInvalidTransitionIsInspectable(t *testing.T) {
	err := agentrun.Transition(agentrun.StateSucceeded, agentrun.StateRunning)
	var invalid agentrun.InvalidTransitionError
	if !errors.As(err, &invalid) || invalid.From != agentrun.StateSucceeded || invalid.To != agentrun.StateRunning {
		t.Fatalf("error = %#v, want source and target", err)
	}
}

func TestInvocationLineageBindsPhysicalAttemptsToLogicalJob(t *testing.T) {
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest("tree:abc", "prompt", nil))
	root, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	child, err := agentrun.NewChildInvocation(root, 2, agentrun.DecisionRetry)
	if err != nil {
		t.Fatal(err)
	}
	if !root.BelongsTo(job) || !child.BelongsTo(job) || child.ParentInvocationID() != root.InvocationID() || child.RootIdentity() != job.ID() {
		t.Fatal("lineage lost its logical job, run, or parent/root identity")
	}
	ancestors := child.AncestorIDs()
	ancestors[0] = "tampered"
	if child.RootIdentity() != job.ID() || child.LineageIdentity() == root.LineageIdentity() {
		t.Fatal("lineage must own ancestor data and distinguish attempts")
	}
	if _, err := agentrun.NewInvocationEnvelope(job, root.InvocationID(), []agentrun.Identity{job.ID()}, 2, agentrun.DecisionRetry); err == nil {
		t.Fatal("a child with a missing parent ancestor must be rejected")
	}
	event, err := agentrun.NewNormalizedEvent(child, agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if event.RunID() != job.RunID() || event.JobID() != job.ID() || event.InvocationID() != child.InvocationID() || event.LineageIdentity() != child.LineageIdentity() || event.TerminalClass() != agentrun.TerminalSuccess {
		t.Fatal("normalized event must retain execution and lineage identity")
	}
}
