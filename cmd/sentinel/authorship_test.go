package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

func adapterWith(binary, model, effort string) *agentadapter.CLIAdapter {
	return &agentadapter.CLIAdapter{
		BinaryName: binary,
		Config:     config.AgentConfig{Model: model, ReasoningEffort: effort},
	}
}

// failingAgent implements review.AgentReviewer always returning an error,
// to check that a failed request attributes authorship to nobody.
type failingAgent struct{ *agentadapter.CLIAdapter }

func (failingAgent) RunPrompt(string) (string, error) {
	return "", errors.New("unavailable")
}

type promptOnlyAgent struct {
	prompts int
}

type policyRecordingAgent struct{ policy reviewcontract.ToolPolicy }

func (*policyRecordingAgent) RunPrompt(string) (string, error) { return "ok", nil }

func (a *policyRecordingAgent) ReviewWithPolicy(_ string, _ string, _ []string, policy reviewcontract.ToolPolicy) (string, error) {
	a.policy = policy
	return "ok", nil
}

func (a *promptOnlyAgent) RunPrompt(string) (string, error) {
	a.prompts++
	return "it must not run", nil
}

// TestObservedAgentRegistersOnlyAfterResponse closes the link between the
// adapter and the collector: authorship is recorded AFTER a successful
// response, never before and never after a failure.
func TestObservedAgentRegistersOnlyAfterResponse(t *testing.T) {
	collector := &authorshipCollector{}
	failed := &observedAgent{
		AgentReviewer: failingAgent{adapterWith("claude", "opus", "xhigh")},
		authorship:    collector,
	}
	if _, err := failed.RunPrompt("hello"); err == nil {
		t.Fatal("expected an error from the agent")
	}
	if !collector.consolidate().Empty() {
		t.Error("attributed authorship to an agent that failed")
	}
}

func TestObservedAgentRunReviewRequiresRestrictedCapability(t *testing.T) {
	promptOnly := &promptOnlyAgent{}
	agent := &observedAgent{
		AgentReviewer: promptOnly,
		authorship:    &authorshipCollector{},
	}

	_, err := agent.RunReview("review", "abc", []string{"a.go"})
	if err == nil {
		t.Fatal("RunReview should fail without the restricted capability")
	}
	if err.Error() != "semantic review unavailable" {
		t.Errorf("error = %q, expected %q", err, "semantic review unavailable")
	}
	if promptOnly.prompts != 0 {
		t.Errorf("RunPrompt ran %d times, expected 0", promptOnly.prompts)
	}
}

func TestObservedAgentForwardsSemanticToolPolicy(t *testing.T) {
	inner := &policyRecordingAgent{}
	agent := &observedAgent{AgentReviewer: inner, authorship: &authorshipCollector{}}
	policy := reviewcontract.DefaultToolPolicy()

	if _, err := agent.ReviewWithPolicy("review", "abc", []string{"a.go"}, policy); err != nil {
		t.Fatalf("ReviewWithPolicy() error = %v", err)
	}
	if inner.policy != policy {
		t.Fatalf("policy = %#v, want %#v", inner.policy, policy)
	}
}

// effectiveReporter knows how to say who attended its last request; it is the
// capability the engine needs to stamp each finding's producer.
type effectiveReporter struct {
	identity agentadapter.EffectiveAgent
	reports  bool
}

func (r effectiveReporter) RunPrompt(string) (string, error) { return "ok", nil }

func (r effectiveReporter) EffectiveAgent() (agentadapter.EffectiveAgent, bool) {
	return r.identity, r.reports
}

// TestObservedAgentForwardsTheEffectiveResponder: the wrapper must expose the
// identity of the real child; otherwise the findings' producers stay empty on
// the durable path (defect demonstrated in the live review of 9c68ef3).
func TestObservedAgentForwardsTheEffectiveResponder(t *testing.T) {
	effective := agentadapter.EffectiveAgent{Binary: "opencode", Model: "gpt-5.6-luna", Effort: "max"}
	wrapped := &observedAgent{
		AgentReviewer: effectiveReporter{identity: effective, reports: true},
		authorship:    &authorshipCollector{},
	}
	got, ok := wrapped.EffectiveAgent()
	if !ok || got != effective {
		t.Fatalf("EffectiveAgent() = (%+v, %v), expected (%+v, true)", got, ok, effective)
	}

	mute := &observedAgent{AgentReviewer: &promptOnlyAgent{}, authorship: &authorshipCollector{}}
	if _, ok := mute.EffectiveAgent(); ok {
		t.Fatal("an agent without a report should not declare an identity")
	}
}

// TestCollectorRegistersTheAgentThatAnswered: the H4 case. The record must
// store who answered, not the name of the requested profile.
func TestCollectorRegistersTheAgentThatAnswered(t *testing.T) {
	collector := &authorshipCollector{}
	collector.register(adapterWith("opencode", "sonnet", "high"))

	authorship := collector.consolidate()
	if authorship.Binary != "opencode" {
		t.Errorf("binary = %q, expected opencode", authorship.Binary)
	}
	if authorship.Model != "sonnet" || authorship.Effort != "high" {
		t.Errorf("model/effort = %q/%q, expected sonnet/high", authorship.Model, authorship.Effort)
	}
}

// TestCollectorWithoutAgentsInventsNoAuthor: with nobody who answered, the
// field stays empty. Inventing an author is exactly the H4 defect.
func TestCollectorWithoutAgentsInventsNoAuthor(t *testing.T) {
	collector := &authorshipCollector{}
	if !collector.consolidate().Empty() {
		t.Error("invented an author without any agent answering")
	}
}

