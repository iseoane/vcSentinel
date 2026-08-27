package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveParentBranchPrecedence(t *testing.T) {
	tests := []struct {
		name, explicit, gh, wantRef, wantEvidence string
		tracking                                  bool
		wantSource                                ParentSource
	}{
		{"explicit parent wins", "B", `{"baseRefName":"A"}`, "B", "explicit parent", false, ParentSourceExplicit},
		{"pull request base wins over tracking upstream", "", `{"baseRefName":"A"}`, "A", "gh pr view", true, ParentSourcePullRequest},
		{"tracking upstream wins", "", "", "B", "tracking upstream", true, ParentSourceTracking},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := parentStackRepository(t)
			if tt.tracking {
				parentGit(t, dir, "branch", "--set-upstream-to=B")
			}
			gh := parentCommandResult{stdout: tt.gh}
			if tt.gh == "" {
				gh = noPullRequestResult()
			}
			var got ParentResolution
			var err error
			if tt.explicit != "" {
				got, err = ResolveParentBranch(ParentResolutionOptions{Worktree: dir, ExplicitParent: tt.explicit})
			} else {
				got, err = resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(gh))
			}
			if err != nil {
				t.Fatalf("resolve parent: %v", err)
			}
			if got.Reference != tt.wantRef || got.Source != tt.wantSource {
				t.Fatalf("resolution = %+v, want %s/%s", got, tt.wantSource, tt.wantRef)
			}
			if !strings.Contains(strings.Join(got.Evidence, "\n"), tt.wantEvidence) {
				t.Errorf("evidence = %v, want %q", got.Evidence, tt.wantEvidence)
			}
		})
	}
}
func TestResolveParentBranchUsesTopologicallyClosestLocalAncestor(t *testing.T) {
	dir := parentStackRepository(t)
	got, err := resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(noPullRequestResult()))
	if err != nil {
		t.Fatalf("resolve parent: %v", err)
	}
	if got.Reference != "B" || got.Source != ParentSourceLocalMergeBase {
		t.Fatalf("resolution = %+v, want local branch B", got)
	}
}

func TestResolveParentBranchRejectsMissingExplicitParent(t *testing.T) {
	dir := parentStackRepository(t)
	parentGit(t, dir, "branch", "--set-upstream-to=B")
	_, err := resolveParentBranch(ParentResolutionOptions{Worktree: dir, ExplicitParent: "missing"}, fakeParentRunner(noPullRequestResult()))
	if err == nil || !strings.Contains(err.Error(), `parent reference "missing" is not a commit`) {
		t.Fatalf("error = %v, want missing explicit parent error without fallback", err)
	}
}

func TestResolveParentBranchPublicationBranch(t *testing.T) {
	dir := parentStackRepository(t)
	parentGit(t, dir, "update-ref", "refs/remotes/origin/b", "B")
	res, err := ResolveParentBranch(ParentResolutionOptions{Worktree: dir, ExplicitParent: "refs/remotes/origin/b"})
	if err != nil || res.Reference != "refs/remotes/origin/b" || res.PublicationBranch != "b" {
		t.Fatalf("full remote parent: (%q, %q, %v), want refs/remotes/origin/b/b", res.Reference, res.PublicationBranch, err)
	}
	parentGit(t, dir, "tag", "tagB", "B")
	sha := strings.TrimSpace(runParentCommand(dir, "git", "rev-parse", "B").stdout)
	for _, ref := range []string{"tagB", sha} {
		_, err := ResolveParentBranch(ParentResolutionOptions{Worktree: dir, ExplicitParent: ref})
		if err == nil || !strings.Contains(err.Error(), "not a publishable branch") {
			t.Errorf("parent %q: err=%v, want publishable-branch failure", ref, err)
		}
	}
	gh := parentCommandResult{stdout: `{"baseRefName":"layer-a"}`}
	parentGit(t, dir, "update-ref", "refs/remotes/origin/layer-a", "B")
	res, err = resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(gh))
	if err != nil || res.Reference != "origin/layer-a" || res.PublicationBranch != "layer-a" {
		t.Fatalf("resolution = %+v, err = %v", res, err)
	}
	parentGit(t, dir, "update-ref", "refs/remotes/upstream/layer-a", "B")
	_, err = resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(gh))
	if err == nil || !strings.Contains(err.Error(), "2 remote candidates") {
		t.Fatalf("ambiguous error = %v", err)
	}
}

