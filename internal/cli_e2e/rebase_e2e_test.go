package cli_e2e

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRebaseCancellation(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed rebase cancellation fixture")
	runPublicInit(t, runner)
	if setupCommit := runner.commit("chore: commit initialized rebase cancellation fixture"); setupCommit == "" {
		t.Fatal("rebase cancellation setup commit returned an empty revision")
	}
	if status := runner.gitStatus(runner.repository); status != "" {
		t.Fatalf("rebase cancellation fixture is not clean before branching: %q", status)
	}

	baseBranchResult := runner.git("branch", "--show-current")
	if baseBranchResult.ExitCode != 0 {
		t.Fatalf("discover initial branch failed:\n%s", baseBranchResult.Diagnostic())
	}
	baseBranch := strings.TrimSpace(baseBranchResult.Stdout)
	if baseBranch != "main" && baseBranch != "master" {
		t.Fatalf("initial branch = %q, want main or master", baseBranch)
	}

	remoteRoot := t.TempDir()
	remotePath := filepath.Join(remoteRoot, "origin.git")
	bareInit := runner.runInDir(
		runner.gitPath,
		remoteRoot,
		"init",
		"--quiet",
		"--bare",
		filepath.ToSlash(remotePath),
	)
	if bareInit.ExitCode != 0 {
		t.Fatalf("initialize disposable bare remote failed:\n%s", bareInit.Diagnostic())
	}

	runner.mustGit("remote", "add", "origin", filepath.ToSlash(remotePath))
	runner.mustGit("push", "--quiet", "--set-upstream", "origin", baseBranch)

	const featureBranch = "feature/rebase-cancellation"
	runner.mustGit("checkout", "--quiet", "-b", featureBranch)
	runner.writeFile("feature.txt", "feature change\n")
	runner.commit("test: add feature change")

	runner.mustGit("checkout", "--quiet", baseBranch)
	runner.writeFile("base-advance.txt", "base advance\n")
	baseTipBeforeRebase := runner.commit("test: advance base branch")
	runner.mustGit("push", "--quiet", "origin", baseBranch)

	runner.mustGit("checkout", "--quiet", featureBranch)
	runner.mustGit("config", "--local", "branch."+featureBranch+".remote", "origin")
	runner.mustGit("config", "--local", "branch."+featureBranch+".merge", "refs/heads/"+baseBranch)

	upstream := strings.TrimSpace(runner.mustGit("rev-parse", "--abbrev-ref", "@{upstream}").Stdout)
	if upstream != "origin/"+baseBranch {
		t.Fatalf("feature upstream = %q, want origin/%s", upstream, baseBranch)
	}

	branchBefore := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout)
	if branchBefore != featureBranch {
		t.Fatalf("current branch = %q, want %q", branchBefore, featureBranch)
	}
	statusBefore := runner.gitStatus(runner.repository)
	if statusBefore != "" {
		t.Fatalf("rebase cancellation fixture is not clean before rebase: %q", statusBefore)
	}
	headBefore := cliHead(t, runner)
	featureRefBefore := strings.TrimSpace(runner.mustGit("rev-parse", featureBranch).Stdout)
	trackingRefBefore := strings.TrimSpace(runner.mustGit("rev-parse", "origin/"+baseBranch).Stdout)
	remoteRefBefore := cliRemoteHead(t, runner, baseBranch)
	if trackingRefBefore != baseTipBeforeRebase || remoteRefBefore != baseTipBeforeRebase {
		t.Fatalf("base refs before canceled rebase = tracking %s / remote %s, want %s", trackingRefBefore, remoteRefBefore, baseTipBeforeRebase)
	}

	rebase := runPublicRebaseWithCancellation(t, runner)
	if rebase.ExitCode != 0 {
		t.Fatalf("public rebase cancellation failed:\n%s", rebase.Diagnostic())
	}
	if !strings.Contains(rebase.Stdout, "🚫 Canceled, no changes.") {
		t.Fatalf("public rebase omitted cancellation message:\n%s", rebase.Stdout)
	}

	if got := cliHead(t, runner); got != headBefore {
		t.Fatalf("HEAD after canceled rebase = %s, want unchanged %s", got, headBefore)
	}
	if got := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout); got != branchBefore {
		t.Fatalf("current branch after canceled rebase = %q, want unchanged %q", got, branchBefore)
	}
	if got := strings.TrimSpace(runner.mustGit("rev-parse", featureBranch).Stdout); got != featureRefBefore {
		t.Fatalf("feature ref after canceled rebase = %s, want unchanged %s", got, featureRefBefore)
	}
	if got := runner.gitStatus(runner.repository); got != statusBefore {
		t.Fatalf("worktree status after canceled rebase = %q, want unchanged %q", got, statusBefore)
	}
	if got := strings.TrimSpace(runner.mustGit("rev-parse", "origin/"+baseBranch).Stdout); got != trackingRefBefore {
		t.Fatalf("tracking ref after canceled rebase = %s, want unchanged %s", got, trackingRefBefore)
	}
	if got := cliRemoteHead(t, runner, baseBranch); got != remoteRefBefore {
		t.Fatalf("remote ref after canceled rebase = %s, want unchanged %s", got, remoteRefBefore)
	}
}