// TestCollectorWithDifferentAgentsDoesNotSummarizeAFakeOne: if each dimension
// was attended by a different agent, there is no single author to register.
// Picking one would lie in the same shape as H4.
func TestCollectorWithDifferentAgentsDoesNotSummarizeAFakeOne(t *testing.T) {
	collector := &authorshipCollector{}
	collector.register(adapterWith("claude", "opus", "xhigh"))
	collector.register(adapterWith("opencode", "sonnet", "high"))

	authorship := collector.consolidate()
	if authorship.Binary == "claude" || authorship.Binary == "opencode" {
		t.Errorf("picked an arbitrary author among different agents: %+v", authorship)
	}
	if authorship.Binary != multipleAgents {
		t.Errorf("binary = %q, expected %q", authorship.Binary, multipleAgents)
	}
}

// TestCollectorWithTheSameRepeatedAgentDoesConsolidate: several dimensions
// attended by the same agent do have a single author.
func TestCollectorWithTheSameRepeatedAgentDoesConsolidate(t *testing.T) {
	collector := &authorshipCollector{}
	collector.register(adapterWith("claude", "opus", "xhigh"))
	collector.register(adapterWith("claude", "opus", "xhigh"))

	if authorship := collector.consolidate(); authorship.Binary != "claude" {
		t.Errorf("binary = %q, expected claude", authorship.Binary)
	}
}

// richPolicyAgent offers the rich seam that carries the policy.
type richPolicyAgent struct {
	policy reviewcontract.ToolPolicy
	ctx    context.Context
}

func (*richPolicyAgent) RunPrompt(string) (string, error) { return "ok", nil }

func (a *richPolicyAgent) ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error) {
	return "", errors.New("the context-free path must not be reached")
}

func (a *richPolicyAgent) ReviewWithContextAndPolicyResult(ctx context.Context, _, _ string, _ []string, policy reviewcontract.ToolPolicy) (acpadapter.Result, error) {
	a.ctx, a.policy = ctx, policy
	return acpadapter.Result{Output: "rich"}, nil
}

// contextualPolicyAgent offers only the context-aware policy form, which is
// what most CLI reviewers expose.
type contextualPolicyAgent struct {
	policy reviewcontract.ToolPolicy
	ctx    context.Context
}

func (*contextualPolicyAgent) RunPrompt(string) (string, error) { return "ok", nil }

func (a *contextualPolicyAgent) ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error) {
	return "", errors.New("the context-free path must not be reached while a context-aware one exists")
}

func (a *contextualPolicyAgent) ReviewWithContextAndPolicy(ctx context.Context, _, _ string, _ []string, policy reviewcontract.ToolPolicy) (string, error) {
	a.ctx, a.policy = ctx, policy
	return "contextual", nil
}

// TestObservedAgentRichPolicySeamDescendsWithoutLosingCapabilities pins the
// production descent. The policy binding prefers this seam, so it must not
// skip a capability the adapter still has: dropping to the context-free form
// while a context-aware one exists would discard cancellation and deadlines,
// and reaching a policy-free form would discard the tool restrictions.
func TestObservedAgentRichPolicySeamDescendsWithoutLosingCapabilities(t *testing.T) {
	policy := reviewcontract.ToolPolicy{AllowRead: true, AllowSearch: true, RequireImmutableSnapshot: true}
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")

	t.Run("rich policy-aware adapter wins", func(t *testing.T) {
		inner := &richPolicyAgent{}
		collector := &authorshipCollector{}
		agent := &observedAgent{AgentReviewer: inner, authorship: collector}
		res, err := agent.ReviewWithContextAndPolicyResult(ctx, "p", "sha", []string{"a.go"}, policy)
		if err != nil || res.Output != "rich" {
			t.Fatalf("result = %+v, err = %v; want the rich adapter's result", res, err)
		}
		if inner.policy != policy {
			t.Errorf("policy = %+v, want the resolved policy", inner.policy)
		}
		if inner.ctx == nil || inner.ctx.Value(ctxKey{}) != "caller" {
			t.Error("the caller context did not reach the rich adapter")
		}
	})

	t.Run("context-aware policy adapter is preferred over the context-free form", func(t *testing.T) {
		inner := &contextualPolicyAgent{}
		agent := &observedAgent{AgentReviewer: inner, authorship: &authorshipCollector{}}
		res, err := agent.ReviewWithContextAndPolicyResult(ctx, "p", "sha", []string{"a.go"}, policy)
		if err != nil || res.Output != "contextual" {
			t.Fatalf("result = %+v, err = %v; want the context-aware adapter's output", res, err)
		}
		if inner.policy != policy {
			t.Errorf("policy = %+v, want the resolved policy", inner.policy)
		}
		if inner.ctx == nil || inner.ctx.Value(ctxKey{}) != "caller" {
			t.Error("the caller context did not reach the context-aware adapter")
		}
	})

	t.Run("policy-free adapter still carries the policy", func(t *testing.T) {
		inner := &policyRecordingAgent{}
		agent := &observedAgent{AgentReviewer: inner, authorship: &authorshipCollector{}}
		if _, err := agent.ReviewWithContextAndPolicyResult(ctx, "p", "sha", []string{"a.go"}, policy); err != nil {
			t.Fatalf("error = %v", err)
		}
		if inner.policy != policy {
			t.Errorf("policy = %+v, want the resolved policy", inner.policy)
		}
	})

	t.Run("an adapter that cannot review under a policy is refused", func(t *testing.T) {
		agent := &observedAgent{AgentReviewer: &promptOnlyAgent{}, authorship: &authorshipCollector{}}
		_, err := agent.ReviewWithContextAndPolicyResult(ctx, "p", "sha", []string{"a.go"}, policy)
		if !errors.Is(err, review.ErrRestrictedRequired) {
			t.Fatalf("error = %v, want ErrRestrictedRequired", err)
		}
	})
}