func TestResolveParentBranchRejectsMissingPullRequestBase(t *testing.T) {
	dir := parentStackRepository(t)
	parentGit(t, dir, "branch", "--set-upstream-to=B")
	gh := parentCommandResult{stdout: `{"baseRefName":"missing"}`}
	_, err := resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(gh))
	if err == nil || !strings.Contains(err.Error(), `pull request base "missing" has 0 remote candidates`) {
		t.Fatalf("error = %v, want missing pull request base error without fallback", err)
	}
}

func TestResolveParentBranchRejectsUnrelatedSiblingLocalBranch(t *testing.T) {
	dir := siblingParentRepository(t)
	_, err := resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(noPullRequestResult()))
	if err == nil || !strings.Contains(err.Error(), "no reliable") {
		t.Fatalf("error = %v, want no reliable parent signal", err)
	}
}

func TestResolveParentBranchRejectsAmbiguousLocalCandidates(t *testing.T) {
	dir := ambiguousParentRepository(t)
	_, err := resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(noPullRequestResult()))
	var resolutionErr *ParentResolutionError
	if !errors.As(err, &resolutionErr) || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v, want an explicit ambiguity error", err)
	}
}

func TestResolveParentBranchFailsWithoutReliableSignal(t *testing.T) {
	dir := prepararRepositorioPrueba(t, map[string]string{"root.txt": "root\n"})
	parentGit(t, dir, "branch", "-M", "main")
	parentCommit(t, dir, "C")
	_, err := resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(noPullRequestResult()))
	if err == nil || !strings.Contains(err.Error(), "no reliable") || strings.Contains(err.Error(), "fallback") {
		t.Fatalf("error = %v, want failure without a main fallback", err)
	}
}

func TestResolveParentBranchPropagatesPullRequestErrors(t *testing.T) {
	dir := parentStackRepository(t)
	gh := parentCommandResult{stderr: "network unavailable", err: errors.New("exit status 1")}
	_, err := resolveParentBranch(ParentResolutionOptions{Worktree: dir}, fakeParentRunner(gh))
	if err == nil || !strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("error = %v, want the gh error instead of a fallback", err)
	}
}

func parentStackRepository(t *testing.T) string {
	t.Helper()
	dir := prepararRepositorioPrueba(t, map[string]string{"root.txt": "root\n"})
	parentGit(t, dir, "branch", "-M", "main")
	parentCommit(t, dir, "A")
	parentCommit(t, dir, "B")
	parentCommit(t, dir, "C")
	return dir
}

func siblingParentRepository(t *testing.T) string {
	t.Helper()
	dir := prepararRepositorioPrueba(t, map[string]string{"root.txt": "root\n"})
	parentGit(t, dir, "branch", "-M", "main")
	parentCommit(t, dir, "current")
	parentGit(t, dir, "checkout", "-q", "main")
	parentCommit(t, dir, "sibling")
	parentGit(t, dir, "checkout", "-q", "current")
	return dir
}

func ambiguousParentRepository(t *testing.T) string {
	t.Helper()
	dir := prepararRepositorioPrueba(t, map[string]string{"root.txt": "root\n"})
	parentGit(t, dir, "branch", "-M", "main")
	parentCommit(t, dir, "A")
	parentGit(t, dir, "checkout", "-q", "main")
	parentCommit(t, dir, "B")
	parentGit(t, dir, "checkout", "-q", "-b", "C", "A")
	parentGit(t, dir, "merge", "--no-ff", "-q", "B", "-m", "merge A and B")
	return dir
}

func parentCommit(t *testing.T, dir, branch string) {
	t.Helper()
	parentGit(t, dir, "checkout", "-q", "-b", branch)
	file := strings.ToLower(branch) + ".txt"
	if err := os.WriteFile(filepath.Join(dir, file), []byte(branch+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	parentGit(t, dir, "add", file)
	parentGit(t, dir, "commit", "-q", "-m", branch)
}

func fakeParentRunner(gh parentCommandResult) parentCommandRunner {
	return func(worktree, command string, args ...string) parentCommandResult {
		if command == "gh" {
			return gh
		}
		return runParentCommand(worktree, command, args...)
	}
}

func noPullRequestResult() parentCommandResult {
	return parentCommandResult{stderr: "no pull requests found for branch", err: errors.New("exit status 1")}
}

func parentGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}
