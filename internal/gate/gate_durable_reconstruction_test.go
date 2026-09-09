// Reconstruction contract test for ticket 11: given ONLY the admitted store
// contents of one durable gate execution — the root run record, its children
// discovered through the persisted ParentRunID linkage, and the root's
// terminal settlement detail — the gate summary (terminal state, failing
// layer, child enumeration, per-validation-job evidence bindings, final
// class) must rebuild identically to what RunGate returned live. This
// test is the contract; every value asserted here comes from store reads,
// never from in-process orchestration state.
package gate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// reconstructedSummary is everything the store alone can tell about one
// durable gate execution.
type reconstructedSummary struct {
	// rootState is the settled lifecycle state of the root run.
	rootState agentrun.LifecycleState
	// layer names the failing layer parsed from the root settlement detail
	// ("validation" | "review" | "infrastructure"); empty means green.
	layer string
	// enumerated are the child run IDs machine-parsed from the root
	// settlement detail's "|children=" suffix.
	enumerated []string
	// scanned are the child run IDs discovered purely through the persisted
	// ParentRunID linkage of their admission records.
	scanned []string
	// classes count the terminal AttemptOutcome classes across children.
	classes map[agentrun.OutcomeClass]int
	// outputHashes collects every child's persisted OutputHash.
	outputHashes []string
}

// reconstructFromStore rebuilds the summary from STORE CONTENTS ONLY.
func reconstructFromStore(t *testing.T, st *store.Store) reconstructedSummary {
	t.Helper()
	ids, err := st.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs() error = %v", err)
	}
	parents := make(map[string]string, len(ids))
	var rootID string
	for _, id := range ids {
		request, err := st.ReadExecutionRequest(id)
		if err != nil {
			t.Fatalf("ReadExecutionRequest(%s) error = %v", id, err)
		}
		parents[id] = request.ParentRunID
		if request.ParentRunID == "" {
			if rootID != "" {
				t.Fatalf("store holds multiple parentless runs (%s, %s)", rootID, id)
			}
			rootID = id
		}
	}
	if rootID == "" {
		t.Fatal("store holds no root run")
	}

	summary := reconstructedSummary{classes: map[agentrun.OutcomeClass]int{}}
	for _, id := range ids {
		if parents[id] == "" {
			continue
		}
		if parents[id] != rootID {
			t.Fatalf("run %s links to foreign parent %s", id, parents[id])
		}
		summary.scanned = append(summary.scanned, id)
		inspection, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(id))
		if err != nil {
			t.Fatalf("child %s inspection failed: %v", id, err)
		}
		if len(inspection.Outcomes) != 1 {
			t.Fatalf("child %s recorded %d outcomes, expected exactly one terminal attempt", id, len(inspection.Outcomes))
		}
		outcome := inspection.Outcomes[0]
		summary.classes[outcome.Class]++
		if outcome.OutputHash != "" {
			summary.outputHashes = append(summary.outputHashes, outcome.OutputHash)
		}
	}

	inspection, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(rootID))
	if err != nil {
		t.Fatalf("root inspection failed: %v", err)
	}
	summary.rootState = inspection.Projection.State
	for _, outcome := range inspection.Outcomes {
		summary.layer = layerFromDetail(outcome.Error)
		if suffix, ok := childrenSuffix(outcome.Error); ok {
			summary.enumerated = suffix
		}
	}
	return summary
}

// layerFromDetail parses the failing-layer marker out of a settlement
// detail; empty means no layer marker (a green root).
func layerFromDetail(detail string) string {
	for _, layer := range []string{"validation", "review", "infrastructure"} {
		if strings.Contains(detail, "failing layer: "+layer) {
			return layer
		}
	}
	return ""
}

// childrenSuffix parses the machine-parseable "|children=<id,id,...>" suffix of
// a settlement detail.
func childrenSuffix(detail string) ([]string, bool) {
	marker := "|children="
	index := strings.Index(detail, marker)
	if index < 0 {
		return nil, false
	}
	list := detail[index+len(marker):]
	if list == "" {
		return []string{}, true
	}
	return strings.Split(list, ","), true
}

// expectedTerminal maps a live facade state onto the durable vocabulary so
// reconstruction can be compared against what RunGate returned.
func expectedTerminal(state string) (agentrun.LifecycleState, string) {
	switch state {
	case StatePass:
		return agentrun.StateSucceeded, ""
	case StateValidationFailed:
		return agentrun.StateFailed, "validation"
	default:
		return agentrun.StateUnavailable, "infrastructure"
	}
}

