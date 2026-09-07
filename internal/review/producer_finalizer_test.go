package review_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

type finalizerAgent struct{}

func (*finalizerAgent) RunPrompt(string) (string, error) {
	return `{"dim":"logic","verdict":"ok"}`, nil
}

func (*finalizerAgent) ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error) {
	return `{"dim":"logic","verdict":"ok"}`, nil
}

func TestDimensionReviewerFinalizesEachPhysicalRunAfterSemanticDisposition(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	var finalized []struct {
		runID, invocation, class, detail string
	}
	transport := func(_, _, _ string, _ review.AgentReviewer) (string, review.ReviewEvidence, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return "not-json", review.ReviewEvidence{RunID: "run-invalid", InvocationID: "invocation-invalid"}, nil
		}
		return `{"dim":"logic","verdict":"ok"}`, review.ReviewEvidence{RunID: "run-valid", InvocationID: "invocation-valid"}, nil
	}
	factory := func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		return &finalizerAgent{}, "normal", nil
	}
	result := review.AuditCommit(factory, 1, review.AuditOptions{
		SHA: "semantic-finalizer",
		Bundles: []review.ReviewBundle{{
			Name: "test", Dimensions: []string{review.DimLogic}, Priority: review.PriorityRequired, Cost: 1,
		}},
		ReviewTransportWithEvidence: transport,
		FinalizeMetrics: func(runID, invocation, class, detail string) error {
			finalized = append(finalized, struct {
				runID, invocation, class, detail string
			}{runID: runID, invocation: invocation, class: class, detail: detail})
			return nil
		},
	})
	if result.Verdict != review.VerdictOK {
		t.Fatalf("result = %+v, want successful corrective retry", result)
	}
	if len(finalized) != 2 {
		t.Fatalf("finalized = %+v, want both physical runs finalized", finalized)
	}
	// The recorded class is the semantic classification of the failure, not a
	// blanket invalid_output: "not-json" carries no semantic payload at all,
	// and a denied tool would arrive under its own class rather than being
	// filed as malformed output.
	if finalized[0].runID != "run-invalid" || finalized[0].invocation != "invocation-invalid" ||
		finalized[0].class != string(review.SemanticOutputMissingPayload) {
		t.Fatalf("first finalization = %+v, want the %q disposition", finalized[0], review.SemanticOutputMissingPayload)
	}
	if finalized[1].runID != "run-valid" || finalized[1].invocation != "invocation-valid" || finalized[1].class != "" {
		t.Fatalf("second finalization = %+v, want clean semantic success", finalized[1])
	}
}

type durableRetryAgent struct {
	mu    sync.Mutex
	calls int
}

func (*durableRetryAgent) RunPrompt(string) (string, error) {
	return "", errors.New("legacy path must not be used")
}

func (a *durableRetryAgent) ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.calls == 1 {
		return "", errors.New("provider failed")
	}
	return `{"dim":"logic","verdict":"ok"}`, nil
}

func (*durableRetryAgent) ReviewToolPolicy() reviewcontract.ToolPolicy {
	return reviewcontract.ToolPolicy{AllowRead: true, AllowSearch: true, RequireImmutableSnapshot: true}
}