func TestCLIRebasePublicProcess(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed rebase fixture")
	runPublicInit(t, runner)
	if setupCommit := runner.commit("chore: commit initialized rebase fixture"); setupCommit == "" {
		t.Fatal("rebase setup commit returned an empty revision")
	}
	if status := runner.gitStatus(runner.repository); status != "" {
		t.Fatalf("rebase fixture is not clean before branching: %q", status)
	}

	baseBranchResult := runner.git("branch", "--show-current")
	if baseBranchResult.ExitCode != 0 {
		t.Fatalf("discover initial branch failed:\n%s", baseBranchResult.Diagnostic())
	}
	baseBranch := strings.TrimSpace(baseBranchResult.Stdout)
	if baseBranch != "main" && baseBranch != "master" {
		t.Fatalf("initial branch = %q, want main or master", baseBranch)
	}

	remoteRoot := t.TempDir()
	remotePath := filepath.Join(remoteRoot, "origin.git")
	bareInit := runner.runInDir(
		runner.gitPath,
		remoteRoot,
		"init",
		"--quiet",
		"--bare",
		filepath.ToSlash(remotePath),
	)
	if bareInit.ExitCode != 0 {
		t.Fatalf("initialize disposable bare remote failed:\n%s", bareInit.Diagnostic())
	}

	runner.mustGit("remote", "add", "origin", filepath.ToSlash(remotePath))
	runner.mustGit("push", "--quiet", "--set-upstream", "origin", baseBranch)

	const featureBranch = "feature/rebase"
	runner.mustGit("checkout", "--quiet", "-b", featureBranch)
	runner.writeFile("feature.txt", "feature change\n")
	featureCommitBefore := runner.commit("test: add feature change")

	runner.mustGit("checkout", "--quiet", baseBranch)
	runner.writeFile("base-advance.txt", "base advance\n")
	baseTipBeforeRebase := runner.commit("test: advance base branch")
	runner.mustGit("push", "--quiet", "origin", baseBranch)

	runner.mustGit("checkout", "--quiet", featureBranch)
	runner.mustGit("config", "--local", "branch."+featureBranch+".remote", "origin")
	runner.mustGit("config", "--local", "branch."+featureBranch+".merge", "refs/heads/"+baseBranch)

	rebase := runPublicRebaseWithConfirmation(t, runner)
	if rebase.ExitCode != 0 {
		t.Fatalf("public rebase failed:\n%s", rebase.Diagnostic())
	}
	confirmation := "✅ " + featureBranch + " rebased onto @{u}."
	if !strings.Contains(rebase.Stdout, confirmation) {
		t.Fatalf("public rebase omitted confirmation %q:\n%s", confirmation, rebase.Stdout)
	}

	rebasedFeatureCommit := cliHead(t, runner)
	if rebasedFeatureCommit == featureCommitBefore {
		t.Fatalf("feature commit was not rewritten: HEAD remained %s", featureCommitBefore)
	}

	parent := runner.git("rev-parse", "HEAD^")
	if parent.ExitCode != 0 {
		t.Fatalf("resolve rebased feature parent failed:\n%s", parent.Diagnostic())
	}
	originTip := runner.git("rev-parse", "origin/"+baseBranch)
	if originTip.ExitCode != 0 {
		t.Fatalf("resolve fetched origin/%s failed:\n%s", baseBranch, originTip.Diagnostic())
	}
	parentSHA := strings.TrimSpace(parent.Stdout)
	originTipSHA := strings.TrimSpace(originTip.Stdout)
	if originTipSHA != baseTipBeforeRebase {
		t.Fatalf("fetched origin/%s tip = %s, want pushed base tip %s", baseBranch, originTipSHA, baseTipBeforeRebase)
	}
	if parentSHA != originTipSHA {
		t.Fatalf("rebased feature parent = %s, want fetched origin/%s tip %s", parentSHA, baseBranch, originTipSHA)
	}
	if status := runner.gitStatus(runner.repository); status != "" {
		t.Fatalf("worktree is not clean after rebase: %q", status)
	}
}

func runPublicRebaseWithConfirmation(t *testing.T, runner *cliRunner) commandResult {
	return runPublicRebaseWithInput(t, runner, "y\n")
}

func runPublicRebaseWithCancellation(t *testing.T, runner *cliRunner) commandResult {
	return runPublicRebaseWithInput(t, runner, "n\n")
}

func runPublicRebaseWithInput(t *testing.T, runner *cliRunner, input string) commandResult {
	t.Helper()

	command := exec.Command(runner.binary, "rebase")
	command.Dir = runner.repository
	command.Env = append([]string(nil), runner.env...)
	command.Stdin = strings.NewReader(input)

	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return commandResult{
		Command:  []string{runner.binary, "rebase"},
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
		Err:      err,
	}
}

func cliRemoteHead(t *testing.T, runner *cliRunner, branch string) string {
	t.Helper()

	result := runner.mustGit("ls-remote", "origin", "refs/heads/"+branch)
	fields := strings.Fields(result.Stdout)
	expectedRef := "refs/heads/" + branch
	if len(fields) != 2 || fields[1] != expectedRef {
		t.Fatalf("remote origin/%s returned unexpected refs: %q", branch, result.Stdout)
	}
	return fields[0]
}
