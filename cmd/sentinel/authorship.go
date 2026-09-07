package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// multipleAgents is the value recorded when the dimensions of the same review
// were attended by different agents. It is not an author: it is the honest
// record that there is no single one. Picking one of them would repeat the
// H4 defect with another face.
const multipleAgents = "multiple"

// authorshipCollector gathers the agents that actually attended each
// dimension of a review. The audit engine runs the dimensions in parallel,
// hence the mutex.
//
// It lives in cmd/ and not in internal/review on purpose: the audit engine
// only depends on the minimal AuditorAgente interface and does not know about
// agentadapter. Extracting the authorship here keeps the engine decoupled
// from the concrete adapter implementations.
type authorshipCollector struct {
	mu     sync.Mutex
	agents []agentadapter.EffectiveAgent
}

// observedAgent wraps one dimension agent and records the responder only
// AFTER a successful request. Timing matters: in an `active_agent: auto`
// chain, the child that serves the request is known only after it responds,
// so asking earlier would reproduce the incorrect H4 record.
type observedAgent struct {
	review.AgentReviewer
	authorship *authorshipCollector
}

func (a *observedAgent) RunPrompt(prompt string) (string, error) {
	output, err := a.AgentReviewer.RunPrompt(prompt)
	if err == nil {
		a.authorship.register(a.AgentReviewer)
	}
	return output, err
}

// EffectiveAgent forwards the wrapped adapter's effective-responder report so
// per-finding producer stamping survives authorship observation: the engine
// sees this wrapper, not the CLIAdapter that knows who answered.
func (a *observedAgent) EffectiveAgent() (agentadapter.EffectiveAgent, bool) {
	reporter, ok := a.AgentReviewer.(agentadapter.ReportsEffectiveAgent)
	if !ok {
		return agentadapter.EffectiveAgent{}, false
	}
	return reporter.EffectiveAgent()
}

func (a *observedAgent) RunReview(prompt, sha string, paths []string) (string, error) {
	reviewer, ok := a.AgentReviewer.(interface {
		RunReview(string, string, []string) (string, error)
	})
	if !ok {
		return "", errors.New("semantic review unavailable")
	}
	output, err := reviewer.RunReview(prompt, sha, paths)
	if err == nil {
		a.authorship.register(a.AgentReviewer)
	}
	return output, err
}

func (a *observedAgent) ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	reviewer, ok := a.AgentReviewer.(interface {
		ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error)
	})
	if !ok {
		// Same actionable diagnosis as the engine: it names the adapter
		// instead of repeating six times an absent capability with no owner.
		return "", fmt.Errorf("%w: the configured agent %T cannot review under a tool policy; configure a CLI agent (claude or opencode) for review", review.ErrRestrictedRequired, a.AgentReviewer)
	}
	output, err := reviewer.ReviewWithPolicy(prompt, sha, paths, policy)
	if err == nil {
		a.authorship.register(a.AgentReviewer)
	}
	return output, err
}

// ReviewWithContextAndPolicyResult is the rich review seam that carries the
// resolved tool policy. The policy binding prefers this method, so it owns the
// whole descent and must not skip a capability the adapter still has: the rich
// policy-aware form, then the context-aware policy form, and only then the
// context-free one. Every step carries the policy, so ReviewWithPolicy's
// ErrRestrictedRequired refusal still guards an agent that cannot honour it.
func (a *observedAgent) ReviewWithContextAndPolicyResult(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (acpadapter.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if rich, ok := a.AgentReviewer.(interface {
		ReviewWithContextAndPolicyResult(context.Context, string, string, []string, reviewcontract.ToolPolicy) (acpadapter.Result, error)
	}); ok {
		res, err := rich.ReviewWithContextAndPolicyResult(ctx, prompt, sha, paths, policy)
		if err == nil {
			a.authorship.register(a.AgentReviewer)
		}
		return res, err
	}
	if contextual, ok := a.AgentReviewer.(interface {
		ReviewWithContextAndPolicy(context.Context, string, string, []string, reviewcontract.ToolPolicy) (string, error)
	}); ok {
		output, err := contextual.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, policy)
		if err == nil {
			a.authorship.register(a.AgentReviewer)
		}
		return acpadapter.Result{Output: output}, err
	}
	output, err := a.ReviewWithPolicy(prompt, sha, paths, policy)
	return acpadapter.Result{Output: output}, err
}

// ReviewWithContextResult preserves the transactional ACP result, including
// partial output and wire observations on post-spawn errors. It intentionally
// avoids the adapter's process-global effective identity fallback. It carries
// no tool policy, so the policy binding never routes through it.
func (a *observedAgent) ReviewWithContextResult(ctx context.Context, prompt, sha string, paths []string) (acpadapter.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := a.AgentReviewer.(interface {
		ReviewWithContextResult(context.Context, string, string, []string) (acpadapter.Result, error)
	}); ok {
		res, err := contextual.ReviewWithContextResult(ctx, prompt, sha, paths)
		if err == nil {
			a.authorship.register(a.AgentReviewer)
		}
		return res, err
	}
	output, err := a.ReviewWithContext(ctx, prompt, sha, paths)
	return acpadapter.Result{Output: output}, err
}

func (a *observedAgent) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := a.AgentReviewer.(interface {
		ReviewWithContext(context.Context, string, string, []string) (string, error)
	}); ok {
		output, err := contextual.ReviewWithContext(ctx, prompt, sha, paths)
		if err == nil {
			a.authorship.register(a.AgentReviewer)
		}
		return output, err
	}
	reviewer, ok := a.AgentReviewer.(interface {
		RunReview(string, string, []string) (string, error)
	})
	if !ok {
		return "", errors.New("semantic review unavailable")
	}
	output, err := reviewer.RunReview(prompt, sha, paths)
	if err == nil {
		a.authorship.register(a.AgentReviewer)
	}
	return output, err
}

func (a *observedAgent) ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := a.AgentReviewer.(interface {
		ReviewWithContextAndPolicy(context.Context, string, string, []string, reviewcontract.ToolPolicy) (string, error)
	}); ok {
		output, err := contextual.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, policy)
		if err == nil {
			a.authorship.register(a.AgentReviewer)
		}
		return output, err
	}
	return a.ReviewWithPolicy(prompt, sha, paths, policy)
}

func (a *observedAgent) OwnedTree() *process.Tree {
	if provider, ok := a.AgentReviewer.(interface {
		OwnedTree() *process.Tree
	}); ok {
		return provider.OwnedTree()
	}
	return nil
}

// register records the effective agent of an adapter, when it knows how to
// report it.
func (r *authorshipCollector) register(agent any) {
	reporter, ok := agent.(agentadapter.ReportsEffectiveAgent)
	if !ok {
		return
	}
	effective, ok := reporter.EffectiveAgent()
	if !ok || effective.Empty() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = append(r.agents, effective)
}

// consolidate returns the single author of the review: the agent if all
// dimensions agreed, the multipleAgents mark if not, and the empty value
// if nobody responded.
func (r *authorshipCollector) consolidate() agentadapter.EffectiveAgent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.agents) == 0 {
		return agentadapter.EffectiveAgent{}
	}
	first := r.agents[0]
	for _, agent := range r.agents[1:] {
		if agent != first {
			return agentadapter.EffectiveAgent{Binary: multipleAgents}
		}
	}
	return first
}
