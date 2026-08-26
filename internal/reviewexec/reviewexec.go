// Package reviewexec adapts restricted review agents to the durable execution
// controller so each review invocation becomes a recorded physical attempt.
package reviewexec

import (
	"context"
	"errors"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// RestrictedReviewer is the structural contract of the review engine's
// tool-restricted reviewer. It is declared locally so this package never
// imports internal/review: wiring flows from review into this package, not
// back into the engine.
//
// Naming waiver: EjecutarRevision intentionally mirrors the legacy Spanish
// identifier because Go structural satisfaction requires byte-identical
// method names with the existing review agents. Do not translate it without
// changing every implementer at once.
type RestrictedReviewer interface {
	EjecutarRevision(prompt, sha string, paths []string) (string, error)
}

// PolicyRestrictedReviewer is the semantic-review capability. A durable
// production invocation must carry the resolved dimension tool policy instead
// of selecting a fallback policy at the transport boundary.
type PolicyRestrictedReviewer interface {
	ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error)
}

// PolicyProvider exposes the contract policy bound by the review engine for
// one durable invocation.
type PolicyProvider interface {
	ReviewToolPolicy() reviewcontract.ToolPolicy
}

// ContextualReviewer is the optional contract of reviewers that can carry the
// worker cancellation context down to the spawned provider process. It is
// discovered structurally, exactly like the effective-agent observer wrappers,
// so reviewers that only implement RestrictedReviewer keep working unchanged.
//
// Naming waiver: ReviewWithContext intentionally pairs with the legacy
// EjecutarRevision entry point it extends; both stay byte-identical across
// every implementer because Go structural satisfaction requires it.
type ContextualReviewer interface {
	ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error)
}

// PolicyContextualReviewer is the cancellation-aware semantic-review
// capability paired with PolicyRestrictedReviewer.
type PolicyContextualReviewer interface {
	ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error)
}

// Classifier assigns a durable outcome class to an operational failure. It
// classifies infrastructure only; it never decides a semantic review verdict.
type Classifier func(error) agentrun.OutcomeClass

// DefaultClassifier maps caller cancellation and deadlines to their durable
// classes and keeps every other concrete provider failure as a plain failure
// whose text must survive end to end.
func DefaultClassifier(err error) agentrun.OutcomeClass {
	switch {
	case errors.Is(err, context.Canceled):
		return agentrun.OutcomeCancellation
	case errors.Is(err, context.DeadlineExceeded):
		return agentrun.OutcomeTimeout
	default:
		return agentrun.OutcomeFailure
	}
}

// TreeProvider is the structural contract of reviewers that own a live
// provider process tree while executing. It is discovered exactly like the
// other optional reviewer contracts so ownership and bounded escalation reach
// the controller without coupling this package to any adapter type.
type TreeProvider interface {
	OwnedTree() *process.Tree
}

// ReviewAdapter performs exactly one restricted review call per physical
// invocation routed by the controller. The audited commit identity is bound at
// construction; the dimension prompt travels inside the admitted RunRequest.
// An empty invocation response means first attempt; a non-empty response is
// the clarification round's user answer appended to the base prompt until the
// engine-level wiring builds the full follow-up prompt itself (slice 2+).
// Observing wrappers around the reviewer keep working unchanged because calls
// always go through the reviewer's own method, so effective-agent recording
// fires where it always did: right after a successful answer.
//
// The worker context is forwarded whenever the reviewer implements
// ContextualReviewer, so Apply(ActionAbort) and deadline contexts reach the
// spawned provider process immediately (R7 slice 1). Reviewers that only
// implement the legacy contract keep working unchanged: cancellation then
// surfaces once the current provider call returns. Process-tree ownership and
// bounded escalation are live since R7 slice 2: the controller discovers the
// adapter's owned tree through TreeProvider and escalates against it after the
// cooperative grace budget expires.
type ReviewAdapter struct {
	reviewer any
	sha      string
	paths    []string
	classify Classifier
	policy   *reviewcontract.ToolPolicy
}