func TestProviderRetryFinalizesBothPhysicalRuns(t *testing.T) {
	backing := store.NewStore(t.TempDir())
	transport := reviewexec.NewDurableTransport(backing, store.RunPolicy{ID: "policy:review"}, "sha-retry", nil)
	agent := &durableRetryAgent{}
	rich := func(bundleName, dimension, prompt string, reviewer review.AgentReviewer) (string, review.ReviewEvidence, error) {
		restricted := reviewer.(reviewexec.PolicyRestrictedReviewer)
		provider := reviewer.(reviewexec.PolicyProvider)
		output, evidence, err := transport.RunWithPolicy(restricted, bundleName+"/"+dimension, prompt, provider.ReviewToolPolicy())
		return output, review.ReviewEvidence{RunID: evidence.RunID, InvocationID: evidence.InvocationID}, err
	}
	var mu sync.Mutex
	var finalized []struct {
		runID, invocation, class string
	}
	finalize := func(runID, invocationID, class, detail string) error {
		var failures []store.ExecutionFailure
		if class != "" {
			failures = []store.ExecutionFailure{{InvocationID: invocationID, Class: store.FailureClass(class), Detail: detail}}
		}
		_, err := transport.FinalizeMetricsForDisposition(context.Background(), runID, failures)
		if err == nil {
			mu.Lock()
			finalized = append(finalized, struct {
				runID, invocation, class string
			}{runID: runID, invocation: invocationID, class: class})
			mu.Unlock()
		}
		return err
	}
	factory := func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		return agent, "normal", nil
	}
	result := review.AuditCommit(factory, 1, review.AuditOptions{
		SHA: "sha-retry",
		Bundles: []review.ReviewBundle{{
			Name: "quality", Dimensions: []string{review.DimLogic}, Priority: review.PriorityRequired, Cost: 1,
		}},
		ReviewTransportWithEvidence: rich,
		FinalizeMetrics:             finalize,
	})
	if result.Verdict != review.VerdictOK {
		t.Fatalf("result = %+v, want successful bounded provider retry", result)
	}
	if agent.calls != 2 {
		t.Fatalf("provider calls = %d, want first failure plus one retry", agent.calls)
	}
	mu.Lock()
	got := append([]struct {
		runID, invocation, class string
	}{}, finalized...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("finalized = %+v, want both physical runs finalized", got)
	}
	if got[0].runID == "" || got[0].invocation == "" || got[0].class != string(agentrun.OutcomeFailure) {
		t.Fatalf("first finalization = %+v, want provider failure disposition", got[0])
	}
	if got[1].runID == got[0].runID || got[1].invocation == got[0].invocation || got[1].class != "" {
		t.Fatalf("second finalization = %+v, want independent clean physical run", got[1])
	}
	for _, final := range got {
		snapshot, err := backing.ReadExecutionMetrics(final.runID)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot == nil {
			t.Fatalf("metrics snapshot for %s is absent", final.runID)
		}
	}
	if result.Dims[0].Result.InvocationID != got[1].invocation {
		t.Fatalf("successful invocation = %q, want second finalized invocation %q", result.Dims[0].Result.InvocationID, got[1].invocation)
	}
}

func TestProviderTerminalErrorsPreserveTypedFailuresAfterFinalization(t *testing.T) {
	tests := []struct {
		name  string
		class agentrun.OutcomeClass
	}{
		{name: "cancellation", class: agentrun.OutcomeCancellation},
		{name: "timeout", class: agentrun.OutcomeTimeout},
		{name: "process", class: agentrun.OutcomeProcessError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := &reviewexec.TerminalError{
				RunID: "run-" + tt.name, InvocationID: "inv-" + tt.name,
				Class: tt.class, Text: tt.name + " evidence",
			}
			transport := func(string, string, string, review.AgentReviewer) (string, review.ReviewEvidence, error) {
				return "", review.ReviewEvidence{RunID: original.RunID, InvocationID: original.InvocationID}, original
			}
			finalized := false
			result := review.AuditCommit(func(review.ReviewBundle, string) (review.AgentReviewer, string, error) {
				return &finalizerAgent{}, "normal", nil
			}, 1, review.AuditOptions{
				SHA: "preserve-" + tt.name,
				Bundles: []review.ReviewBundle{{
					Name: "test", Dimensions: []string{review.DimLogic}, Priority: review.PriorityRequired, Cost: 1,
				}},
				ReviewTransportWithEvidence: transport,
				FinalizeMetrics: func(runID, invocation, class, detail string) error {
					finalized = runID == original.RunID && invocation == original.InvocationID && class == string(tt.class) && detail == original.Text
					return nil
				},
			})
			if !finalized {
				t.Fatalf("finalizer was not given exact %s evidence", tt.name)
			}
			if len(result.Dims) != 1 || result.Dims[0].Error == nil {
				t.Fatalf("result = %+v, want typed terminal error", result)
			}
			var got *reviewexec.TerminalError
			if !errors.As(result.Dims[0].Error, &got) || got != original {
				t.Fatalf("error = %T(%v), want original terminal error %p", result.Dims[0].Error, result.Dims[0].Error, original)
			}
		})
	}
}
