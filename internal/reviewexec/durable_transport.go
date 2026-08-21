package reviewexec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

// Run executes exactly one physical invocation of reviewer for the identity
// key (bundle and dimension). A nanosecond salt keeps repeated audits of the
// same commit from colliding on candidate identity: durability of distinct
// runs wins over content-stable reuse here. Success returns raw output; any
// other terminal class returns TerminalError preserving the original text.
func (t *DurableTransport) Run(reviewer RestrictedReviewer, identityKey, prompt string) (string, error) {
	if t.backing == nil {
		return "", errors.New("reviewexec: durable transport requires a store")
	}
	candidate := agentrun.Candidate(fmt.Sprintf("review:%s:%s:%d", identityKey, t.sha, time.Now().UnixNano()))
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
