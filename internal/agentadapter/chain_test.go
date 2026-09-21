package agentadapter

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
)

// fakeAdapter is an in-memory adapter to test AdapterChain without running
// real binaries. It implements completeAdapter (AgentAdapter + RunPrompt) and
// exposes its name for the chain's error messages.
type fakeAdapter struct {
	name    string
	output  string
	message string
	err     error
	prompts int
}

func (f *fakeAdapter) RunPrompt(prompt string) (string, error) {
	f.prompts++
	if f.err != nil {
		return "", f.err
	}
	return f.output, nil
}

func (f *fakeAdapter) GetCommitMessage(paths []string, layer string, batchNum int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.message, nil
}

func (f *fakeAdapter) String() string { return f.name }

// fakeAdapterWithDiff adds diff capability (AdapterWithDiff) to a base fake.
type fakeAdapterWithDiff struct {
	fakeAdapter
	diffOutput string
	diffErr    error
}

type adapterReviewFake struct {
	fakeAdapter
	prompt string
	sha    string
	paths  []string
	policy reviewcontract.ToolPolicy
}

func (f *adapterReviewFake) ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	f.policy = policy
	return f.RunReview(prompt, sha, paths)
}

func (f *adapterReviewFake) RunReview(prompt, sha string, paths []string) (string, error) {
	f.prompt = prompt
	f.sha = sha
	f.paths = append([]string(nil), paths...)
	return f.output, f.err
}

func (f *fakeAdapterWithDiff) GetCommitMessageWithDiff(paths []string, layer string, batchNum int, diff string) (string, error) {
	if f.diffErr != nil {
		return "", f.diffErr
	}
	return f.diffOutput, nil
}

// fakeAdapterRefactor adds refactor capability (AdapterRefactor) to a base
// fake.
type fakeAdapterRefactor struct {
	fakeAdapter
	planOutput  string
	planErr     error
	applyOutput string
	applyErr    error
}

func (f *fakeAdapterRefactor) ProposeRefactorPlan(filePath string) (string, error) {
	if f.planErr != nil {
		return "", f.planErr
	}
	return f.planOutput, nil
}

func (f *fakeAdapterRefactor) ApplyRefactorPlan(filePath string, plan string) (string, error) {
	if f.applyErr != nil {
		return "", f.applyErr
	}
	return f.applyOutput, nil
}

// TestChainRunPromptWithFallback verifies RunPrompt tries the first child
// and, if it fails, returns the second one's answer.
func TestChainRunPromptWithFallback(t *testing.T) {
	first := &fakeAdapter{name: "first", err: errors.New("no response")}
	second := &fakeAdapter{name: "second", output: "output of the second"}
	chain := &AdapterChain{adapters: []completeAdapter{first, second}}

	output, err := chain.RunPrompt("prompt de prueba")
	if err != nil {
		t.Fatalf("RunPrompt returned an error: %v", err)
	}
	if output != "output of the second" {
		t.Errorf("output = %q, want the second adapter's", output)
	}
}

func TestChainRunReviewRequiresRestrictedCapability(t *testing.T) {
	withoutReview := &fakeAdapter{name: "without-review", output: "must not be executed"}
	chain := &AdapterChain{adapters: []completeAdapter{withoutReview}}

	_, err := chain.RunReview("review", "abc", []string{"a.go"})
	if err == nil {
		t.Fatal("RunReview should fail without restricted capability")
	}
	if !strings.Contains(err.Error(), "semantic review unavailable") {
		t.Errorf("error = %q, want a semantic review unavailable error", err)
	}
	if withoutReview.prompts != 0 {
		t.Errorf("RunPrompt ran %d times, want 0", withoutReview.prompts)
	}
}

func TestChainRunReviewUsesLaterRestrictedAdapter(t *testing.T) {
	withoutReview := &fakeAdapter{name: "without-review", output: "must not be executed"}
	withReview := &adapterReviewFake{fakeAdapter: fakeAdapter{name: "with-review", output: "ok"}}
	chain := &AdapterChain{adapters: []completeAdapter{withoutReview, withReview}}

	output, err := chain.RunReview("review", "abc123", []string{"safe.go"})
	if err != nil || output != "ok" {
		t.Fatalf("RunReview() = %q, %v", output, err)
	}
	if withoutReview.prompts != 0 || withReview.prompt != "review" || withReview.sha != "abc123" || strings.Join(withReview.paths, ",") != "safe.go" {
		t.Fatalf("unrestricted=%d prompt=%q sha=%q paths=%v", withoutReview.prompts, withReview.prompt, withReview.sha, withReview.paths)
	}
}

func TestChainReviewWithPolicyForwardsResolvedPolicy(t *testing.T) {
	adapter := &adapterReviewFake{fakeAdapter: fakeAdapter{name: "policy-aware", output: "ok"}}
	chain := &AdapterChain{adapters: []completeAdapter{adapter}}
	policy := reviewcontract.DefaultToolPolicy()

	output, err := chain.ReviewWithPolicy("review", "abc", []string{"safe.go"}, policy)
	if err != nil || output != "ok" {
		t.Fatalf("ReviewWithPolicy() = %q, %v", output, err)
	}
	if adapter.policy != policy {
		t.Fatalf("policy = %#v, want %#v", adapter.policy, policy)
	}
}

