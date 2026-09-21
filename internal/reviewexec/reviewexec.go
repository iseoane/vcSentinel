// Package reviewexec adapts restricted review agents to the durable execution
// controller so each review invocation becomes a recorded physical attempt.
package reviewexec

import (
	"context"
	"errors"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/process"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
)

// RestrictedReviewer is the structural contract of the review engine's
// tool-restricted reviewer. It is declared locally so this package never
// imports internal/review: wiring flows from review into this package, not
// back into the engine.
//
// Naming waiver: RunReview intentionally mirrors the legacy Spanish
// identifier because Go structural satisfaction requires byte-identical
// method names with the existing review agents. Do not translate it without
// changing every implementer at once.
type RestrictedReviewer interface {
	RunReview(prompt, sha string, paths []string) (string, error)
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
// RunReview entry point it extends; both stay byte-identical across
// every implementer because Go structural satisfaction requires it.
type ContextualReviewer interface {
	ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error)
}

// ResultContextualReviewer is the rich provider seam. It preserves the
// normalized ACP result when a provider returns an outcome error after
// producing partial output.
type ResultContextualReviewer interface {
	ReviewWithContextResult(ctx context.Context, prompt, sha string, paths []string) (acpadapter.Result, error)
}

// ResultPolicyContextualReviewer is the policy-bound counterpart of the rich
// provider seam.
type ResultPolicyContextualReviewer interface {
	ReviewWithContextAndPolicyResult(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (acpadapter.Result, error)
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
// DefaultClassifier maps an adapter's explicit outcome first, then caller
// cancellation and deadlines, and keeps every other concrete provider failure
// as a plain failure whose text must survive end to end.
func DefaultClassifier(err error) agentrun.OutcomeClass {
	var reported interface{ Outcome() agentrun.OutcomeClass }
	if errors.As(err, &reported) {
		if class := reported.Outcome(); class != "" {
			return class
		}
	}
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

// Execute runs exactly one reviewer call. Success and failure both preserve
// any rich provider result so the controller can durably retain observations.
func (a *ReviewAdapter) Execute(ctx context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	prompt := string(job.Request().Prompt())
	if strings.TrimSpace(response) != "" {
		prompt += "\n\n" + response
	}
	var (
		output string
		result acpadapter.Result
		rich   bool
		err    error
	)
	if a.policy != nil {
		policyReviewer, ok := a.reviewer.(PolicyRestrictedReviewer)
		if !ok {
			failure := errors.New("reviewexec: policy-aware reviewer is required")
			return execution.AdapterResult{}, execution.NewAdapterError(a.classify(failure), failure)
		}
		if contextual, ok := a.reviewer.(ResultPolicyContextualReviewer); ok {
			result, err = contextual.ReviewWithContextAndPolicyResult(ctx, prompt, a.sha, a.paths, *a.policy)
			rich = true
		} else if contextual, ok := a.reviewer.(PolicyContextualReviewer); ok {
			output, err = contextual.ReviewWithContextAndPolicy(ctx, prompt, a.sha, a.paths, *a.policy)
		} else {
			output, err = policyReviewer.ReviewWithPolicy(prompt, a.sha, a.paths, *a.policy)
		}
	} else if contextual, ok := a.reviewer.(ResultContextualReviewer); ok {
		result, err = contextual.ReviewWithContextResult(ctx, prompt, a.sha, a.paths)
		rich = true
	} else if contextual, ok := a.reviewer.(ContextualReviewer); ok {
		output, err = contextual.ReviewWithContext(ctx, prompt, a.sha, a.paths)
	} else {
		legacy := a.reviewer.(RestrictedReviewer)
		output, err = legacy.RunReview(prompt, a.sha, a.paths)
	}
	adapted := execution.AdapterResult{Output: output}
	if rich {
		adapted = execution.AdapterResult{
			Output:      result.Output,
			Observation: observationFromResult(result),
		}
	}
	if err != nil {
		return adapted, execution.NewAdapterError(a.classify(err), err)
	}
	return adapted, nil
}

func observationFromResult(result acpadapter.Result) *execution.AdapterObservation {
	provider := result.AdapterObservation()
	observation := &execution.AdapterObservation{
		Agent:           provider.Agent,
		Model:           provider.Model,
		RequestedModel:  provider.RequestedModel,
		Effort:          provider.Effort,
		RequestedEffort: provider.RequestedEffort,
		StopReason:      provider.StopReason,
		Enforcement:     provider.Enforcement,
		Turns:           cloneInt(provider.Turns),
	}
	if provider.Usage != nil {
		observation.Usage = &execution.AdapterUsage{
			InputTokens:           cloneInt64(provider.Usage.InputTokens),
			OutputTokens:          cloneInt64(provider.Usage.OutputTokens),
			TotalTokens:           cloneInt64(provider.Usage.TotalTokens),
			CachedInputTokens:     cloneInt64(provider.Usage.CachedInputTokens),
			CacheWriteInputTokens: cloneInt64(provider.Usage.CacheWriteInputTokens),
			ReasoningTokens:       cloneInt64(provider.Usage.ReasoningTokens),
		}
	}
	return observation
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// cloneInt deep-copies an observed turn count so the returned observation
// never aliases the provider result's own pointer.
func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// TranscriptMetadata implements execution.TranscriptReporter: it forwards the
// wrapped reviewer's EffectiveAgent report so the durable transcript sidecar
// records who actually answered. The controller queries this only after a
// successful Execute, exactly when the report is honest. StopReason stays
// empty today because the raw provider output carries no stop-reason field
// this layer can observe; it must never be fabricated.
func (a *ReviewAdapter) TranscriptMetadata() execution.TranscriptIdentity {
	reports, ok := a.reviewer.(agentadapter.ReportsEffectiveAgent)
	if !ok {
		return execution.TranscriptIdentity{}
	}
	effective, ok := reports.EffectiveAgent()
	if !ok || effective.Empty() {
		return execution.TranscriptIdentity{}
	}
	return execution.TranscriptIdentity{
		Agent:  effective.Binary,
		Model:  effective.Model,
		Effort: effective.Effort,
	}
}
