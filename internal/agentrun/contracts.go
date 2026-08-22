// Package agentrun defines provider-neutral durable execution contracts.
package agentrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"
)

type Identity string

func (i Identity) String() string { return string(i) }

type Candidate string

func (c Candidate) Canonical() string  { return canonical(string(c)) }
func (c Candidate) Identity() Identity { return CandidateIdentity(c) }
func CandidateIdentity(c Candidate) Identity {
	return hashIdentity("candidate", c.Canonical())
}

type Prompt string

func (p Prompt) Canonical() string  { return canonical(string(p)) }
func (p Prompt) Identity() Identity { return PromptIdentity(p) }
func PromptIdentity(p Prompt) Identity {
	return hashIdentity("prompt", p.Canonical())
}

type Capability struct {
	name       string
	attributes map[string]string
}

type capabilityValue struct {
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

func NewCapability(name string, attributes map[string]string) Capability {
	return Capability{name: name, attributes: maps.Clone(attributes)}
}
func (c Capability) Name() string                  { return c.name }
func (c Capability) Attributes() map[string]string { return maps.Clone(c.attributes) }
func (c Capability) Canonical() string {
	return canonical(capabilityValue{c.name, maps.Clone(c.attributes)})
}
func (c Capability) Identity() Identity { return hashIdentity("capability", c.Canonical()) }

type RunRequest struct {
	candidate    Candidate
	prompt       Prompt
	capabilities []Capability
}

func NewRunRequest(candidate Candidate, prompt Prompt, capabilities []Capability) RunRequest {
	return RunRequest{candidate: candidate, prompt: prompt, capabilities: copyCapabilities(capabilities)}
}
func (r RunRequest) Candidate() Candidate       { return r.candidate }
func (r RunRequest) Prompt() Prompt             { return r.prompt }
func (r RunRequest) Capabilities() []Capability { return copyCapabilities(r.capabilities) }
func (r RunRequest) Canonical() string {
	capabilities := make([]Identity, len(r.capabilities))
	for i, capability := range r.capabilities {
		capabilities[i] = capability.Identity()
	}
	return canonical(struct {
		Candidate    Identity   `json:"candidate"`
		Prompt       Identity   `json:"prompt"`
		Capabilities []Identity `json:"capabilities,omitempty"`
	}{CandidateIdentity(r.candidate), PromptIdentity(r.prompt), capabilities})
}
func (r RunRequest) Identity() Identity { return hashIdentity("request", r.Canonical()) }

type LogicalJob struct {
	id      Identity
	runID   Identity
	request RunRequest
}

func NewLogicalJob(request RunRequest) LogicalJob {
	requestID := request.Identity()
	return LogicalJob{hashIdentity("job", requestID), hashIdentity("run", requestID), request}
}

// NewRecoveredLogicalJob recreates the identity portion needed for durable
// control actions. Its request payload is intentionally empty.
func NewRecoveredLogicalJob(runID, jobID Identity) (LogicalJob, error) {
	if runID == "" || jobID == "" {
		return LogicalJob{}, errors.New("agentrun: recovered job identity is incomplete")
	}
	return LogicalJob{id: jobID, runID: runID}, nil
}

func (j LogicalJob) ID() Identity        { return j.id }
func (j LogicalJob) RunID() Identity     { return j.runID }
func (j LogicalJob) Request() RunRequest { return j.request }

// Decision controls execution; semantic review verdicts remain in internal/review.
type Decision string

const (
	DecisionNone     Decision = ""
	DecisionStart    Decision = "start"
	DecisionRetry    Decision = "retry"
	DecisionRespond  Decision = "respond"
	DecisionAbort    Decision = "abort"
	DecisionComplete Decision = "complete"
)

type InvocationEnvelope struct {
	job          LogicalJob
	parentID     Identity
	ancestors    []Identity
	lineageID    Identity
	invocationID Identity
	attempt      uint32
	decision     Decision
}

type lineageValue struct {
	Run       Identity   `json:"run"`
	Job       Identity   `json:"job"`
	Root      Identity   `json:"root"`
	Parent    Identity   `json:"parent,omitempty"`
	Ancestors []Identity `json:"ancestors"`
}

type InvalidLineageError struct{ Reason string }

func (e InvalidLineageError) Error() string {
	return "agentrun: invalid invocation lineage: " + e.Reason
}
func lineageError(reason string) (InvocationEnvelope, error) {
	return InvocationEnvelope{}, InvalidLineageError{Reason: reason}
}

// brokenAncestryRule reports which physical-ancestor rule a candidate chain
// violates, so live and recovered envelope constructors share one validation
// while keeping their own error texts.
type ancestryRule int

const (
	ancestryRootMissing ancestryRule = iota
	ancestryRootHasPhysical
	ancestryParentNotChainEnd
)

func brokenAncestryRule(jobID Identity, parent Identity, ancestors []Identity) (ancestryRule, bool) {
	if len(ancestors) == 0 || ancestors[0] != jobID {
		return ancestryRootMissing, true
	}
	if parent == "" && len(ancestors) != 1 {
		return ancestryRootHasPhysical, true
	}
	if parent != "" && ancestors[len(ancestors)-1] != parent {
		return ancestryParentNotChainEnd, true
	}
	return 0, false
}

func NewInvocationEnvelope(job LogicalJob, parent Identity, ancestors []Identity, attempt uint32, decision Decision) (InvocationEnvelope, error) {
	if job.ID() == "" {
		return lineageError("logical job identity is empty")
	}
	if attempt == 0 {
		return lineageError("attempt must be positive")
	}
	if len(ancestors) == 0 {
		if parent != "" {
			return lineageError("a child invocation requires its parent in the ancestor list")
		}
		ancestors = []Identity{job.ID()}
	}
	if rule, broken := brokenAncestryRule(job.ID(), parent, ancestors); broken {
		switch rule {
		case ancestryRootMissing:
			return lineageError("ancestor root does not identify the logical job")
		case ancestryRootHasPhysical:
			return lineageError("a root invocation cannot have physical ancestors")
		default:
			return lineageError("parent identity must be the last physical ancestor")
		}
	}
	lineage := lineageValue{job.RunID(), job.ID(), ancestors[0], parent, append([]Identity(nil), ancestors...)}
	lineageID := hashIdentity("invocation-lineage", lineage)
	invocationID := hashIdentity("invocation", struct {
		Lineage  Identity `json:"lineage"`
		Attempt  uint32   `json:"attempt"`
		Decision Decision `json:"decision"`
	}{lineageID, attempt, decision})
	return InvocationEnvelope{job, parent, append([]Identity(nil), ancestors...), lineageID, invocationID, attempt, decision}, nil
}

func NewRootInvocation(job LogicalJob, attempt uint32, decision Decision) (InvocationEnvelope, error) {
	return NewInvocationEnvelope(job, "", nil, attempt, decision)
}
func NewChildInvocation(parent InvocationEnvelope, attempt uint32, decision Decision) (InvocationEnvelope, error) {
	ancestors := append(parent.AncestorIDs(), parent.InvocationID())
	return NewInvocationEnvelope(parent.job, parent.invocationID, ancestors, attempt, decision)
}

// NewRetryInvocation derives the next attempt from a terminal invocation so a
// retried execution stays inside the original run, job, and lineage instead of
// becoming a sibling run.
func NewRetryInvocation(parent InvocationEnvelope) (InvocationEnvelope, error) {
	return NewChildInvocation(parent, parent.Attempt()+1, DecisionRetry)
}

// NewRecoveredInvocation restores only the identity needed for a durable
// control action. Its request payload is absent, so the result must never be
// passed to an adapter that needs it; attempt must carry the recovered
// invocation's durable attempt number. ancestors must be the complete
// physical chain derived from durable evidence — the logical job identity
// followed by every prior physical invocation in event order — so a
// continuation built on top of this envelope keeps its original lineage
// identity instead of fabricating a divergent one.
func NewRecoveredInvocation(runID, jobID, invocationID, lineageID, parentID Identity, ancestors []Identity, attempt uint32) (InvocationEnvelope, error) {
	if runID == "" || jobID == "" || invocationID == "" || lineageID == "" || attempt == 0 {
		return InvocationEnvelope{}, errors.New("agentrun: recovered invocation identity is incomplete")
	}
	if rule, broken := brokenAncestryRule(jobID, parentID, ancestors); broken {
		switch rule {
		case ancestryRootMissing:
			return InvocationEnvelope{}, InvalidLineageError{Reason: "recovered ancestor chain does not identify the logical job"}
		case ancestryRootHasPhysical:
			return InvocationEnvelope{}, InvalidLineageError{Reason: "a recovered root invocation cannot have physical ancestors"}
		default:
			return InvocationEnvelope{}, InvalidLineageError{Reason: "recovered parent identity must end the ancestor chain"}
		}
	}
	job := LogicalJob{id: jobID, runID: runID}
	return InvocationEnvelope{
		job: job, parentID: parentID, ancestors: append([]Identity(nil), ancestors...),
		lineageID: lineageID, invocationID: invocationID, attempt: attempt,
	}, nil
}

func (i InvocationEnvelope) RunID() Identity              { return i.job.RunID() }
func (i InvocationEnvelope) JobID() Identity              { return i.job.ID() }
func (i InvocationEnvelope) ParentInvocationID() Identity { return i.parentID }
func (i InvocationEnvelope) RootIdentity() Identity {
	if len(i.ancestors) == 0 {
		return ""
	}
	return i.ancestors[0]
}
func (i InvocationEnvelope) AncestorIDs() []Identity {
	return append([]Identity(nil), i.ancestors...)
}
func (i InvocationEnvelope) LineageCanonical() string  { return canonical(i.lineageValue()) }
func (i InvocationEnvelope) LineageIdentity() Identity { return i.lineageID }
func (i InvocationEnvelope) InvocationID() Identity    { return i.invocationID }
func (i InvocationEnvelope) Attempt() uint32           { return i.attempt }
func (i InvocationEnvelope) Decision() Decision        { return i.decision }
func (i InvocationEnvelope) BelongsTo(job LogicalJob) bool {
	return i.RunID() == job.RunID() && i.JobID() == job.ID() && i.RootIdentity() == job.ID()
}
func (i InvocationEnvelope) lineageValue() lineageValue {
	return lineageValue{i.RunID(), i.JobID(), i.RootIdentity(), i.parentID, i.AncestorIDs()}
}

type LifecycleState string

const (
	StateCreated          LifecycleState = "created"
	StateQueued           LifecycleState = "queued"
	StateAdmitted         LifecycleState = "admitted"
	StateRunning          LifecycleState = "running"
	StateAwaitingDecision LifecycleState = "awaiting_decision"
	// StateTerminating marks the bounded escalation window of ticket 08
	// slice 2: the controller requested whole-tree termination after the
	// cooperative grace budget expired. It is transient evidence between the
	// running head and the canceled settlement and never persists beyond the
	// same controller goroutine that authored it.
	StateTerminating LifecycleState = "terminating"
	// StateTerminated records confirmed reaping of an escalated process tree.
	// Like terminating it is transient, non-retryable, and non-terminal: the
	// canceled settlement remains the authoritative terminal frame.
	StateTerminated  LifecycleState = "terminated"
	StateSucceeded   LifecycleState = "succeeded"
	StateFailed      LifecycleState = "failed"
	StateCanceled    LifecycleState = "canceled"
	StateTimedOut    LifecycleState = "timed_out"
	StateUnavailable LifecycleState = "unavailable"
)

var validTransitions = map[LifecycleState]map[LifecycleState]bool{
	StateCreated:          {StateQueued: true, StateCanceled: true},
	StateQueued:           {StateAdmitted: true, StateCanceled: true},
	StateAdmitted:         {StateRunning: true, StateCanceled: true},
	StateRunning:          {StateAwaitingDecision: true, StateSucceeded: true, StateFailed: true, StateCanceled: true, StateTimedOut: true, StateUnavailable: true, StateTerminating: true},
	StateAwaitingDecision: {StateRunning: true, StateSucceeded: true, StateFailed: true, StateCanceled: true, StateTimedOut: true, StateUnavailable: true},
	StateTerminating:      {StateTerminated: true, StateCanceled: true},
	StateTerminated:       {StateCanceled: true},
	StateFailed:           {StateRunning: true},
	StateCanceled:         {StateRunning: true},
	StateTimedOut:         {StateRunning: true},
}

// retryableStates lists the terminal outcomes an explicit retry decision may
// relaunch. Success and unavailable evidence stay final.
var retryableStates = map[LifecycleState]bool{
	StateFailed: true, StateCanceled: true, StateTimedOut: true,
}

// Retryable reports whether this terminal lifecycle state may relaunch
// execution through an explicit retry decision.
func (s LifecycleState) Retryable() bool { return retryableStates[s] }

func canRetryRelaunch(from, to LifecycleState) bool {
	return from.Retryable() && to == StateRunning
}

type InvalidTransitionError struct{ From, To LifecycleState }

func (e InvalidTransitionError) Error() string {
	return "agentrun: invalid lifecycle transition " + string(e.From) + " -> " + string(e.To)
}
func (s LifecycleState) CanTransition(to LifecycleState) bool { return validTransitions[s][to] }
func Transition(from, to LifecycleState) error {
	if !from.CanTransition(to) {
		return InvalidTransitionError{from, to}
	}
	return nil
}

type InvalidDecisionError struct {
	From     LifecycleState
	To       LifecycleState
	Decision Decision
}

func (e InvalidDecisionError) Error() string {
	return fmt.Sprintf("agentrun: decision %q cannot drive %s -> %s", string(e.Decision), e.From, e.To)
}

type TerminalClass string

const (
	TerminalNone         TerminalClass = ""
	TerminalSuccess      TerminalClass = "success"
	TerminalFailure      TerminalClass = "failure"
	TerminalCancellation TerminalClass = "cancellation"
	TerminalTimeout      TerminalClass = "timeout"
	TerminalUnavailable  TerminalClass = "unavailable"
)

var terminalClasses = map[LifecycleState]TerminalClass{
	StateSucceeded: TerminalSuccess, StateFailed: TerminalFailure,
	StateCanceled: TerminalCancellation, StateTimedOut: TerminalTimeout,
	StateUnavailable: TerminalUnavailable,
}

func (s LifecycleState) TerminalClass() TerminalClass { return terminalClasses[s] }

// OutcomeClass describes the adapter observation that the controller admitted
// for one physical invocation. It is operational evidence, not a semantic
// review verdict.
type OutcomeClass string

const (
	OutcomeSuccess          OutcomeClass = "success"
	OutcomeFailure          OutcomeClass = "failure"
	OutcomeUnavailable      OutcomeClass = "unavailable"
	OutcomeTimeout          OutcomeClass = "timeout"
	OutcomeCancellation     OutcomeClass = "cancellation"
	OutcomeProcessError     OutcomeClass = "process_error"
	OutcomeAwaitingDecision OutcomeClass = "awaiting_decision"
)

func (c OutcomeClass) IsTerminal() bool {
	switch c {
	case OutcomeSuccess, OutcomeFailure, OutcomeUnavailable, OutcomeTimeout,
		OutcomeCancellation, OutcomeProcessError:
		return true
	default:
		return false
	}
}

type NormalizedEvent struct {
	at                 time.Time
	runID              Identity
	jobID              Identity
	invocationID       Identity
	lineageID          Identity
	parentInvocationID Identity
	responseHash       string
	from, to           LifecycleState
	decision           Decision
	terminal           TerminalClass
}

func NewNormalizedEvent(invocation InvocationEnvelope, from, to LifecycleState, decision Decision, at time.Time) (NormalizedEvent, error) {
	if err := Transition(from, to); err != nil {
		return NormalizedEvent{}, err
	}
	if canRetryRelaunch(from, to) != (decision == DecisionRetry) {
		return NormalizedEvent{}, InvalidDecisionError{From: from, To: to, Decision: decision}
	}
	return NormalizedEvent{
		at:                 at.UTC(),
		runID:              invocation.RunID(),
		jobID:              invocation.JobID(),
		invocationID:       invocation.InvocationID(),
		lineageID:          invocation.LineageIdentity(),
		parentInvocationID: invocation.ParentInvocationID(),
		from:               from,
		to:                 to,
		decision:           decision,
		terminal:           to.TerminalClass(),
	}, nil
}

// NewResponseEvent creates the durable continuation event for an explicit
// response. The response hash travels with the event so control admission and
// lifecycle transition share one hash-linked append.
func NewResponseEvent(invocation InvocationEnvelope, from, to LifecycleState, responseHash string, at time.Time) (NormalizedEvent, error) {
	event, err := NewNormalizedEvent(invocation, from, to, DecisionRespond, at)
	if err != nil {
		return NormalizedEvent{}, err
	}
	event.responseHash = responseHash
	return event, nil
}
func (e NormalizedEvent) At() time.Time                { return e.at }
func (e NormalizedEvent) RunID() Identity              { return e.runID }
func (e NormalizedEvent) JobID() Identity              { return e.jobID }
func (e NormalizedEvent) InvocationID() Identity       { return e.invocationID }
func (e NormalizedEvent) LineageIdentity() Identity    { return e.lineageID }
func (e NormalizedEvent) ParentInvocationID() Identity { return e.parentInvocationID }
func (e NormalizedEvent) ResponseHash() string         { return e.responseHash }
func (e NormalizedEvent) From() LifecycleState         { return e.from }
func (e NormalizedEvent) To() LifecycleState           { return e.to }
func (e NormalizedEvent) Decision() Decision           { return e.decision }
func (e NormalizedEvent) TerminalClass() TerminalClass { return e.terminal }

func copyCapabilities(values []Capability) []Capability {
	copyOfValues := make([]Capability, len(values))
	for i, value := range values {
		copyOfValues[i] = NewCapability(value.name, value.attributes)
	}
	sort.Slice(copyOfValues, func(i, j int) bool { return copyOfValues[i].Canonical() < copyOfValues[j].Canonical() })
	return copyOfValues
}
func canonical(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
func hashIdentity(domain string, value any) Identity {
	digest := sha256.Sum256([]byte(domain + "\x00" + canonical(value)))
	return Identity(hex.EncodeToString(digest[:]))
}
