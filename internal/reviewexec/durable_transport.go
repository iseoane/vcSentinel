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
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
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

// ProviderSettledRetryable declara que este error representa un run que ASENTÓ
// durablemente, no un fallo de admisión ni una incertidumbre posterior al
// envío. Solo por eso el motor puede repetir la dimensión sin arriesgarse a
// duplicar una invocación: el intento anterior terminó y quedó registrado.
//
// Un timeout no entra: repetirlo costaría otro plazo completo. Una cancelación
// tampoco: el llamante pidió parar. El éxito no es un fallo.
func (e *TerminalError) ProviderSettledRetryable() bool {
	switch e.Class {
	case agentrun.OutcomeFailure, agentrun.OutcomeUnavailable:
		return true
	default:
		return false
	}
}

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

// AdmissionReasonPrefix is the literal prefix every admission failure carries
// in its public text (ticket 07): failures are first-class evidence with
// provenance, never generic unavailability. Surfacing layers that only see
// persisted reason strings classify through this single source of truth.
const AdmissionReasonPrefix = "admission: "

// AdmissionError rejects a completion whose durable evidence failed
// admission. The raw provider output is discarded with it: a mismatched
// completion never reaches verdict computation.
type AdmissionError struct {
	Identity string
	Reason   string
}

func (e *AdmissionError) Error() string { return AdmissionReasonPrefix + e.Reason }

// IsAdmissionError reports whether err is (or wraps) an AdmissionError, so
// callers holding the typed error can distinguish admission failures from
// infrastructure failures without parsing text.
func IsAdmissionError(err error) bool {
	var admission *AdmissionError
	return errors.As(err, &admission)
}

// IsAdmissionReason reports whether a persisted unavailable-reason string was
// produced by an admission failure. It is the string-level counterpart of
// IsAdmissionError for surfaces that aggregate reasons after the typed error
// is gone (ledger revisions, rendered summaries).
func IsAdmissionReason(reason string) bool {
	return strings.HasPrefix(reason, AdmissionReasonPrefix)
}

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
	// admissionEnabled gates evidence admission (ticket 07): when false, Run
	// restores the pre-R6 observe-but-admit lenient behavior. The constructor
	// always initializes it to true, so accidental non-wiring stays strict;
	// the rollback seam is an explicit WithEvidenceAdmission(false).
	admissionEnabled bool
	// escalationPolicy configures bounded cancellation escalation (ticket 08
	// slice 2). The zero value is enabled with default budgets; slice 3 wires
	// review.cancellation_escalation into it through
	// WithCancellationEscalation at construction time.
	escalationPolicy execution.EscalationPolicy
	// observedRun optionally receives the durable run identity of every run
	// this transport admits, synchronously after Start succeeds and before
	// any completion can exist (ticket 11 slice 3). Review candidate
	// identities are process-salted, so an orchestrator that parents review
	// runs under its own root learns the ACTUAL child identities here instead
	// of deriving them from the plan; failures never lose the identity because
	// it is reported at admission time, not at settlement time.
	observedRun func(runID string)
}

// DurableTransportOption configures construction-time behavior of a
// DurableTransport. Options are the rollback seam of ticket 07: flags only,
// no behavioral forks anywhere else.
type DurableTransportOption func(*DurableTransport)

// WithEvidenceAdmission sets whether Run verifies snapshot binding and durable
// output evidence before admitting a completion. Enabled by default; passing
// false restores the pre-R6 lenient mode where output is returned with zero
// Evidence exactly as before admission existed, while every run stays
// inspectable through `sentinel runs`.
func WithEvidenceAdmission(enabled bool) DurableTransportOption {
	return func(t *DurableTransport) { t.admissionEnabled = enabled }
}

// NewDurableTransport binds the shared store, admission policy, and audited
// commit context used by every routed invocation. Admission is strict unless
// an option explicitly relaxes it.
func NewDurableTransport(backing *store.Store, policy store.RunPolicy, sha string, paths []string, opts ...DurableTransportOption) *DurableTransport {
	t := &DurableTransport{backing: backing, policy: policy, sha: sha, paths: paths, admissionEnabled: true}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t
}

