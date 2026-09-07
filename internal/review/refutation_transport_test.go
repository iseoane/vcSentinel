package review

import (
	"errors"
	"strings"
	"testing"
)

const (
	refutationSHA       = "abc12345"
	criticalFindingJSON = `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"bug","evidence":"criticalCall()","confidence":"high"}]}`
	validRefutationJSON = `{"refuted":true,"reason":"the committed implementation is safe","sha":"abc12345","file":"a.go","evidence":"criticalCall()","line_start":1,"line_end":1}`
)

// transportCall records one ReviewTransport invocation for assertions.
type transportCall struct {
	bundleName string
	dimension  string
	prompt     string
}

// metricsCall records one MetricsFinalizer invocation for assertions.
type metricsCall struct {
	runID        string
	invocation   string
	failureClass string
	detail       string
}

// snapshotReaderForRefutation serves the immutable snapshot content the valid
// refutation evidence must match byte for byte.
func snapshotReaderForRefutation(t *testing.T) SnapshotReader {
	t.Helper()
	return func(sha, file string) (string, error) {
		if sha != refutationSHA || file != "a.go" {
			t.Fatalf("snapshot read sha=%q file=%q", sha, file)
		}
		return "criticalCall()\n", nil
	}
}

// runTransportScenario audits one logic CRITICAL blocker through a recording
// fake durable transport. The transport answers the dimension review with the
// blocking finding and answers the refutation envelope (bundleName
// "refutation") with respondRefutation, so tests control exactly what the
// admitted refuter call produces. It returns the audit result, every transport
// invocation in order, every metrics finalization the engine recorded, and the
// refuter double so tests can prove no direct legacy call bypassed the
// transport.
func runTransportScenario(t *testing.T, respondRefutation func() (string, string, error)) (AuditResult, []transportCall, []metricsCall, *fakeAgent) {
	t.Helper()
	factory, _ := fixedFactory([]string{criticalFindingJSON})
	refuterFactory, refuter := fixedRefuterFactory([]string{validRefutationJSON})
	var calls []transportCall
	var metrics []metricsCall
	result := AuditCommit(factory, 1, AuditOptions{
		SHA:                 refutationSHA,
		Bundles:             testBundles(DimLogic),
		RefuterFactory:      refuterFactory,
		ReadSnapshotContent: snapshotReaderForRefutation(t),
		ReviewTransportWithEvidence: func(bundleName, dimension, prompt string, _ AgentReviewer) (string, ReviewEvidence, error) {
			calls = append(calls, transportCall{bundleName: bundleName, dimension: dimension, prompt: prompt})
			if bundleName == "refutation" {
				output, invocation, err := respondRefutation()
				return output, ReviewEvidence{RunID: "run-refutation", InvocationID: invocation}, err
			}
			return criticalFindingJSON, ReviewEvidence{RunID: "run-dimension", InvocationID: "inv-dimension"}, nil
		},
		FinalizeMetrics: func(runID, invocation, failureClass, detail string) error {
			metrics = append(metrics, metricsCall{runID: runID, invocation: invocation, failureClass: failureClass, detail: detail})
			return nil
		},
	})
	return result, calls, metrics, refuter
}

// TestRefutationRoutesThroughReviewTransport proves the CRITICAL refuter call
// is admitted through the configured transport on the durable path: the
// transport sees the "refutation" envelope exactly once, the refuter double
// never answers directly, and an accepted refutation downgrades the blocker.
func TestRefutationRoutesThroughReviewTransport(t *testing.T) {
	result, calls, metrics, refuter := runTransportScenario(t, func() (string, string, error) {
		return validRefutationJSON, "inv-refutation-1", nil
	})

	var refutationCalls []transportCall
	for _, call := range calls {
		if call.bundleName == "refutation" {
			refutationCalls = append(refutationCalls, call)
		}
	}
	if len(refutationCalls) != 1 {
		t.Fatalf("refutation transport invocations = %d (%+v), want exactly one", len(refutationCalls), calls)
	}
	if refutationCalls[0].dimension != DimLogic {
		t.Fatalf("refutation dimension = %q, want %q", refutationCalls[0].dimension, DimLogic)
	}
	if !strings.Contains(refutationCalls[0].prompt, "Audited commit SHA (trusted): "+refutationSHA) {
		t.Fatalf("refutation prompt missing trusted SHA: %q", refutationCalls[0].prompt)
	}
	refuter.mu.Lock()
	directCalls := refuter.calls
	refuter.mu.Unlock()
	if directCalls != 0 {
		t.Fatalf("direct refuter calls = %d, want zero when a transport is present", directCalls)
	}
	if len(result.Dims) != 1 || result.Dims[0].Result == nil {
		t.Fatalf("result = %+v, want one dimension result", result)
	}
	dim := result.Dims[0].Result
	finding := dim.Findings[0]
	if dim.Verdict != VerdictWarn || finding.Status != StatusRefuted || finding.RefutationReason == "" {
		t.Fatalf("result=%+v, expected accepted refutation through the transport to downgrade the blocker", result)
	}
	if finding.RefutationActor != RefutationActorRefuter || finding.RefutationEvidence == "" || finding.RefutationRangeHash == "" {
		t.Fatalf("refuted finding = %+v, want the refuter actor, evidence and range hash on the finding", finding)
	}
	// Ticket 13 hardening pool (R10 L1): the admitted refutation is its own
	// durable invocation, recorded through the metrics finalizer exactly as
	// the dimension transports record theirs — one finalization per admitted
	// envelope, the same stamping parity on both.
	if len(metrics) != 2 {
		t.Fatalf("metrics finalizations = %+v, want one for the dimension and one for the refutation", metrics)
	}
	if metrics[0].runID != "run-dimension" || metrics[0].invocation != "inv-dimension" || metrics[0].failureClass != "" {
		t.Fatalf("dimension finalization = %+v, want run-dimension/inv-dimension without failure", metrics[0])
	}
	if metrics[1].runID != "run-refutation" || metrics[1].invocation != "inv-refutation-1" || metrics[1].failureClass != "" {
		t.Fatalf("refutation finalization = %+v, want the transport-returned identity \"inv-refutation-1\" without failure", metrics[1])
	}
}

