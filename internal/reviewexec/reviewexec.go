// Package reviewexec adapts restricted review agents to the durable execution
// controller so each review invocation becomes a recorded physical attempt.
package reviewexec

import (
	"context"
	"errors"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
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
// The worker context is accepted but not forwarded: the legacy reviewer
// contract carries no context, so cooperative cancellation surfaces once the
// current provider call returns. Process-tree ownership arrives with R7.
type ReviewAdapter struct {
	reviewer RestrictedReviewer
	sha      string
	paths    []string
	classify Classifier
}

// NewReviewAdapter binds one dimension's reviewer to its audited commit
// context. A nil classifier selects DefaultClassifier.
func NewReviewAdapter(reviewer RestrictedReviewer, sha string, paths []string, classify Classifier) *ReviewAdapter {
	if classify == nil {
		classify = DefaultClassifier
	}
	return &ReviewAdapter{reviewer: reviewer, sha: sha, paths: paths, classify: classify}
}

// Execute runs exactly one reviewer call. Success returns the raw untrusted
// provider output; failure returns an AdapterError carrying the classified
// outcome plus the original provider error, never a generic replacement.
func (a *ReviewAdapter) Execute(_ context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, response string) (execution.AdapterResult, error) {
	prompt := string(job.Request().Prompt())
	if strings.TrimSpace(response) != "" {
		prompt += "\n\n" + response
	}
	output, err := a.reviewer.EjecutarRevision(prompt, a.sha, a.paths)
	if err != nil {
		return execution.AdapterResult{}, execution.NewAdapterError(a.classify(err), err)
	}
	return execution.AdapterResult{Output: output}, nil
}
