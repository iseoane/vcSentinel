package review_test

// Ticket 07 slice 2b acceptance criterion: reviewed content re-encountered
// under a DIFFERENT commit SHA is admitted through the existing blob identity
// rules without any reviewer/provider call, and every surfaced finding still
// reports its ORIGINAL producing invocation identity from the first review.
//
// Same black-box shape as rebase_test.go (package review_test over a real git
// repository and a real store), so it reuses that file's git helpers instead
// of duplicating them a third time.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

const (
	invocationFindingDescription = "v2 finding with durable provenance"
	originalInvocation           = "inv-first-review-0001"
	secondPassInvocation         = "inv-second-review-should-not-happen"
)

// rebaseV2Response emits one v2 finding (the "id" marker marks it as a v2
// finding) so the persisted record carries Findings whose InvocationID
// binding can be checked after adoption.
var rebaseV2Response = "BEGIN_REVIEW\n" +
	`{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"b.txt","line":1,"severity":"WARNING","description":"` + invocationFindingDescription + `","id":"rebase-inv-1","title":"durable provenance","evidence":"content-b","confidence":"high","location":{"file":"b.txt","line_start":1,"line_end":1},"status":"open"}]}` +
	"\nEND_REVIEW"

// rebaseV2StubAgent implements review.AgentReviewer but must NEVER be
// called: both passes route through a transport, so any direct legacy call is
// exactly the provider invocation the reuse rule forbids.
type rebaseV2StubAgent struct{ calls int }

func (a *rebaseV2StubAgent) RunPrompt(string) (string, error) {
	a.calls++
	return "", errors.New("direct reviewer call forbidden while a transport is configured")
}

func (a *rebaseV2StubAgent) RunReview(prompt, _ string, _ []string) (string, error) {
	return a.RunPrompt(prompt)
}

func (a *rebaseV2StubAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

func rebaseV2StubFactory(a *rebaseV2StubAgent) review.ReviewerFactory {
	return func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		return a, "stub", nil
	}
}

// countedTransportFactory builds a per-commit ReviewTransport factory whose
// transports count their calls and answer under the given invocation id,
// mimicking what the durable transport reports through its verified evidence.
func countedTransportFactory(calls *int, invocation string) func(string, []string) review.ReviewTransport {
	return func(sha string, paths []string) review.ReviewTransport {
		return func(_, _, _ string, _ review.AgentReviewer) (string, string, error) {
			*calls++
			if sha == "" || strings.Contains(sha, " ") {
				return "", "", errors.New("transport bound without an audited sha")
			}
			return rebaseV2Response, invocation, nil
		}
	}
}

// findV2FindingInRecord finds one v2 Finding by exact description across
// every revision of a review record. Dimension findings are projected to the
// v1 shape, so the durable InvocationID binding lives on the record's
// aggregated findings.
func findV2FindingInRecord(record review.Record, description string) *review.Finding {
	for _, rev := range record.Revisions {
		for i := range rev.AggregatedFindings {
			if rev.AggregatedFindings[i].Description == description {
				return &rev.AggregatedFindings[i]
			}
		}
	}
	return nil
}

