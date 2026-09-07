// Cutover proof for the wiring-time review-child learning seam (ticket 11
// slice 3). Review candidate identities are process-salted inside the shared
// durable transport, so the orchestrator learns its actually-admitted review
// children from the DurableReviewChildren observer sink. This file pins that,
// when the factory routes reviews into the SAME store as the root run, the
// root settlement's "|children=" enumeration equals the persisted ParentRunID
// scan — the exact production cutover shape.
package gate

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestGateDurableLearnedReviewChildrenEnumerateScanned(t *testing.T) {
	const blockJSON = `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo confirmable"}]}`
	transports := 0
	var learned []string

	base := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil),
		countingFactory(new(int), blockJSON, nil))
	base.RunValidation = runProfileWithoutCandidate
	base.RefuterFactory = func() (review.AgentReviewer, string, error) {
		return &fakeReviewer{output: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
	}

	durable := base
	durable.Stage = "pre-push"
	durable.CandidateSHA = base.ReviewOptions.SHA
	// ONE store for the root run, the validation-job settlements, and every
	// routed review invocation: the production cutover shape.
	st := store.NewStore(filepath.Join(t.TempDir(), "gate-common"))
	durable.DurableStore = st
	durable.DurableReviewChildren = func() []agentrun.Identity {
		ids := make([]agentrun.Identity, 0, len(learned))
		for _, id := range learned {
			ids = append(ids, agentrun.Identity(id))
		}
		return ids
	}
	durable.DurableReviewTransportFactory = func(rootRunID agentrun.Identity) review.ReviewTransport {
		transports++
		transport := reviewexec.NewDurableTransport(st,
			store.RunPolicy{ID: "policy:test-gate-review", ParentRunID: string(rootRunID)},
			base.ReviewOptions.SHA, nil,
			reviewexec.WithRunObserver(func(runID string) { learned = append(learned, runID) }))
		return func(bundleName, dimension, prompt string, agent review.AgentReviewer) (string, string, error) {
			restricted, ok := agent.(reviewexec.RestrictedReviewer)
			if !ok {
				return "", "", review.ErrRestrictedRequired
			}
			output, evidence, err := transport.Run(restricted, bundleName+"/"+dimension, prompt)
			if err != nil {
				return "", "", err
			}
			return output, evidence.InvocationID, nil
		}
	}

	result := RunGate(durable)

	if result.State != StateCodeReviewFailed {
		t.Fatalf("state = %q, expected %q", result.State, StateCodeReviewFailed)
	}
	if transports < 1 {
		t.Fatal("review transport factory was never invoked, expected at least one routed audit")
	}
	if len(learned) == 0 {
		t.Fatal("the observer sink learned no review child identity")
	}

	summary := reconstructFromStore(t, st)

	// The salted review runs must be enumerable: the machine-parseable
	// settlement suffix and the persisted ParentRunID scan must describe the
	// SAME set of children (validation jobs plus every admitted review run).
	if !slices.Equal(sortedCopy(summary.enumerated), sortedCopy(summary.scanned)) {
		t.Fatalf("enumerated children %v != ParentRunID scan %v", summary.enumerated, summary.scanned)
	}
	wantChildren := 1 + len(learned) // single-capability profile: one validation job
	if len(summary.scanned) != wantChildren {
		t.Fatalf("scanned children = %d (%v), want %d (1 validation job + %d learned review runs)",
			len(summary.scanned), summary.scanned, wantChildren, len(learned))
	}
	wantState, wantLayer := expectedTerminal(result.State)
	if summary.rootState != wantState || summary.layer != wantLayer {
		t.Fatalf("reconstructed terminal = {%v %s}, want {%v %s}", summary.rootState, summary.layer, wantState, wantLayer)
	}
	// Sensible classification of the settled tree: every child reached a
	// terminal attempt exactly once and none of them failed (the reviewer
	// answered; the BLOCK verdict belongs to the caller-side engine, so only
	// the ROOT carries the failure).
	for _, class := range []agentrun.OutcomeClass{agentrun.OutcomeFailure, agentrun.OutcomeUnavailable} {
		if summary.classes[class] != 0 {
			t.Fatalf("child classes recorded %d %s outcomes, expected none on children", summary.classes[class], class)
		}
	}
	if summary.classes[agentrun.OutcomeSuccess] != wantChildren {
		t.Fatalf("child success count = %d, want %d", summary.classes[agentrun.OutcomeSuccess], wantChildren)
	}
}