// WithCancellationEscalation overrides the bounded escalation policy used by
// every controller this transport builds. Zero fields keep their defaults.
// Disabled=true restricts every kill to the DIRECT CHILD through the exec
// kill switch: neither controller escalation nor the containment watchdog
// ever signals the whole tree, and aborted runs settle promptly as canceled
// with an explicit descendant-accounting caveat (never a reaped or orphaned
// claim) instead of escalation evidence.
func WithCancellationEscalation(policy execution.EscalationPolicy) DurableTransportOption {
	return func(t *DurableTransport) { t.escalationPolicy = policy }
}

// WithRunObserver registers the callback invoked synchronously after each
// successful Start with the admitted durable run identity (ticket 11 slice
// 3). It is a wiring-time learning seam, not a telemetry hook: the callback
// runs before any completion can exist, so every admitted run is reported
// exactly once regardless of how its invocation later settles. Concurrency
// safety belongs to the callback owner — parallel dimensions admit runs from
// different goroutines.
func WithRunObserver(observer func(runID string)) DurableTransportOption {
	return func(t *DurableTransport) { t.observedRun = observer }
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
// The WithRunObserver callback fires immediately after successful durable
// admission, synchronously before any wait on this goroutine. The provider
// worker is launched inside Start independently of the callback, so observer
// I/O can never prevent or delay provider execution.
//
// Immediately after a successful Start, and only in strict admission mode,
// the admitted snapshot is bound against the live audit context
// (validateSnapshotBinding), failing fast before any provider answer can be
// waited on. A succeeded completion is then admitted only after its durable
// evidence passes verification: an AttemptOutcome must exist for the
// completing invocation, record success, and bind the returned output through
// OutputHash. Any divergence discards the raw output and returns an
// AdmissionError instead.
//
// With WithEvidenceAdmission(false) both verifications are skipped entirely:
// Run restores the pre-R6 observe-but-admit lenient behavior and returns the
// output with zero Evidence, byte-compatible with the transport as it existed
// before ticket 07. Every run stays inspectable through `sentinel runs`.
func (t *DurableTransport) Run(reviewer RestrictedReviewer, identityKey, prompt string) (string, Evidence, error) {
	return t.run(reviewer, identityKey, prompt, nil)
}

// RunWithPolicy executes an admitted production semantic review with the
// exact policy resolved for its dimension. Unlike Run, it has no default
// policy compatibility path.
func (t *DurableTransport) RunWithPolicy(reviewer PolicyRestrictedReviewer, identityKey, prompt string, policy reviewcontract.ToolPolicy) (string, Evidence, error) {
	return t.run(reviewer, identityKey, prompt, &policy)
}

func (t *DurableTransport) run(reviewer any, identityKey, prompt string, policy *reviewcontract.ToolPolicy) (string, Evidence, error) {
	if t.backing == nil {
		return "", Evidence{}, errors.New("reviewexec: durable transport requires a store")
	}
	sequence := invocationSequence.Add(1)
	candidate := agentrun.Candidate(fmt.Sprintf("review:%s:%s:%s:%06d", identityKey, t.sha, candidateSalt, sequence))
	request := agentrun.NewRunRequest(candidate, agentrun.Prompt(prompt), nil)
	var adapter *ReviewAdapter
	if policy == nil {
		legacy, ok := reviewer.(RestrictedReviewer)
		if !ok {
			return "", Evidence{}, errors.New("reviewexec: restricted reviewer is required")
		}
		adapter = NewReviewAdapter(legacy, t.sha, t.paths, nil)
	} else {
		policyReviewer, ok := reviewer.(PolicyRestrictedReviewer)
		if !ok {
			return "", Evidence{}, errors.New("reviewexec: policy-aware reviewer is required")
		}
		adapter = NewReviewAdapterWithPolicy(policyReviewer, t.sha, t.paths, *policy, nil)
	}
	controller := execution.NewControllerWithClockAndEscalation(t.backing, adapter, nil, t.escalationPolicy)

	handle, err := controller.Start(context.Background(), request, t.policy)
	if err != nil {
		return "", Evidence{}, fmt.Errorf("review run %s not admitted: %w", identityKey, err)
	}
	// Observation happens immediately after successful durable admission,
	// synchronously on this goroutine and before any wait: the run is already
	// durably admitted (Start launched its detached worker), so a slow or
	// blocking observer can never prevent the provider from executing.
	// Enrich the stored operation label with the per-dimension identity
	// now that it is known. Admission-time policy is generic "review", but
	// the TUI and `sentinel runs` surfaces want "review logic" etc. This
	// best-effort update keeps the persisted bytes compatible (additive).
	if t.policy.Operation == "review" && identityKey != "" {
		if dim := identityKey[strings.LastIndex(identityKey, "/")+1:]; dim != "" && dim != "review" {
			if storeErr := t.backing.UpdateRunOperation(string(handle.RunID), "review "+dim); storeErr != nil {
				fmt.Fprintf(os.Stderr, "reviewexec: could not enrich operation label for run %s: %v\n", handle.RunID, storeErr)
			}
		}
	}
	if t.observedRun != nil {
		t.observedRun(string(handle.RunID))
	}
	if t.admissionEnabled {
		if err := t.validateSnapshotBinding(identityKey, handle.RunID, prompt, candidate); err != nil {
			// A binding rejection must not leave the admitted run executing a
			// provider call whose output can never be trusted. Abort cooperatively
			// so the durable record settles canceled with the rejection on record;
			// the original admission error stays the caller-facing result.
			if _, abortErr := controller.Apply(context.Background(), handle.RunID,
				execution.ControlAction{Kind: execution.ActionAbort}); abortErr != nil {
				fmt.Fprintf(os.Stderr, "reviewexec: abort after rejected binding for run %s failed: %v\n", handle.RunID, abortErr)
			}
			return "", Evidence{}, err
		}
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		return "", Evidence{}, fmt.Errorf("review run %s observation failed: %w", identityKey, err)
	}
	if completion.State == agentrun.StateSucceeded {
		if !t.admissionEnabled {
			// Lenient mode (review.evidence_admission=false): pre-R6 behavior —
			// output admitted unverified, zero Evidence, nothing else changes.
			return completion.Output, Evidence{}, nil
		}
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

// validateSnapshotBinding validates, fail-fast before waiting, that the just-admitted
// durable request still binds the live audit context. Three identities must
// hold: the readable admitted candidate carries exactly one segment equal to
// the transport-bound SHA (snapshot freshness — the audited SHA embedded at
// admission is the SHA this transport audits for), the durable record stores
// exactly that candidate identity, and the live prompt reproduces the
// recorded prompt identity through the same agentrun derivation the store
// used at admission. Any divergence returns an AdmissionError naming the
// diverging identity verbatim; read failures from the durable record are
// wrapped infrastructure errors, because a store outage must not wear the
// admission label.
//
// The record stores candidate and prompt IDENTITIES (hex hashes, never raw
// strings — durable request records carry no non-identity content), so the
// colon-segment freshness rule applies to the readable candidate this
// transport admitted, and storage coherence is proven by identity comparison
// rather than by parsing stored bytes.
func (t *DurableTransport) validateSnapshotBinding(identityKey string, runID agentrun.Identity, prompt string, admitted agentrun.Candidate) error {
	stored, err := t.backing.ReadExecutionRequest(string(runID))
	if err != nil {
		return fmt.Errorf("reviewexec: admitted request for run %s could not be read: %w", runID, err)
	}
	if stored.RunID != string(runID) {
		return &AdmissionError{
			Identity: identityKey,
			Reason:   fmt.Sprintf("request lineage %q does not belong to run %q", stored.RunID, string(runID)),
		}
	}
	if t.sha == "" {
		return &AdmissionError{Identity: identityKey, Reason: "transport-bound audited sha is empty"}
	}
	shaSegments := 0
	for _, segment := range strings.Split(string(admitted), ":") {
		if segment == t.sha {
			shaSegments++
		}
	}
	if shaSegments != 1 {
		return &AdmissionError{
			Identity: identityKey,
			Reason:   fmt.Sprintf("candidate %q does not carry exactly one segment matching audited sha %q", string(admitted), t.sha),
		}
	}
	if stored.CandidateID != string(admitted.Identity()) {
		return &AdmissionError{
			Identity: identityKey,
			Reason:   fmt.Sprintf("durable request candidate identity %q does not match admitted candidate %q", stored.CandidateID, admitted.Identity()),
		}
	}
	livePromptIdentity := agentrun.PromptIdentity(agentrun.Prompt(prompt))
	if livePromptIdentity != agentrun.Identity(stored.PromptID) {
		return &AdmissionError{
			Identity: identityKey,
			Reason:   fmt.Sprintf("prompt identity %q does not match admitted request %q", livePromptIdentity, stored.PromptID),
		}
	}
	return nil
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