func TestAnalyzeBranchRebaseReusePreservesInvocationIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real-git-repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		runGitRebase(t, args...)
	}
	if err := os.WriteFile("base.txt", []byte(strings.Repeat("b\n", 5)), 0644); err != nil {
		t.Fatal(err)
	}
	runGitRebase(t, "add", "base.txt")
	runGitRebase(t, "commit", "-m", "feat(base): branch base")
	runGitRebase(t, "checkout", "-b", "feature")

	commitInBranchRebase(t, "a.txt", "content-a\n")
	commitInBranchRebase(t, "b.txt", "content-b\n")
	commitInBranchRebase(t, "c.txt", "content-c\n")

	gitDir := filepath.Join(repo, ".git")
	ledger := review.NewLedger(gitDir)
	st := store.NewStore(gitDir)
	stub := &rebaseV2StubAgent{}

	callsFirstPass := 0
	res, err := review.AnalyzeBranch(ledger, review.BranchOptions{
		Factory:                rebaseV2StubFactory(stub),
		Parallel:               1,
		Store:                  st,
		ReviewTransportFactory: countedTransportFactory(&callsFirstPass, originalInvocation),
	})
	if err != nil {
		t.Fatalf("first pass failed: %v", err)
	}
	if len(res.Pending) != 3 {
		t.Fatalf("Pending = %v, expected 3 new commits", res.Pending)
	}
	if callsFirstPass == 0 {
		t.Fatal("the transport should have been invoked during the first pass")
	}
	if stub.calls != 0 {
		t.Fatalf("the agent was invoked directly %d times with a transport configured", stub.calls)
	}
	if len(res.SHAs) != 3 {
		t.Fatalf("pre-rebase SHAs = %v, expected 3 commits", res.SHAs)
	}
	shaBeforeRebase := res.SHAs[1]

	var recordBeforeRebase *review.Record
	for i := range res.Records {
		if res.Records[i].SHA == shaBeforeRebase {
			recordBeforeRebase = &res.Records[i]
		}
	}
	if recordBeforeRebase == nil {
		t.Fatalf("no review record for %s after the first pass", shaBeforeRebase)
	}
	findingBeforeRebase := findV2FindingInRecord(*recordBeforeRebase, invocationFindingDescription)
	if findingBeforeRebase == nil {
		t.Fatalf("the record for %s lost the v2 finding: %+v", shaBeforeRebase, recordBeforeRebase)
	}
	if findingBeforeRebase.InvocationID != originalInvocation {
		t.Fatalf("initial InvocationID = %q, expected %q", findingBeforeRebase.InvocationID, originalInvocation)
	}

	// Real rebase: rewrites the 3 commit SHAs without touching content.
	runGitRebase(t, "checkout", "main")
	if err := os.WriteFile("docs.txt", []byte("docs\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitRebase(t, "add", "docs.txt")
	runGitRebase(t, "commit", "-m", "docs: advance main")
	runGitRebase(t, "checkout", "feature")
	runGitRebase(t, "rebase", "main")

	callsSecondPass := 0
	res2, err := review.AnalyzeBranch(ledger, review.BranchOptions{
		Factory:                rebaseV2StubFactory(stub),
		Parallel:               1,
		Store:                  st,
		ReviewTransportFactory: countedTransportFactory(&callsSecondPass, secondPassInvocation),
	})
	if err != nil {
		t.Fatalf("second pass (post-rebase) failed: %v", err)
	}

	if len(res2.SHAs) != 3 || res2.SHAs[1] == shaBeforeRebase {
		t.Fatalf("post-rebase SHAs = %v, expected 3 REWRITTEN commits (before: %s)", res2.SHAs, shaBeforeRebase)
	}
	if len(res2.Pending) != 0 {
		t.Errorf("post-rebase Pending = %v, expected 0 (content already reviewed by blob)", res2.Pending)
	}
	if callsSecondPass != 0 {
		t.Errorf("the transport was invoked %d times after the rebase, expected 0 new calls", callsSecondPass)
	}
	if stub.calls != 0 {
		t.Errorf("the reviewer was invoked %d times after the rebase, expected 0 provider calls", stub.calls)
	}

	var recordAfterRebase *review.Record
	for i := range res2.Records {
		if res2.Records[i].SHA == res2.SHAs[1] {
			recordAfterRebase = &res2.Records[i]
		}
	}
	if recordAfterRebase == nil {
		t.Fatalf("no adopted review record under post-rebase SHA %s", res2.SHAs[1])
	}
	findingAfterRebase := findV2FindingInRecord(*recordAfterRebase, invocationFindingDescription)
	if findingAfterRebase == nil {
		t.Fatalf("the adopted record under %s lost the v2 finding: %+v", res2.SHAs[1], recordAfterRebase)
	}
	if findingAfterRebase.InvocationID != originalInvocation {
		t.Fatalf("post-rebase InvocationID = %q, expected the original identity %q",
			findingAfterRebase.InvocationID, originalInvocation)
	}
}