// TestChainGetCommitMessageWithFallback verifies GetCommitMessage applies the
// same chained fallback as RunPrompt.
func TestChainGetCommitMessageWithFallback(t *testing.T) {
	first := &fakeAdapter{name: "first", err: errors.New("no response")}
	second := &fakeAdapter{name: "second", message: "feat: from the second"}
	chain := &AdapterChain{adapters: []completeAdapter{first, second}}

	message, err := chain.GetCommitMessage([]string{"a.go"}, "config", 1)
	if err != nil {
		t.Fatalf("GetCommitMessage returned an error: %v", err)
	}
	if message != "feat: from the second" {
		t.Errorf("message = %q, want the second adapter's", message)
	}
}

// TestChainAllFailNamesEachCandidate verifies the aggregate error names each
// candidate and its errors.
func TestChainAllFailNamesEachCandidate(t *testing.T) {
	first := &fakeAdapter{name: "first", err: errors.New("timeout")}
	second := &fakeAdapter{name: "second", err: errors.New("no connection")}
	chain := &AdapterChain{adapters: []completeAdapter{first, second}}

	_, err := chain.RunPrompt("prompt")
	if err == nil {
		t.Fatal("an aggregate error was expected, none was returned")
	}
	if !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Errorf("the error does not name the candidates: %v", err)
	}
	if !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), "no connection") {
		t.Errorf("the error does not include each candidate's: %v", err)
	}
}

// TestChainGetCommitMessageWithDiff verifies the chain uses the with-diff
// method when the child implements it and falls back to the base method when
// it does not.
func TestChainGetCommitMessageWithDiff(t *testing.T) {
	t.Run("uses the diff-capable child", func(t *testing.T) {
		withDiff := &fakeAdapterWithDiff{
			fakeAdapter: fakeAdapter{name: "with-diff", message: "base", output: "base"},
			diffOutput:  "feat: with diff",
		}
		chain := &AdapterChain{adapters: []completeAdapter{withDiff}}

		message, err := chain.GetCommitMessageWithDiff([]string{"a.go"}, "config", 1, "diff-xyz")
		if err != nil {
			t.Fatalf("GetCommitMessageWithDiff returned an error: %v", err)
		}
		if message != "feat: with diff" {
			t.Errorf("message = %q, want the with-diff method's", message)
		}
	})

	t.Run("without a diff child uses the base method", func(t *testing.T) {
		base := &fakeAdapter{name: "base", message: "feat: base", output: "x"}
		chain := &AdapterChain{adapters: []completeAdapter{base}}

		message, err := chain.GetCommitMessageWithDiff([]string{"a.go"}, "config", 1, "diff-xyz")
		if err != nil {
			t.Fatalf("GetCommitMessageWithDiff returned an error: %v", err)
		}
		if message != "feat: base" {
			t.Errorf("message = %q, want the base method's", message)
		}
	})

	t.Run("the diff child fails and the next one answers", func(t *testing.T) {
		withDiff := &fakeAdapterWithDiff{
			fakeAdapter: fakeAdapter{name: "with-diff", err: errors.New("falls")},
			diffErr:     errors.New("falls with diff"),
		}
		base := &fakeAdapter{name: "base", message: "feat: fallback", output: "x"}
		chain := &AdapterChain{adapters: []completeAdapter{withDiff, base}}

		message, err := chain.GetCommitMessageWithDiff([]string{"a.go"}, "config", 1, "diff")
		if err != nil {
			t.Fatalf("GetCommitMessageWithDiff returned an error: %v", err)
		}
		if message != "feat: fallback" {
			t.Errorf("message = %q, want fallback to the next adapter", message)
		}
	})
}

// TestChainRefactorOnlyCapableChildren verifies the refactor plan is
// delegated only to the children implementing AdapterRefactor, skipping the
// rest.
func TestChainRefactorOnlyCapableChildren(t *testing.T) {
	base := &fakeAdapter{name: "base", err: errors.New("does not implement refactor")}
	withRefactor := &fakeAdapterRefactor{
		fakeAdapter: fakeAdapter{name: "refactor", err: errors.New("does not implement refactor")},
		planOutput:  "division plan",
	}
	chain := &AdapterChain{adapters: []completeAdapter{base, withRefactor}}

	plan, err := chain.ProposeRefactorPlan("massive.go")
	if err != nil {
		t.Fatalf("ProposeRefactorPlan returned an error: %v", err)
	}
	if plan != "division plan" {
		t.Errorf("plan = %q, want the AdapterRefactor child's", plan)
	}
}

// TestChainEmptyList verifies a chain without adapters returns an error on
// any call.
func TestChainEmptyList(t *testing.T) {
	chain := &AdapterChain{}
	if _, err := chain.RunPrompt("prompt"); err == nil {
		t.Error("empty list should return an error on RunPrompt")
	}
	if _, err := chain.GetCommitMessage([]string{}, "config", 1); err == nil {
		t.Error("empty list should return an error on GetCommitMessage")
	}
	if _, err := chain.ProposeRefactorPlan("massive.go"); err == nil {
		t.Error("empty list should return an error on ProposeRefactorPlan")
	}
}
