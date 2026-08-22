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
func (t *DurableTransport) Run(reviewer RestrictedReviewer, identityKey, prompt string) (string, error) {
	if t.backing == nil {
		return "", errors.New("reviewexec: durable transport requires a store")
	}
	sequence := invocationSequence.Add(1)
	candidate := agentrun.Candidate(fmt.Sprintf("review:%s:%s:%s:%06d", identityKey, t.sha, candidateSalt, sequence))
	request := agentrun.NewRunRequest(candidate, agentrun.Prompt(prompt), nil)
	adapter := NewReviewAdapter(reviewer, t.sha, t.paths, nil)
	controller := execution.NewController(t.backing, adapter)

	handle, err := controller.Start(context.Background(), request, t.policy)
	if err != nil {
		return "", fmt.Errorf("review run %s not admitted: %w", identityKey, err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil {
		return "", fmt.Errorf("review run %s observation failed: %w", identityKey, err)
	}
	if completion.State == agentrun.StateSucceeded {
		return completion.Output, nil
	}
	text := strings.TrimSpace(completion.Error)
	if text == "" {
		text = fmt.Sprintf("review run %s ended %s/%s without evidence", identityKey, completion.State, completion.Outcome)
	}
	return "", &TerminalError{Identity: identityKey, Class: completion.Outcome, Text: text}
}