// sortedCopy returns a sorted copy of a string slice.
func sortedCopy(values []string) []string {
	copied := slices.Clone(values)
	slices.Sort(copied)
	return copied
}

// assertBoundEvidence proves the multiset of child OutputHashes equals the
// HashAdapterOutput digest of every validation evidence serialization.
func assertBoundEvidence(t *testing.T, evidence []ValidationEvidence, got []string) {
	t.Helper()
	want := make([]string, 0, len(evidence))
	for _, entry := range evidence {
		want = append(want, execution.HashAdapterOutput(entry.String()))
	}
	slices.Sort(want)
	gotSorted := slices.Clone(got)
	slices.Sort(gotSorted)
	if !slices.Equal(want, gotSorted) {
		t.Fatalf("child OutputHashes %v do not bind the evidence serializations %v", gotSorted, want)
	}
}

func TestGateDurableReconstructionFromStore(t *testing.T) {
	t.Run("validation failure reconstructs layer outcome, children, and evidence", func(t *testing.T) {
		base := baseOptions(t, cfgWithTwoCapabilities(), selectedExecutor(map[string]validation.ValidationRun{
			"echo test": {Exit: 1, Output: "real output of the failed command"},
		}))
		var captured []validation.ValidationRun
		base.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
			runs, err := runProfileWithoutCandidate(profile, scope, o)
			captured = runs
			return runs, err
		}
		durableOpts := durableOptions(t, base)

		result := RunGate(durableOpts)

		if result.State != StateValidationFailed {
			t.Fatalf("state = %q, expected %q", result.State, StateValidationFailed)
		}

		summary := reconstructFromStore(t, durableOpts.DurableStore)

		wantState, wantLayer := expectedTerminal(result.State)
		if summary.rootState != wantState || summary.layer != wantLayer {
			t.Fatalf("reconstructed terminal = {%v %s}, want {%v %s} for live state %q",
				summary.rootState, summary.layer, wantState, wantLayer, result.State)
		}
		// Set equality: enumeration order is profile/admission order while
		// the scan order is the store's lexicographic listing.
		if !slices.Equal(sortedCopy(summary.enumerated), sortedCopy(summary.scanned)) {
			t.Fatalf("enumerated children %v != ParentRunID scan %v", summary.enumerated, summary.scanned)
		}
		if len(summary.scanned) != 2 {
			t.Fatalf("scanned children = %d, expected both validation jobs", len(summary.scanned))
		}
		if summary.classes[agentrun.OutcomeSuccess] != 1 || summary.classes[agentrun.OutcomeFailure] != 1 {
			t.Fatalf("child classes = %v, expected one success and one failure", summary.classes)
		}
		assertBoundEvidence(t, RecordValidationEvidence(captured), summary.outputHashes)
	})

	t.Run("green gate reconstructs success from the root record alone", func(t *testing.T) {
		base := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil))
		var captured []validation.ValidationRun
		base.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
			runs, err := runProfileWithoutCandidate(profile, scope, o)
			captured = runs
			return runs, err
		}
		durableOpts := durableOptions(t, base)

		result := RunGate(durableOpts)

		if result.State != StatePass {
			t.Fatalf("state = %q, expected %q", result.State, StatePass)
		}

		summary := reconstructFromStore(t, durableOpts.DurableStore)

		wantState, wantLayer := expectedTerminal(result.State)
		if summary.rootState != wantState || summary.layer != wantLayer {
			t.Fatalf("reconstructed terminal = {%v %s}, want {%v %s} for live state %q",
				summary.rootState, summary.layer, wantState, wantLayer, result.State)
		}
		// Green settlements carry no children suffix; the scan still finds
		// both validation jobs through their persisted parent linkage. The
		// review-side runs live in the factory's own backing store, so this
		// store holds exactly root plus validation children.
		if len(summary.enumerated) != 0 {
			t.Fatalf("green root enumerated children %v, expected none", summary.enumerated)
		}
		if len(summary.scanned) != 1 {
			t.Fatalf("scanned children = %d, expected the single validation job", len(summary.scanned))
		}
		if summary.classes[agentrun.OutcomeSuccess] != 1 {
			t.Fatalf("child classes = %v, expected one success", summary.classes)
		}
		assertBoundEvidence(t, RecordValidationEvidence(captured), summary.outputHashes)
	})
}
