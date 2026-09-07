package review

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// auditOutputCritical is a valid audit JSONL that reports one CRITICAL finding
// for every commit audited with it.
const auditOutputCritical = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"x.go\",\"line\":1,\"severity\":\"CRITICAL\",\"description\":\"injected defect\",\"evidence\":\"defect\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"

// stackRepo is a temporary repository holding the stack main -> feature-a ->
// feature-b (current branch: feature-b), plus the SHAs needed by assertions.
type stackRepo struct {
	gitDir  string
	baseSHA string // tip of main
	shaA    string // feature-a's own commit
	shaB    string // feature-b's own commit (current HEAD)
}

// prepareStackRepo builds the three-branch stack required by the T8.2
// acceptance criteria on a real, temporary Git repository.
func prepareStackRepo(t *testing.T) stackRepo {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		runGit(t, args...)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("b\nb\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "base.txt")
	runGit(t, "commit", "-m", "feat(base): trunk")
	stack := stackRepo{gitDir: filepath.Join(repo, ".git")}
	stack.baseSHA = strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	runGit(t, "checkout", "-b", "feature-a")
	stack.shaA = commitInBranch(t, "a.txt", "1\n2\n3\n4\n5\n")
	runGit(t, "checkout", "-b", "feature-b")
	stack.shaB = commitInBranch(t, "b.txt", "x\ny\nz\n")
	return stack
}

// swapParentResolver swaps the parent resolver with a fake for the test.
func swapParentResolver(t *testing.T, replacement ParentResolver) {
	t.Helper()
	previous := defaultParentResolver
	defaultParentResolver = replacement
	t.Cleanup(func() { defaultParentResolver = previous })
}

func fixedResolver(ref string) ParentResolver {
	return func(git.ParentResolutionOptions) (git.ParentResolution, error) {
		return git.ParentResolution{
			Reference: ref,
			Source:    git.ParentSourceLocalMergeBase,
			Evidence:  []string{"local merge-base winner=" + ref},
		}, nil
	}
}

// TestStackedBranchExplainsRanges: with the parent resolved, the
// reviewed range is only the own diff (merge_base(parent, HEAD)..HEAD) and
// parent/base/range evidence is explained on the result.
func TestStackedBranchExplainsRanges(t *testing.T) {
	stack := prepareStackRepo(t)
	swapParentResolver(t, fixedResolver("feature-a"))

	res, err := AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{
		Factory:  stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		Parallel: 2,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if res.Own == nil {
		t.Fatal("Own = nil, expected stacked resolution")
	}
	if res.Own.Parent != "feature-a" || res.Own.ParentSource != string(git.ParentSourceLocalMergeBase) {
		t.Errorf("parent = %s/%s, want feature-a/local_merge_base", res.Own.Parent, res.Own.ParentSource)
	}
	if res.Own.Base != "main" || res.Own.ContextFrom != stack.baseSHA {
		t.Errorf("context = %s from %s, want main from %s", res.Own.Base, res.Own.ContextFrom, stack.baseSHA)
	}
	if res.Own.OwnFrom != stack.shaA {
		t.Errorf("OwnFrom = %s, want %s", res.Own.OwnFrom, stack.shaA)
	}
	if len(res.Own.Evidence) == 0 {
		t.Error("empty Evidence: the resolution must be explainable")
	}
	if len(res.SHAs) != 1 || res.SHAs[0] != stack.shaB {
		t.Errorf("SHAs = %v, want only the own diff [%s]", res.SHAs, stack.shaB)
	}
	if res.Volume != 3 {
		t.Errorf("Volume = %d, want 3 (own commit only)", res.Volume)
	}
}

// TestStackedBranchInheritedFindingDoesNotBlock: the CRITICAL introduced by
// A shows up as inherited when reviewing B and never makes B blocking.
func TestStackedBranchInheritedFindingDoesNotBlock(t *testing.T) {
	stack := prepareStackRepo(t)
	ledger := NewLedger(stack.gitDir)
	swapParentResolver(t, fixedResolver("feature-a"))

	runGit(t, "checkout", "feature-a")
	if _, err := AnalyzeBranch(ledger, BranchOptions{
		Factory:  stubFactory(&auditorStub{auditOutput: auditOutputCritical}),
		Parallel: 1,
	}); err != nil {
		t.Fatalf("audit of A failed: %v", err)
	}
	runGit(t, "checkout", "feature-b")

	res, err := AnalyzeBranch(ledger, BranchOptions{
		Factory:  stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if len(res.Inherited) == 0 {
		t.Fatal("empty Inherited: A's CRITICAL must surface as inherited")
	}
	for _, h := range res.Inherited {
		if h.SHA != stack.shaA || h.Finding.Severity != SevCritical {
			t.Errorf("wrong inherited finding: %+v", h)
		}
	}
	if blockers := BranchBlockers(res.Records); len(blockers) != 0 {
		t.Errorf("B is blocking with %d own critical findings: A's finding is inherited", len(blockers))
	}
	if len(res.Records) != 1 || res.Records[0].SHA != stack.shaB {
		t.Errorf("Records = %v, expected only B's own record", recordSHAs(res.Records))
	}
}

// TestStackedBranchOwnFindingBlocks: a CRITICAL inside B's own diff
// stays own/current and blocks its PR.
func TestStackedBranchOwnFindingBlocks(t *testing.T) {
	stack := prepareStackRepo(t)
	swapParentResolver(t, fixedResolver("feature-a"))

	res, err := AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{
		Factory:  stubFactory(&auditorStub{auditOutput: auditOutputCritical}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if blockers := BranchBlockers(res.Records); len(blockers) == 0 {
		t.Fatal("0 blockers: an own finding must block")
	}
	if len(res.Inherited) != 0 {
		t.Errorf("Inherited = %d, want 0 (no context records)", len(res.Inherited))
	}
}

// TestStackedBranchContextIsReadOnly: context commits without a record
// are neither audited nor become pending; fixing them belongs to the parent PR.
func TestStackedBranchContextIsReadOnly(t *testing.T) {
	stack := prepareStackRepo(t)
	swapParentResolver(t, fixedResolver("feature-a"))

	res, err := AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{
		Factory:  stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	for _, sha := range res.Pending {
		if sha == stack.shaA {
			t.Error("context commit entered Pending: context must not be reviewed")
		}
	}
	recordA, err := NewLedger(stack.gitDir).ReadRecord(stack.shaA)
	if err != nil {
		t.Fatal(err)
	}
	if recordA != nil {
		t.Error("the context commit was audited: stacked mode must be read-only outside the own diff")
	}
}

// TestStackedBranchExplicitParent: explicit internal input goes through
// the T8.1 seam and is verified as a real commit.
func TestStackedBranchExplicitParent(t *testing.T) {
	stack := prepareStackRepo(t)

	res, err := AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{
		Factory:  stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{Parent: "feature-a"},
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if res.Own.Parent != "feature-a" || res.Own.ParentSource != string(git.ParentSourceExplicit) {
		t.Errorf("parent = %s/%s, want feature-a/explicit", res.Own.Parent, res.Own.ParentSource)
	}
	runGit(t, "update-ref", "refs/remotes/origin/feature-a", stack.shaA)
	runGit(t, "branch", "-D", "feature-a")
	remote, err := AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{OnlyPending: true, OwnDiff: &OwnDiffOptions{Parent: "origin/feature-a"}})
	if err != nil || remote.Own.Parent != "origin/feature-a" || remote.Own.PublicationBranch != "feature-a" || remote.Own.OwnFrom != stack.shaA {
		t.Fatalf("remote-only parent = %+v, err=%v", remote, err)
	}

	_, err = AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{
		Factory: stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		OwnDiff: &OwnDiffOptions{Parent: "missing-branch"},
	})
	if err == nil || !strings.Contains(err.Error(), "not a commit") {
		t.Errorf("missing reference = %v, want explicit failure", err)
	}
}

// TestStackedBranchNoSignalFailsWithoutMain: without a reliable parent signal
// the analysis fails explicitly; main is never assumed as the parent.
func TestStackedBranchNoSignalFailsWithoutMain(t *testing.T) {
	stack := prepareStackRepo(t)
	swapParentResolver(t, func(git.ParentResolutionOptions) (git.ParentResolution, error) {
		return git.ParentResolution{}, &git.ParentResolutionError{
			Reason:   "no reliable parent signal",
			Evidence: []string{"local merge-base candidates: none"},
		}
	})

	res, err := AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{
		Factory: stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		OwnDiff: &OwnDiffOptions{ResolveParent: true},
	})
	if err == nil || !strings.Contains(err.Error(), "no reliable parent signal") {
		t.Fatalf("error = %v, want explicit failure for missing signal", err)
	}
	if res != nil && res.Own != nil && res.Own.Parent == "main" {
		t.Error("main was assumed as parent: forbidden in a stack")
	}

	// End to end against the REAL T8.1 resolver: restore it first so this
	// block genuinely exercises git.ResolveParentBranch instead of the fake
	// above. A branch with no pull request, no upstream and no local sibling
	// branches has no reliable signal and must fail instead of falling back
	// to main. The resolver's evidence always names the detected current
	// branch; requiring it proves the fake is out of the loop.
	swapParentResolver(t, git.ResolveParentBranch)
	lone := prepareBranchRepo(t)
	if _, err := AnalyzeBranch(NewLedger(lone), BranchOptions{
		Factory: stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		OwnDiff: &OwnDiffOptions{ResolveParent: true},
	}); err == nil || !strings.Contains(err.Error(), "could not resolve parent branch") ||
		!strings.Contains(err.Error(), "current branch=") {
		t.Fatalf("error = %v, want explicit failure from the REAL T8.1 resolver", err)
	}
}

// TestStackedBranchWithoutOptionsFails: enabling stacked mode without an
// explicit parent or resolution is a configuration error, not silence.
func TestStackedBranchWithoutOptionsFails(t *testing.T) {
	stack := prepareStackRepo(t)
	_, err := AnalyzeBranch(NewLedger(stack.gitDir), BranchOptions{
		Factory: stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		OwnDiff: &OwnDiffOptions{},
	})
	if err == nil || !errors.Is(err, errOwnDiffWithoutParent) {
		t.Fatalf("error = %v, expected errOwnDiffWithoutParent", err)
	}
}

func recordSHAs(records []Record) []string {
	shas := make([]string, 0, len(records))
	for _, record := range records {
		shas = append(shas, record.SHA)
	}
	return shas
}
