package review_test

// Black-box test (package review_test) on purpose: the F2 exit criterion
// requires injecting a real *store.Store into AnalyzeBranch, and
// internal/store already imports internal/review (review.Finding, the v1
// migration of review.Ledger). An internal test (package review) that
// imported internal/store would create the review→store→review cycle; in
// black box there is no cycle because nothing imports review_test.
//
// That is why this file cannot reuse the unexported helpers of
// branch_test.go (runGit, auditorStub, prepareBranchRepo...): they are
// duplicated here, minimal, only for this test.

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// rebaseReviewerStub implements review.AgentReviewer and counts audit calls,
// excluding the unused overview. It returns a real finding so B-01 (findings
// lost after blob adoption) remains detectable.
type rebaseReviewerStub struct {
	calls int
}

// rebaseFindingDescription identifies the injected finding that must remain
// recoverable under the post-rebase SHA.
const rebaseFindingDescription = "rebase test finding"

func (a *rebaseReviewerStub) RunPrompt(prompt string) (string, error) {
	a.calls++
	return "BEGIN_REVIEW\n" +
		`{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"b.txt","line":1,"severity":"WARNING","description":"` + rebaseFindingDescription + `","suggestion":"review before rebase","evidence":"content-b","confidence":"high"}]}` +
		"\nEND_REVIEW\n", nil
}

func (a *rebaseReviewerStub) RunReview(prompt, _ string, _ []string) (string, error) {
	return a.RunPrompt(prompt)
}

func (a *rebaseReviewerStub) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

// recordHasFinding walks every revision/dimension of a record looking for
// a ReviewFinding with that exact description.
func recordHasFinding(f review.Record, description string) bool {
	for _, rev := range f.Revisions {
		for _, dim := range rev.Dims {
			for _, finding := range dim.Findings {
				if finding.Description == description {
					return true
				}
			}
		}
	}
	return false
}

func stubRebaseFactory(a *rebaseReviewerStub) review.ReviewerFactory {
	return func(_ review.ReviewBundle, dimension string) (review.AgentReviewer, string, error) {
		return a, "stub", nil
	}
}