// NewReviewAdapterWithPolicy constructs the production semantic-review path.
// It has no default policy: callers must provide the contract resolved for the
// dimension being executed.
func NewReviewAdapterWithPolicy(reviewer PolicyRestrictedReviewer, sha string, paths []string, policy reviewcontract.ToolPolicy, classify Classifier) *ReviewAdapter {
	if classify == nil {
		classify = DefaultClassifier
	}
	return &ReviewAdapter{reviewer: reviewer, sha: sha, paths: paths, classify: classify, policy: &policy}
}

// NewReviewAdapter binds one dimension's reviewer to its audited commit
// context. A nil classifier selects DefaultClassifier.
func NewReviewAdapter(reviewer RestrictedReviewer, sha string, paths []string, classify Classifier) *ReviewAdapter {
	if classify == nil {
		classify = DefaultClassifier
	}
	return &ReviewAdapter{reviewer: reviewer, sha: sha, paths: paths, classify: classify}
}

// OwnedTree forwards tree discovery to the wrapped reviewer, mirroring how
// ContextualReviewer is forwarded, so controller-authored escalation sees the
// same live child the adapter spawned.
func (a *ReviewAdapter) OwnedTree() *process.Tree {
	if provider, ok := a.reviewer.(TreeProvider); ok {
		return provider.OwnedTree()
	}
	return nil
}

// Execute runs exactly one reviewer call. Success returns the raw untrusted
// provider output; failure returns an AdapterError carrying the classified
// outcome plus the original provider error, never a generic replacement.
func (a *ReviewAdapter) Execute(ctx context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	prompt := string(job.Request().Prompt())
	if strings.TrimSpace(response) != "" {
		prompt += "\n\n" + response
	}
	var (
		output string
		err    error
	)
	if a.policy != nil {
		policyReviewer, ok := a.reviewer.(PolicyRestrictedReviewer)
		if !ok {
			return execution.AdapterResult{}, execution.NewAdapterError(a.classify(errors.New("reviewexec: policy-aware reviewer is required")), errors.New("reviewexec: policy-aware reviewer is required"))
		}
		if contextual, ok := a.reviewer.(PolicyContextualReviewer); ok {
			output, err = contextual.ReviewWithContextAndPolicy(ctx, prompt, a.sha, a.paths, *a.policy)
		} else {
			output, err = policyReviewer.ReviewWithPolicy(prompt, a.sha, a.paths, *a.policy)
		}
	} else if contextual, ok := a.reviewer.(ContextualReviewer); ok {
		output, err = contextual.ReviewWithContext(ctx, prompt, a.sha, a.paths)
	} else {
		legacy := a.reviewer.(RestrictedReviewer)
		output, err = legacy.EjecutarRevision(prompt, a.sha, a.paths)
	}
	if err != nil {
		return execution.AdapterResult{}, execution.NewAdapterError(a.classify(err), err)
	}
	return execution.AdapterResult{Output: output}, nil
}

// TranscriptMetadata implements execution.TranscriptReporter: it forwards the
// wrapped reviewer's AgenteEfectivo report so the durable transcript sidecar
// records who actually answered. The controller queries this only after a
// successful Execute, exactly when the report is honest. StopReason stays
// empty today because the raw provider output carries no stop-reason field
// this layer can observe; it must never be fabricated.
func (a *ReviewAdapter) TranscriptMetadata() execution.TranscriptIdentity {
	reporta, ok := a.reviewer.(agentadapter.ReportaAgenteEfectivo)
	if !ok {
		return execution.TranscriptIdentity{}
	}
	efectivo, ok := reporta.AgenteEfectivo()
	if !ok || efectivo.Vacio() {
		return execution.TranscriptIdentity{}
	}
	return execution.TranscriptIdentity{
		Agent:  efectivo.Binario,
		Model:  efectivo.Modelo,
		Effort: efectivo.Esfuerzo,
	}
}
