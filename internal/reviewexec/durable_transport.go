package reviewexec

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// TerminalError carries a durable non-success outcome plus its concrete
// provider failure text so callers preserve the evidence verbatim instead of
// degrading it into a generic message.
type TerminalError struct {
	Identity string
	Class    agentrun.OutcomeClass
	Text     string
}

func (e *TerminalError) Error() string { return e.Text }

// Evidence is the durable provenance bound to an admitted completion. Every
// field is copied from the AttemptOutcome record persisted by the execution
// controller, so accepted output always travels with verifiable provenance.
type Evidence struct {
	RunID        string
	JobID        string
	InvocationID string
	LineageID    string
	Class        agentrun.OutcomeClass
	OutputHash   string
}

// AdmissionError rejects a completion whose durable evidence failed
// admission. The raw provider output is discarded with it: a mismatched
// completion never reaches verdict computation.
type AdmissionError struct {
	Identity string
	Reason   string
}

func (e *AdmissionError) Error() string { return "admission: " + e.Reason }

// DurableTransport routes single review invocations through the execution
// controller over a shared durable store, so every call becomes an inspectable
// durable run. Output parsing and semantic verdicts stay on the caller side;
// this layer only admits, waits, and translates terminal evidence.
//
// One ephemeral controller per invocation keeps adapter binding simple: runs
// are independent by design and the controller owns no cross-run state beyond
// its in-memory handle map. Retries and chain fallbacks are later slices;
// today each Run call is exactly one physical invocation.
type DurableTransport struct {
	backing *store.Store
	policy  store.RunPolicy
	sha     string
	paths   []string
}

// NewDurableTransport binds the shared store, admission policy, and audited
// commit context used by every routed invocation.
func NewDurableTransport(backing *store.Store, policy store.RunPolicy, sha string, paths []string) *DurableTransport {
	return &DurableTransport{backing: backing, policy: policy, sha: sha, paths: paths}
}

// candidateSalt is process-random (pid plus crypto entropy) so two processes
// auditing the same commit never derive identical candidates, even within one
// wall-clock tick. JD-B1: a wall-clock-only salt collided across processes
// and degraded a healthy dimension to unavailable.
var candidateSalt = fmt.Sprintf("%d-%x", os.Getpid(), mustRandomBytes())

func mustRandomBytes() []byte {
	var rnd [4]byte
	if _, err := crand.Read(rnd[:]); err != nil {
		// Entropy failure is effectively impossible on supported platforms;
		// the pid component alone still separates concurrent processes.
		return nil
	}
	return rnd[:]
}

// invocationSequence guarantees intra-process uniqueness even when several
// transports fire within the same nanosecond.
var invocationSequence atomic.Uint64

// Run executes exactly one physical invocation of reviewer for the identity
// key (bundle and dimension). The candidate combines the key, the audited
// SHA, a process-random salt, and a monotonic sequence so repeated audits —
// in this process or any other — can never collide on candidate identity.
//
// A succeeded completion is admitted only after its durable evidence passes
// verification: an AttemptOutcome must exist for the completing invocation,
// record success, and bind the returned output through OutputHash. Any
// divergence discards the raw output and returns an AdmissionError instead.
func (t *DurableTransport) Run(reviewer RestrictedReviewer, identityKey, prompt string) (string, Evidence, error) {
	if t.backing == nil {
		return "", Evidence{}, errors.New("reviewexec: durable transport requires a store")
	}
	sequence := invocationSequence.Add(1)
	candidate := agentrun.Candidate(fmt.Sprintf("review:%s:%s:%s:%06d", identityKey, t.sha, candidateSalt, sequence))
	request := agentrun.NewRunRequest(candidate, agentrun.Prompt(prompt), nil)
	adapter := NewReviewAdapter(reviewer, t.sha, t.paths, nil)
	controller := execution.NewController(t.backing, adapter)

	handle, err := controller.Start(context.Background(), request, t.policy)
	if err != nil {
		return "", Evidence{}, fmt.Errorf("review run %s not admitted: %w", identityKey, err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		return "", Evidence{}, fmt.Errorf("review run %s observation failed: %w", identityKey, err)
	}
	if completion.State == agentrun.StateSucceeded {
		evidence, evidenceErr := t.verifyEvidence(identityKey, handle.RunID, string(completion.InvocationID), completion.Output)
		if evidenceErr != nil {
			return "", Evidence{}, evidenceErr
		}
		return completion.Output, evidence, nil
	}
	text := strings.TrimSpace(completion.Error)
	if text == "" {
		text = fmt.Sprintf("review run %s ended %s/%s without evidence", identityKey, completion.State, completion.Outcome)
	}
	return "", Evidence{}, &TerminalError{Identity: identityKey, Class: completion.Outcome, Text: text}
}

// verifyEvidence admits a returned output only when the durable attempt
// outcome of that exact invocation records success and binds the output
// through the shared hashing authority. The three refusal branches — missing
// outcome, class divergence, and hash mismatch — are admission failures, so
// each returns an AdmissionError naming the failed check. Unreadable durable
// evidence refuses too, because absence of proof is never silent acceptance,
// but as a wrapped infrastructure error: a store outage must not wear the
// admission label that slice 2 surfaces as evidence-class reasons.
func (t *DurableTransport) verifyEvidence(identityKey string, runID agentrun.Identity, invocationID, output string) (Evidence, error) {
	outcomes, err := t.backing.ReadAttemptOutcomes(string(runID))
	if err != nil {
		return Evidence{}, fmt.Errorf("reviewexec: attempt outcomes for run %s could not be read: %w", runID, err)
	}
	var outcome *store.AttemptOutcome
	for i := range outcomes {
		if outcomes[i].InvocationID == invocationID {
			outcome = &outcomes[i]
			break
		}
	}
	if outcome == nil {
		return Evidence{}, &AdmissionError{
			Identity: identityKey,
			Reason:   fmt.Sprintf("no durable attempt outcome for invocation %s", invocationID),
		}
	}
	if outcome.Class != agentrun.OutcomeSuccess {
		return Evidence{}, &AdmissionError{
			Identity: identityKey,
			Reason:   fmt.Sprintf("attempt outcome class %q does not record success for invocation %s", outcome.Class, invocationID),
		}
	}
	outputHash := execution.HashAdapterOutput(output)
	if outputHash != outcome.OutputHash {
		return Evidence{}, &AdmissionError{
			Identity: identityKey,
			Reason:   fmt.Sprintf("output hash mismatch for invocation %s: returned %q, durable %q", invocationID, outputHash, outcome.OutputHash),
		}
	}
	return evidenceFromOutcome(outcome), nil
}

// evidenceFromOutcome copies the verified durable record into the transport's
// public evidence shape in one place, so new fields cannot drift per call site.
func evidenceFromOutcome(outcome *store.AttemptOutcome) Evidence {
	return Evidence{
		RunID:        outcome.RunID,
		JobID:        outcome.JobID,
		InvocationID: outcome.InvocationID,
		LineageID:    outcome.LineageID,
		Class:        outcome.Class,
		OutputHash:   outcome.OutputHash,
	}
}