func runGitRebase(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// commitInBranchRebase adds a file and commits it on the current branch.
func commitInBranchRebase(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGitRebase(t, "add", name)
	runGitRebase(t, "commit", "-m", "feat("+name+"): test content")
}

// TestAnalyzeBranchSurvivesRebaseViaBlob is F2 exit criterion 1:
// "a git rebase that does not alter content preserves 100% of the findings".
// It audits 3 real commits, performs a real rebase that rewrites their SHAs
// without touching the content of any file, and checks that zero commits
// remain pending and that the reviewer is NOT invoked a single extra time.
func TestAnalyzeBranchSurvivesRebaseViaBlob(t *testing.T) {
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

	commitInBranchRebase(t, "a.txt", "content a\n")
	commitInBranchRebase(t, "b.txt", "content b\n")
	commitInBranchRebase(t, "c.txt", "content c\n")

	gitDir := repo + "/.git"
	ledger := review.NewLedger(gitDir)
	st := store.NewStore(gitDir)
	stub := &rebaseReviewerStub{}

	res, err := review.AnalyzeBranch(ledger, review.BranchOptions{
		Factory: stubRebaseFactory(stub), Parallel: 1, Store: st,
	})
	if err != nil {
		t.Fatalf("first pass failed: %v", err)
	}
	if len(res.Pending) != 3 {
		t.Fatalf("Pending = %v, expected 3 new commits", res.Pending)
	}
	callsBefore := stub.calls
	if callsBefore == 0 {
		t.Fatal("reviewer should have been called during the first pass")
	}
	if len(res.SHAs) != 3 {
		t.Fatalf("pre-rebase SHAs = %v, expected 3 commits", res.SHAs)
	}
	// The middle commit (b.txt) is the one we track: RangeSHAs returns the
	// SHAs in chronological order (--reverse), so the position survives the
	// rebase because a rebase does not reorder commits.
	shaBBefore := res.SHAs[1]

	// Real rebase: advances main with a commit foreign to "feature" and
	// rewrites feature's 3 commits on top of that new base. Not one line of
	// a.txt, b.txt or c.txt changes, but their SHAs do (the parent changed).
	runGitRebase(t, "checkout", "main")
	if err := os.WriteFile("docs.txt", []byte("docs\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitRebase(t, "add", "docs.txt")
	runGitRebase(t, "commit", "-m", "docs: advance main")
	runGitRebase(t, "checkout", "feature")
	runGitRebase(t, "rebase", "main")

	res2, err := review.AnalyzeBranch(ledger, review.BranchOptions{
		Factory: stubRebaseFactory(stub), Parallel: 1, Store: st,
	})
	if err != nil {
		t.Fatalf("second pass (post-rebase) failed: %v", err)
	}
	if len(res2.Pending) != 0 {
		t.Errorf("post-rebase Pending = %v, expected 0 (content already reviewed by blob)", res2.Pending)
	}
	if stub.calls != callsBefore {
		t.Errorf("reviewer was called %d additional times after rebase; expected 0", stub.calls-callsBefore)
	}

	// The real F2 exit criterion is not just "Pending = 0 and the reviewer
	// is not re-invoked" (the above already proves that): it is that the
	// REAL FINDING is still recoverable under the new SHA of the rewritten
	// commit. Before the B-01 fix, AnalyzeBranch did a bare "continue"
	// without writing anything under the new SHA and this finding vanished
	// from res2.Records.
	if len(res2.SHAs) != 3 {
		t.Fatalf("post-rebase SHAs = %v, expected 3 commits", res2.SHAs)
	}
	shaBAfter := res2.SHAs[1]
	if shaBAfter == shaBBefore {
		t.Fatal("the SHA of b.txt did not change across the rebase: the test proves nothing")
	}

	var recordB *review.Record
	for i := range res2.Records {
		if res2.Records[i].SHA == shaBAfter {
			recordB = &res2.Records[i]
		}
	}
	if recordB == nil {
		t.Fatalf("no record for the post-rebase SHA of b.txt (%s) in res2.Records: the real finding vanished", shaBAfter)
	}
	if !recordHasFinding(*recordB, rebaseFindingDescription) {
		t.Errorf("the record adopted under %s does not preserve the real finding: %+v", shaBAfter, recordB)
	}
}

// gitOutputRebase runs git and returns its trimmed stdout. The stacked test
// needs the pre-rebase tip of the parent layer to rebase the child onto the
// rewritten parent, and runGitRebase discards output.
func gitOutputRebase(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestStackedBranchSurvivesBaseRebaseViaBlob is F8 exit criterion 2: rebasing
// the base of a stacked PR preserves the review of everything that did not
// change. It differs from TestAnalyzeBranchSurvivesRebaseViaBlob (F2 criterion
// 1, no stack) because here the rewritten commit is the PARENT layer: the
// child's own range is recomputed against a parent whose SHA changed, so blob
// coverage has to survive both rewrites at once.
func TestStackedBranchSurvivesBaseRebaseViaBlob(t *testing.T) {
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
	runGitRebase(t, "commit", "-m", "feat(base): stack base")

	// Stack main → layer-a → layer-b. layer-a is the parent PR; layer-b is
	// the one under review.
	runGitRebase(t, "checkout", "-b", "layer-a")
	commitInBranchRebase(t, "a.txt", "content a\n")
	runGitRebase(t, "checkout", "-b", "layer-b")
	commitInBranchRebase(t, "b1.txt", "content b1\n")
	commitInBranchRebase(t, "b2.txt", "content b2\n")

	gitDir := repo + "/.git"
	ledger := review.NewLedger(gitDir)
	st := store.NewStore(gitDir)
	stub := &rebaseReviewerStub{}
	options := func() review.BranchOptions {
		return review.BranchOptions{
			Base: "main", Factory: stubRebaseFactory(stub), Parallel: 1, Store: st,
			OwnDiff: &review.OwnDiffOptions{Parent: "layer-a"},
		}
	}

	res, err := review.AnalyzeBranch(ledger, options())
	if err != nil {
		t.Fatalf("first pass failed: %v", err)
	}
	// Own range only: a.txt belongs to layer-a and must not be audited here.
	if len(res.SHAs) != 2 {
		t.Fatalf("SHAs = %v, expected layer-b's own 2 commits", res.SHAs)
	}
	if len(res.Pending) != 2 {
		t.Fatalf("Pending = %v, expected 2", res.Pending)
	}
	callsBefore := stub.calls
	if callsBefore == 0 {
		t.Fatal("the reviewer should have been called during the first pass")
	}
	shaB1Before := res.SHAs[0]

	// Rebase of the STACK BASE: main advances, layer-a is rewritten onto it,
	// and layer-b is replanted onto the rewritten layer-a. Not a line of
	// b1.txt or b2.txt changes, but every SHA in the stack does.
	tipABefore := gitOutputRebase(t, "rev-parse", "layer-a")
	runGitRebase(t, "checkout", "main")
	if err := os.WriteFile("docs.txt", []byte("docs\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitRebase(t, "add", "docs.txt")
	runGitRebase(t, "commit", "-m", "docs: advance main")
	runGitRebase(t, "checkout", "layer-a")
	runGitRebase(t, "rebase", "main")
	runGitRebase(t, "checkout", "layer-b")
	runGitRebase(t, "rebase", "--onto", "layer-a", tipABefore)

	res2, err := review.AnalyzeBranch(ledger, options())
	if err != nil {
		t.Fatalf("second pass (after the base rebase) failed: %v", err)
	}
	if len(res2.SHAs) != 2 {
		t.Fatalf("post-rebase SHAs = %v, expected 2", res2.SHAs)
	}
	shaB1After := res2.SHAs[0]
	if shaB1After == shaB1Before {
		t.Fatal("the SHA of b1.txt did not change after rebasing the base: the test proves nothing")
	}
	if len(res2.Pending) != 0 {
		t.Errorf("Pending after rebasing the base = %v, expected 0 (content already reviewed by blob)", res2.Pending)
	}
	if stub.calls != callsBefore {
		t.Errorf("the reviewer was called %d extra times after rebasing the base; expected 0", stub.calls-callsBefore)
	}

	// The criterion is not only "no re-audit": the real finding must stay
	// recoverable under the new SHA of the rewritten commit.
	var recordB1 *review.Record
	for i := range res2.Records {
		if res2.Records[i].SHA == shaB1After {
			recordB1 = &res2.Records[i]
		}
	}
	if recordB1 == nil {
		t.Fatalf("no record for the post-rebase SHA of b1.txt (%s): the review was lost when rebasing the base", shaB1After)
	}
	if !recordHasFinding(*recordB1, rebaseFindingDescription) {
		t.Errorf("the record adopted under %s does not preserve the real finding: %+v", shaB1After, recordB1)
	}
}
