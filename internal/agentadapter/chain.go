package agentadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/process"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
)

// completeAdapter is the internal interface AdapterChain demands from its
// children: generate commit messages (AgentAdapter) and answer arbitrary
// prompts (PromptAdapter).
type completeAdapter interface {
	AgentAdapter
	RunPrompt(prompt string) (string, error)
}

// AdapterChain wraps an ordered list of adapters and, on each request, tries
// the first one; if it fails, it tries the next (per-request fallback, never
// cached). It implements AgentAdapter, AdapterWithDiff, AdapterRefactor and
// PromptAdapter.
type AdapterChain struct {
	adapters []completeAdapter
	// registry records which child served the last request, so the audit
	// record can attribute the real author and not the requested profile (H4).
	registry effectiveRegistry
}

// RunPrompt tries each adapter in order and returns the first output without
// error. If all of them fail, it returns an aggregate error with each one's.
func (c *AdapterChain) RunPrompt(prompt string) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		return a.RunPrompt(prompt)
	})
}

// RunReview preserves per-request fallback while retaining tool limits.
func (c *AdapterChain) RunReview(prompt, sha string, paths []string) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		if reviewer, ok := a.(interface {
			RunReview(string, string, []string) (string, error)
		}); ok {
			return reviewer.RunReview(prompt, sha, paths)
		}
		return "", fmt.Errorf("semantic review unavailable: adapter %s does not implement RunReview", adapterName(a))
	})
}

// ReviewWithPolicy preserves fallback without allowing a semantic
// review to discard its resolved dimension policy.
func (c *AdapterChain) ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		if reviewer, ok := a.(interface {
			ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error)
		}); ok {
			return reviewer.ReviewWithPolicy(prompt, sha, paths, policy)
		}
		return "", fmt.Errorf("semantic review unavailable: adapter %s does not implement the policy-aware reviewer", adapterName(a))
	})
}

// ReviewWithContext preserves per-request fallback while forwarding the
// cancellation context: children that accept a context receive it so aborts
// reach their spawned provider processes; children that only implement the
// legacy contract keep answering exactly as before.
func (c *AdapterChain) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		if contextual, ok := a.(interface {
			ReviewWithContext(context.Context, string, string, []string) (string, error)
		}); ok {
			return contextual.ReviewWithContext(ctx, prompt, sha, paths)
		}
		if reviewer, ok := a.(interface {
			RunReview(string, string, []string) (string, error)
		}); ok {
			return reviewer.RunReview(prompt, sha, paths)
		}
		return "", fmt.Errorf("semantic review unavailable: adapter %s does not implement RunReview", adapterName(a))
	})
}

// ReviewWithContextAndPolicy forwards cancellation and the resolved policy
// together; legacy reviewers cannot satisfy a semantic review through a chain.
func (c *AdapterChain) ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		if reviewer, ok := a.(interface {
			ReviewWithContextAndPolicy(context.Context, string, string, []string, reviewcontract.ToolPolicy) (string, error)
		}); ok {
			return reviewer.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, policy)
		}
		if reviewer, ok := a.(interface {
			ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error)
		}); ok {
			return reviewer.ReviewWithPolicy(prompt, sha, paths, policy)
		}
		return "", fmt.Errorf("semantic review unavailable: adapter %s does not implement the policy-aware reviewer", adapterName(a))
	})
}

// GetCommitMessage generates the commit message trying each adapter in order
// until one returns an output without error.
func (c *AdapterChain) GetCommitMessage(paths []string, layer string, batchNum int) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		return a.GetCommitMessage(paths, layer, batchNum)
	})
}

// GetCommitMessageWithDiff generates the commit message using the micro-diff
// when the child implements AdapterWithDiff; otherwise it uses the child's
// base method.
func (c *AdapterChain) GetCommitMessageWithDiff(paths []string, layer string, batchNum int, diff string) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		if withDiff, ok := a.(AdapterWithDiff); ok {
			return withDiff.GetCommitMessageWithDiff(paths, layer, batchNum, diff)
		}
		return a.GetCommitMessage(paths, layer, batchNum)
	})
}

// ProposeRefactorPlan requests the split plan probing only the children that
// implement AdapterRefactor, in order.
func (c *AdapterChain) ProposeRefactorPlan(filePath string) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		if refactor, ok := a.(AdapterRefactor); ok {
			return refactor.ProposeRefactorPlan(filePath)
		}
		return "", fmt.Errorf("adapter %s does not implement AdapterRefactor", adapterName(a))
	})
}

// ApplyRefactorPlan orders a refactor plan to be applied probing only the
// children that implement AdapterRefactor, in order.
func (c *AdapterChain) ApplyRefactorPlan(filePath string, plan string) (string, error) {
	return c.firstSuccessful(func(a completeAdapter) (string, error) {
		if refactor, ok := a.(AdapterRefactor); ok {
			return refactor.ApplyRefactorPlan(filePath, plan)
		}
		return "", fmt.Errorf("adapter %s does not implement AdapterRefactor", adapterName(a))
	})
}

// OwnedTree reports the live owned review tree of whichever chain child is
// currently executing a restricted review. At most one child runs at a time
// (fallback is per request, never concurrent), so the first non-nil child
// tree is the active one.
func (c *AdapterChain) OwnedTree() *process.Tree {
	for _, adapter := range c.adapters {
		if provider, ok := adapter.(interface {
			OwnedTree() *process.Tree
		}); ok {
			if tree := provider.OwnedTree(); tree != nil {
				return tree
			}
		}
	}
	return nil
}

// firstSuccessful walks the adapters in order running attempt on each one;
// it returns the first output without error or an aggregate error if all of
// them fail.
func (c *AdapterChain) firstSuccessful(attempt func(completeAdapter) (string, error)) (string, error) {
	if len(c.adapters) == 0 {
		return "", fmt.Errorf("the adapter chain is empty")
	}
	errs := make([]string, 0, len(c.adapters))
	for _, adapter := range c.adapters {
		output, err := attempt(adapter)
		if err == nil {
			// Only the one that answered is recorded as the author.
			c.registry.record(adapter)
			return output, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", adapterName(adapter), err))
	}
	return "", fmt.Errorf("no agent in the chain responded: %s", strings.Join(errs, "; "))
}

// adapterName returns a readable identifier of an adapter for error
// messages: the binary's base name for CLIAdapter, the String() if the
// adapter defines it and the type as a last resort.
func adapterName(a completeAdapter) string {
	if cli, ok := a.(*CLIAdapter); ok {
		return cli.baseName()
	}
	if s, ok := a.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", a)
}
