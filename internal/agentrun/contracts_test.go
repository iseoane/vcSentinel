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

func TestRetryableTerminalStatesRelaunchThroughRetryDecision(t *testing.T) {
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest("tree:retry", "prompt", nil))
	root, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		from, to  agentrun.LifecycleState
		decision  agentrun.Decision
		allowed   bool
		wantError bool
	}{
		{"failed relaunches with retry", agentrun.StateFailed, agentrun.StateRunning, agentrun.DecisionRetry, true, false},
		{"canceled relaunches with retry", agentrun.StateCanceled, agentrun.StateRunning, agentrun.DecisionRetry, true, false},
		{"timed out relaunches with retry", agentrun.StateTimedOut, agentrun.StateRunning, agentrun.DecisionRetry, true, false},
		{"failed relaunch needs the retry decision", agentrun.StateFailed, agentrun.StateRunning, agentrun.DecisionNone, true, true},
		{"failed relaunch rejects a respond decision", agentrun.StateFailed, agentrun.StateRunning, agentrun.DecisionRespond, true, true},
		{"retry cannot drive a running continuation", agentrun.StateRunning, agentrun.StateAwaitingDecision, agentrun.DecisionRetry, true, true},
		{"success stays final", agentrun.StateSucceeded, agentrun.StateRunning, agentrun.DecisionRetry, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentrun.Transition(tc.from, tc.to); (got == nil) != tc.allowed {
				t.Fatalf("transition error = %v, allowed = %t", got, tc.allowed)
			}
			_, err := agentrun.NewNormalizedEvent(root, tc.from, tc.to, tc.decision, time.Unix(10, 0))
			if (err == nil) == tc.wantError {
				t.Fatalf("NewNormalizedEvent() error = %v, wantError = %t", err, tc.wantError)
			}
			if tc.wantError && tc.allowed {
				var invalid agentrun.InvalidDecisionError
				if !errors.As(err, &invalid) || invalid.From != tc.from || invalid.To != tc.to || invalid.Decision != tc.decision {
					t.Fatalf("error = %#v, want decision binding evidence", err)
				}
			}
			if !tc.wantError {
				if got := agentrun.LifecycleState(tc.from); !got.Retryable() {
					t.Fatalf("%q must report retryable", got)
				}
			}
		})
	}
}

func TestNewRetryInvocationExtendsAttemptLineageInsideSameRun(t *testing.T) {
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest("tree:retry-lineage", "prompt", nil))
	root, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := agentrun.NewRetryInvocation(root)
	if err != nil {
		t.Fatal(err)
	}
	if retry.RunID() != job.RunID() || retry.JobID() != job.ID() || !retry.BelongsTo(job) {
		t.Fatal("a retry invocation must stay inside its original run and job")
	}
	if retry.Attempt() != 2 || retry.Decision() != agentrun.DecisionRetry || retry.ParentInvocationID() != root.InvocationID() {
		t.Fatalf("retry = attempt %d decision %q parent %q, want attempt 2, retry, and root parent", retry.Attempt(), retry.Decision(), retry.ParentInvocationID())
	}
	chained, err := agentrun.NewRetryInvocation(retry)
	if err != nil || chained.Attempt() != 3 {
		t.Fatalf("chained retry attempt = %d, %v; want 3", chained.Attempt(), err)
	}
	recovered, err := agentrun.NewRecoveredInvocation(job.RunID(), job.ID(), root.InvocationID(), root.LineageIdentity(), "", []agentrun.Identity{job.ID()}, 4)
	if err != nil {
		t.Fatal(err)
	}
	rerecovered, err := agentrun.NewRetryInvocation(recovered)
	if err != nil || rerecovered.Attempt() != 5 || rerecovered.RunID() != job.RunID() {
		t.Fatalf("recovered retry attempt = %d, %v; want 5 inside run", rerecovered.Attempt(), err)
	}
	if _, err := agentrun.NewRecoveredInvocation(job.RunID(), job.ID(), root.InvocationID(), root.LineageIdentity(), "", []agentrun.Identity{job.ID()}, 0); err == nil {
		t.Fatal("a recovered invocation without an attempt number must be rejected")
	}
}

func TestNewRecoveredInvocationPreservesContinuationLineage(t *testing.T) {
	job := agentrun.NewLogicalJob(agentrun.NewRunRequest("tree:recovered-lineage", "prompt", nil))
	root, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	head, err := agentrun.NewChildInvocation(root, 2, agentrun.DecisionRespond)
	if err != nil {
		t.Fatal(err)
	}

	ancestry := []agentrun.Identity{job.ID(), root.InvocationID()}
	recovered, err := agentrun.NewRecoveredInvocation(
		job.RunID(), job.ID(), head.InvocationID(), head.LineageIdentity(),
		root.InvocationID(), ancestry, head.Attempt())
	if err != nil {
		t.Fatal(err)
	}
	live, err := agentrun.NewChildInvocation(head, 3, agentrun.DecisionRespond)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := agentrun.NewChildInvocation(recovered, 3, agentrun.DecisionRespond)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.LineageIdentity() != live.LineageIdentity() || resumed.InvocationID() != live.InvocationID() {
		t.Fatalf("recovered continuation = lineage %s invocation %s; want the live identities %s/%s",
			resumed.LineageIdentity(), resumed.InvocationID(), live.LineageIdentity(), live.InvocationID())
	}

	broken := [][]agentrun.Identity{
		nil,
		{root.InvocationID()},
		{job.ID()},
		{job.ID(), root.InvocationID(), job.ID()},
	}
	for _, ancestors := range broken {
		if _, err := agentrun.NewRecoveredInvocation(
			job.RunID(), job.ID(), head.InvocationID(), head.LineageIdentity(),
			root.InvocationID(), ancestors, head.Attempt()); err == nil {
			t.Fatalf("ancestors %v must be rejected as an inconsistent recovered chain", ancestors)
		}
	}
}