// TestRefutationTransportRejectionPreservesBlocker proves a transport rejection
// (admission refused or provider unavailable) behaves exactly like any other
// unavailable refuter answer: the CRITICAL finding keeps its blocking status,
// the dimension verdict stays block, and the refused envelope records no
// durable metrics.
func TestRefutationTransportRejectionPreservesBlocker(t *testing.T) {
	result, calls, metrics, refuter := runTransportScenario(t, func() (string, string, error) {
		return "", "", errors.New("durable: admission rejected the refutation envelope")
	})

	refutationAttempts := 0
	for _, call := range calls {
		if call.bundleName == "refutation" {
			refutationAttempts++
		}
	}
	if refutationAttempts != 1 {
		t.Fatalf("refutation transport attempts = %d (%+v), want exactly one", refutationAttempts, calls)
	}
	refuter.mu.Lock()
	directCalls := refuter.calls
	refuter.mu.Unlock()
	if directCalls != 0 {
		t.Fatalf("direct refuter calls = %d, want zero when a transport is present", directCalls)
	}
	if len(result.Dims) != 1 || result.Dims[0].Result == nil {
		t.Fatalf("result = %+v, want one dimension result", result)
	}
	dim := result.Dims[0].Result
	finding := dim.Findings[0]
	// The parser leaves the lifecycle status empty until a disposition
	// applies it, so "preserved" here means: no refutation stamp landed and
	// the blocker still decides the dimension verdict.
	if dim.Verdict != VerdictBlock || finding.Status == StatusRefuted {
		t.Fatalf("result=%+v, expected transport rejection to preserve the blocker", result)
	}
	if finding.RefutationActor != "" || finding.RefutationRangeHash != "" {
		t.Fatalf("refutation provenance = %q/%q, want none on a rejected refutation", finding.RefutationActor, finding.RefutationRangeHash)
	}
	for _, record := range metrics {
		if record.runID == "run-refutation" {
			t.Fatalf("a rejected refutation was recorded as durable metrics: %+v", record)
		}
	}
}

// TestRefutationTransportIdentityRecordsRefutationInvocation pins the additive
// provenance contract: the identity the transport reports for the admitted
// refutation envelope is the one recorded for the downgrade (ticket 13
// hardening pool, R10 L1). Dimension findings are projected to the v1 shape,
// so the durable identity lives in the metrics finalization, not on the
// finding itself.
func TestRefutationTransportIdentityRecordsRefutationInvocation(t *testing.T) {
	result, _, metrics, _ := runTransportScenario(t, func() (string, string, error) {
		return validRefutationJSON, "inv-refutation-1", nil
	})

	if len(result.Dims) != 1 || result.Dims[0].Result == nil {
		t.Fatalf("result = %+v, want one dimension result", result)
	}
	refutationRecords := 0
	for _, record := range metrics {
		if record.runID != "run-refutation" {
			continue
		}
		refutationRecords++
		if record.invocation != "inv-refutation-1" {
			t.Fatalf("refutation finalization invocation = %q, want the transport-returned identity \"inv-refutation-1\"", record.invocation)
		}
		if record.failureClass != "" {
			t.Fatalf("refutation finalization class = %q, want empty (accepted refutation)", record.failureClass)
		}
	}
	if refutationRecords == 0 {
		t.Fatal("no metrics finalization found for the refutation invocation")
	}
}
