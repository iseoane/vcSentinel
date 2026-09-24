package cli_e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	cliPRE2EModel  = "fake-pr-model"
	cliPRE2EBranch = "feature/pr-e2e"
)

func TestCLIPublicPRE2E(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed public PR fixture")
	runPublicInit(t, runner)
	publicationMarker := stageCLIPRFakes(t, runner)
	writeProjectConfig(t, runner, cliPRConfig())
	if setupCommit := runner.commit("chore: commit public PR setup"); setupCommit == "" {
		t.Fatal("public PR setup commit returned an empty revision")
	}

	base := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout)
	if base != "main" && base != "master" {
		t.Fatalf("initial branch = %q, want main or master", base)
	}
	runner.mustGit("checkout", "--quiet", "-b", cliPRE2EBranch)
	runner.writeFile("feature.txt", "deterministic public PR feature\n")
	featureHead := runner.commit("feat(cli): add deterministic public PR feature")
	if featureHead == "" {
		t.Fatal("feature commit returned an empty revision")
	}

	treeStatusBefore := runner.gitStatus(runner.repository)
	headBefore := cliHead(t, runner)
	branchBefore := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout)
	publicationBefore := pathState(t, publicationMarker)

	create := runner.run("pr", "create", "--base", base)
	if create.ExitCode != 1 {
		t.Fatalf("pr create without review exit code = %d, want 1:\n%s", create.ExitCode, create.Diagnostic())
	}
	refusal := create.Stdout + "\n" + create.Stderr
	if !strings.Contains(refusal, "No pr review exists for this branch") ||
		!strings.Contains(refusal, "Run 'vcsentinel pr review' first") {
		t.Fatalf("pr create refusal omitted the missing-review boundary:\n%s", create.Diagnostic())
	}
	if got := runner.gitStatus(runner.repository); got != treeStatusBefore {
		t.Fatalf("missing-review pr create changed the disposable tree:\nbefore: %q\nafter:  %q", treeStatusBefore, got)
	}
	if got := cliHead(t, runner); got != headBefore {
		t.Fatalf("missing-review pr create changed HEAD: got %q, want %q", got, headBefore)
	}
	if got := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout); got != branchBefore {
		t.Fatalf("missing-review pr create changed branch: got %q, want %q", got, branchBefore)
	}
	if got := pathState(t, publicationMarker); got != publicationBefore {
		t.Fatalf("missing-review pr create touched the fake publication boundary: before %q, after %q", publicationBefore, got)
	}

	// Keep this scenario focused on the missing-review refusal and the public
	// review JSON contract. Positive publication and evidence convergence have
	// their own process-level test below.
	reviewResult := runner.run("pr", "review", "--base", base, "--json")
	if reviewResult.ExitCode != 0 {
		t.Fatalf("pr review --json failed:\n%s", reviewResult.Diagnostic())
	}
	if fatal := cliPRFatalDiagnostic(reviewResult.Stderr); fatal != "" {
		t.Fatalf("pr review --json wrote a fatal diagnostic to stderr: %s\n%s", fatal, reviewResult.Diagnostic())
	}

	var report cliPRReviewReport
	if err := json.Unmarshal([]byte(reviewResult.Stdout), &report); err != nil {
		t.Fatalf("pr review --json stdout is not valid JSON: %v\n%s", err, reviewResult.Stdout)
	}
	if report.Base != base || report.Branch != cliPRE2EBranch {
		t.Fatalf("pr review range = base %q branch %q, want base %q branch %q", report.Base, report.Branch, base, cliPRE2EBranch)
	}
	if len(report.SHAs) != 1 || report.SHAs[0] != featureHead {
		t.Fatalf("pr review shas = %v, want exactly feature HEAD %q", report.SHAs, featureHead)
	}
	switch report.Decision {
	case "single", "chain":
	default:
		t.Fatalf("pr review decision = %q, want single or chain", report.Decision)
	}
	if report.ReviewAdmissionFailures != 0 || report.ReviewInfrastructureFailures != 0 {
		t.Fatalf("pr review reported failures: admission=%d infrastructure=%d", report.ReviewAdmissionFailures, report.ReviewInfrastructureFailures)
	}
	if len(report.NetUnavailableCauses) != 0 {
		t.Fatalf("pr review reported unavailable net dimensions: %d", len(report.NetUnavailableCauses))
	}
	if got := pathState(t, publicationMarker); got != publicationBefore {
		t.Fatalf("pr review touched the fake publication boundary: before %q, after %q", publicationBefore, got)
	}
}

func TestCLIPublicPRStaleReviewRefusal(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed public PR stale-review fixture")
	runPublicInit(t, runner)
	publicationMarker := stageCLIPRFakes(t, runner)
	publicationBefore := pathState(t, publicationMarker)
	writeProjectConfig(t, runner, cliPRConfig())
	if baseCommit := runner.commit("chore: commit public PR stale-review base"); baseCommit == "" {
		t.Fatal("public PR stale-review base commit returned an empty revision")
	}

	base := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout)
	if base != "main" && base != "master" {
		t.Fatalf("initial branch = %q, want main or master", base)
	}
	runner.mustGit("checkout", "--quiet", "-b", cliPRE2EBranch)
	runner.writeFile("feature.txt", "deterministic public PR feature\n")
	featureHead := runner.commit("feat(cli): add deterministic public PR feature")
	if featureHead == "" {
		t.Fatal("feature commit returned an empty revision")
	}

	reviewResult := runner.run("pr", "review", "--base", base, "--json")
	if reviewResult.ExitCode != 0 {
		t.Fatalf("pr review --json failed:\n%s", reviewResult.Diagnostic())
	}
	var report cliPRReviewReport
	if err := json.Unmarshal([]byte(reviewResult.Stdout), &report); err != nil {
		t.Fatalf("pr review --json stdout is not valid JSON: %v\n%s", err, reviewResult.Stdout)
	}
	if report.Base != base || report.Branch != cliPRE2EBranch {
		t.Fatalf("pr review range = base %q branch %q, want base %q branch %q", report.Base, report.Branch, base, cliPRE2EBranch)
	}
	if len(report.SHAs) != 1 || report.SHAs[0] != featureHead {
		t.Fatalf("pr review shas = %v, want exactly feature HEAD %q", report.SHAs, featureHead)
	}
	if got := pathState(t, publicationMarker); got != publicationBefore {
		t.Fatalf("pr review touched the fake publication boundary: before %q, after %q", publicationBefore, got)
	}

	statusAfterReview := strings.TrimSpace(runner.gitStatus(runner.repository))
	statusLines := strings.Split(statusAfterReview, "\n")
	if len(statusLines) != 1 || !strings.HasPrefix(statusLines[0], "?? ") {
		t.Fatalf("pr review worktree status = %q, want exactly one untracked evidence path", statusAfterReview)
	}
	evidencePath := filepath.ToSlash(strings.TrimSpace(strings.TrimPrefix(statusLines[0], "?? ")))
	if !strings.HasPrefix(evidencePath, ".vcsentinel/evidence/") {
		t.Fatalf("pr review generated path %q outside .vcsentinel/evidence", evidencePath)
	}
	evidenceBytes, err := os.ReadFile(filepath.Join(runner.repository, filepath.FromSlash(evidencePath)))
	if err != nil {
		t.Fatalf("read generated public PR evidence %q: %v", evidencePath, err)
	}
	if len(strings.TrimSpace(string(evidenceBytes))) == 0 {
		t.Fatalf("generated public PR evidence %q is empty", evidencePath)
	}

	runner.mustGit("add", "--", evidencePath)
	stagedPath := strings.TrimSpace(runner.mustGit("diff", "--cached", "--name-only").Stdout)
	if filepath.Clean(filepath.FromSlash(stagedPath)) != filepath.Clean(filepath.FromSlash(evidencePath)) {
		t.Fatalf("evidence commit staged paths = %q, want exactly %q", stagedPath, evidencePath)
	}
	runner.mustGit("commit", "--quiet", "--message", "chore(evidence): record public PR review evidence")
	evidenceHead := cliHead(t, runner)
	if evidenceHead == featureHead {
		t.Fatal("evidence commit did not advance HEAD")
	}
	committedPaths := strings.Split(strings.TrimSpace(runner.mustGit("diff-tree", "--no-commit-id", "--name-only", "-r", evidenceHead).Stdout), "\n")
	if len(committedPaths) != 1 || filepath.Clean(filepath.FromSlash(committedPaths[0])) != filepath.Clean(filepath.FromSlash(evidencePath)) {
		t.Fatalf("evidence commit changed paths = %q, want exactly %q", committedPaths, evidencePath)
	}
	if got := runner.gitStatus(runner.repository); got != "" {
		t.Fatalf("evidence commit left an unintended disposable-tree change: %q", got)
	}
	if got := pathState(t, publicationMarker); got != publicationBefore {
		t.Fatalf("evidence commit touched the fake publication boundary: before %q, after %q", publicationBefore, got)
	}

	runner.writeFile("feature-follow-up.txt", "deterministic stale-review follow-up\n")
	staleHead := runner.commit("feat(cli): advance stale public PR review")
	if staleHead == "" || staleHead == evidenceHead {
		t.Fatalf("stale feature commit = %q, want a new revision after evidence %q", staleHead, evidenceHead)
	}

	treeStatusBefore := runner.gitStatus(runner.repository)
	headBefore := cliHead(t, runner)
	branchBefore := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout)
	if treeStatusBefore != "" {
		t.Fatalf("stale-review fixture is dirty before pr create: %q", treeStatusBefore)
	}

	create := runner.run("pr", "create", "--base", base)
	if create.ExitCode != 1 {
		t.Fatalf("stale-review pr create exit code = %d, want 1:\n%s", create.ExitCode, create.Diagnostic())
	}
	refusal := create.Stdout + "\n" + create.Stderr
	shortSHA := func(sha string) string {
		if len(sha) > 8 {
			return sha[:8]
		}
		return sha
	}
	wantRefusal := fmt.Sprintf("The stored pr review covers %s, but this branch is now at %s. Re-run 'vcsentinel pr review'.", shortSHA(featureHead), shortSHA(staleHead))
	if !strings.Contains(refusal, wantRefusal) {
		t.Fatalf("stale-review pr create omitted the stored-review/current-HEAD mismatch diagnostic %q:\n%s", wantRefusal, create.Diagnostic())
	}
	if got := pathState(t, publicationMarker); got != publicationBefore {
		t.Fatalf("stale-review pr create touched the fake publication boundary: before %q, after %q", publicationBefore, got)
	}
	if got := cliHead(t, runner); got != headBefore {
		t.Fatalf("stale-review pr create changed HEAD: got %q, want %q", got, headBefore)
	}
	if got := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout); got != branchBefore {
		t.Fatalf("stale-review pr create changed branch: got %q, want %q", got, branchBefore)
	}
	if got := runner.gitStatus(runner.repository); got != treeStatusBefore {
		t.Fatalf("stale-review pr create changed the disposable tree:\nbefore: %q\nafter:  %q", treeStatusBefore, got)
	}
}

func TestCLIPublicPRCreateE2E(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed public PR create fixture")
	runPublicInit(t, runner)
	// Install the existing fake agent, then put the recording fake gh first on
	// PATH. Review must invoke the former, while create must reach the latter.
	stageCLIPRFakes(t, runner)
	ghRecordDir := stageCLIPositivePRFake(t, runner)
	writeProjectConfig(t, runner, cliPRCreateConfig())
	if setupCommit := runner.commit("chore: commit public PR create setup"); setupCommit == "" {
		t.Fatal("public PR create setup commit returned an empty revision")
	}

	base := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout)
	if base != "main" && base != "master" {
		t.Fatalf("initial branch = %q, want main or master", base)
	}
	runner.mustGit("checkout", "--quiet", "-b", cliPRE2EBranch)
	runner.writeFile("feature.txt", "deterministic public PR feature\n")
	featureHead := runner.commit("feat(cli): add deterministic public PR feature")
	if featureHead == "" {
		t.Fatal("feature commit returned an empty revision")
	}

	treeStatusBefore := runner.gitStatus(runner.repository)
	headBefore := cliHead(t, runner)
	branchBefore := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout)
	if treeStatusBefore != "" {
		t.Fatalf("public PR fixture is dirty before review: %q", treeStatusBefore)
	}

	runReview := func(label string) (commandResult, cliPRReviewReport) {
		t.Helper()
		result := runner.run("pr", "review", "--base", base, "--json")
		wantCommand := []string{runner.binary, "pr", "review", "--base", base, "--json"}
		if len(result.Command) != len(wantCommand) {
			t.Fatalf("%s command = %q, want a real vcsentinel subprocess %q:\n%s", label, result.Command, wantCommand, result.Diagnostic())
		}
		for index, want := range wantCommand {
			if result.Command[index] != want {
				t.Fatalf("%s command[%d] = %q, want %q; command = %q:\n%s", label, index, result.Command[index], want, result.Command, result.Diagnostic())
			}
		}
		if result.ExitCode != 0 {
			t.Fatalf("%s failed:\n%s", label, result.Diagnostic())
		}
		if fatal := cliPRFatalDiagnostic(result.Stderr); fatal != "" {
			t.Fatalf("%s wrote a fatal diagnostic to stderr: %s\n%s", label, fatal, result.Diagnostic())
		}

		var report cliPRReviewReport
		if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
			t.Fatalf("%s stdout is not valid JSON: %v\n%s\n%s", label, err, result.Stdout, result.Diagnostic())
		}
		if report.Base != base || report.Branch != cliPRE2EBranch {
			t.Fatalf("%s range = base %q branch %q, want base %q branch %q:\n%s", label, report.Base, report.Branch, base, cliPRE2EBranch, result.Diagnostic())
		}
		switch report.Decision {
		case "single", "chain":
		default:
			t.Fatalf("%s decision = %q, want single or chain:\n%s", label, report.Decision, result.Diagnostic())
		}
		if report.ReviewAdmissionFailures != 0 || report.ReviewInfrastructureFailures != 0 {
			t.Fatalf("%s reported failures: admission=%d infrastructure=%d:\n%s", label, report.ReviewAdmissionFailures, report.ReviewInfrastructureFailures, result.Diagnostic())
		}
		if len(report.NetUnavailableCauses) != 0 {
			t.Fatalf("%s reported unavailable net dimensions: %d:\n%s", label, len(report.NetUnavailableCauses), result.Diagnostic())
		}
		return result, report
	}

	assertNoPublication := func(label string) {
		t.Helper()
		for _, name := range []string{"argv", "body", "cwd"} {
			if got := pathState(t, filepath.Join(ghRecordDir, name)); got != "absent" {
				t.Fatalf("%s touched fake gh record %q: %s", label, name, got)
			}
		}
	}

	_, firstReport := runReview("first public pr review")
	if len(firstReport.SHAs) != 1 || firstReport.SHAs[0] != featureHead {
		t.Fatalf("first public pr review shas = %v, want exactly feature HEAD %q", firstReport.SHAs, featureHead)
	}
	if got := cliHead(t, runner); got != headBefore {
		t.Fatalf("first public pr review changed HEAD: got %q, want %q", got, headBefore)
	}
	if got := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout); got != branchBefore {
		t.Fatalf("first public pr review changed branch: got %q, want %q", got, branchBefore)
	}
	assertNoPublication("first public pr review")

	statusAfterFirstReview := strings.TrimSpace(runner.gitStatus(runner.repository))
	statusLines := strings.Split(statusAfterFirstReview, "\n")
	if len(statusLines) != 1 || !strings.HasPrefix(statusLines[0], "?? ") {
		t.Fatalf("first public pr review worktree status = %q, want exactly one untracked evidence path", statusAfterFirstReview)
	}
	evidencePath := filepath.ToSlash(strings.TrimSpace(strings.TrimPrefix(statusLines[0], "?? ")))
	if !strings.HasPrefix(evidencePath, ".vcsentinel/evidence/") {
		t.Fatalf("first public pr review generated path %q outside .vcsentinel/evidence", evidencePath)
	}
	evidenceBytes, err := os.ReadFile(filepath.Join(runner.repository, filepath.FromSlash(evidencePath)))
	if err != nil {
		t.Fatalf("read generated public PR evidence %q: %v", evidencePath, err)
	}
	if len(strings.TrimSpace(string(evidenceBytes))) == 0 {
		t.Fatalf("generated public PR evidence %q is empty", evidencePath)
	}

	// Only the evidence emitted by the real review subprocess may enter the
	// evidence commit. The review itself must not move HEAD or invoke gh.
	runner.mustGit("add", "--", ".vcsentinel/evidence")
	stagedPath := strings.TrimSpace(runner.mustGit("diff", "--cached", "--name-only").Stdout)
	if filepath.Clean(filepath.FromSlash(stagedPath)) != filepath.Clean(filepath.FromSlash(evidencePath)) {
		t.Fatalf("evidence commit staged paths = %q, want exactly %q", stagedPath, evidencePath)
	}
	runner.mustGit("commit", "--quiet", "--message", "chore(evidence): record public PR review evidence")
	evidenceHead := cliHead(t, runner)
	if evidenceHead == headBefore {
		t.Fatal("evidence commit did not advance HEAD")
	}
	committedPaths := strings.Split(strings.TrimSpace(runner.mustGit("diff-tree", "--no-commit-id", "--name-only", "-r", evidenceHead).Stdout), "\n")
	if len(committedPaths) != 1 || filepath.Clean(filepath.FromSlash(committedPaths[0])) != filepath.Clean(filepath.FromSlash(evidencePath)) {
		t.Fatalf("evidence commit changed paths = %q, want exactly %q", committedPaths, evidencePath)
	}
	if got := runner.gitStatus(runner.repository); got != treeStatusBefore {
		t.Fatalf("evidence commit left an unintended disposable-tree change:\nbefore: %q\nafter: %q", treeStatusBefore, got)
	}
	if got := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout); got != branchBefore {
		t.Fatalf("evidence commit changed branch: got %q, want %q", got, branchBefore)
	}
	assertNoPublication("evidence commit")

	_, secondReport := runReview("second public pr review")
	if len(secondReport.SHAs) == 0 {
		t.Fatal("second public pr review returned no reviewed commits")
	}
	if got := cliHead(t, runner); got != evidenceHead {
		t.Fatalf("second public pr review changed HEAD: got %q, want %q", got, evidenceHead)
	}
	if got := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout); got != branchBefore {
		t.Fatalf("second public pr review changed branch: got %q, want %q", got, branchBefore)
	}
	assertNoPublication("second public pr review")
	secondStatus := strings.TrimSpace(runner.gitStatus(runner.repository))
	if secondStatus != "" {
		lines := strings.Split(secondStatus, "\n")
		if len(lines) != 1 || len(lines[0]) < 3 || lines[0][0] != 'M' || lines[0][1] != ' ' || filepath.Clean(filepath.FromSlash(strings.TrimSpace(lines[0][2:]))) != filepath.Clean(filepath.FromSlash(evidencePath)) {
			t.Fatalf("second public pr review changed unintended paths: %q; expected only %q", secondStatus, evidencePath)
		}
	}

	title := strings.TrimSpace(runner.mustGit("show", "-s", "--format=%s", featureHead).Stdout)
	if title == "" {
		t.Fatalf("feature commit %q has no title", featureHead)
	}

	create := runner.run("pr", "create", "--base", base)
	if create.ExitCode != 0 {
		// This is intentionally a success assertion. In the current
		// implementation the diagnostic exposes whether the committed evidence
		// and the re-reviewed HEAD failed to converge.
		t.Fatalf("public pr create exit code = %d, want documented success and fake-gh publication:\n%s", create.ExitCode, create.Diagnostic())
	}
	if create.Stderr != "" {
		t.Fatalf("pr create wrote unexpected stderr:\n%s", create.Diagnostic())
	}
	if !strings.Contains(create.Stdout, "PR created: https://github.example.invalid/vcSentinel/pull/17") {
		t.Fatalf("pr create output omitted the fake PR URL:\n%s", create.Diagnostic())
	}

	argvData, err := os.ReadFile(filepath.Join(ghRecordDir, "argv"))
	if err != nil {
		t.Fatalf("read fake gh argv record: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(argvData), "\n"), "\n")
	wantPrefix := []string{"pr", "create", "--draft", "--title", title, "--base", base, "--body-file"}
	if len(argv) != len(wantPrefix)+1 {
		t.Fatalf("fake gh argv = %q, want %d arguments including a body path", argv, len(wantPrefix)+1)
	}
	for index, want := range wantPrefix {
		if argv[index] != want {
			t.Fatalf("fake gh argv[%d] = %q, want %q; full argv = %q", index, argv[index], want, argv)
		}
	}
	bodyPath := argv[len(argv)-1]
	if !filepath.IsAbs(bodyPath) {
		t.Fatalf("fake gh body path = %q, want an absolute temporary path", bodyPath)
	}
	tempDir := cliRunnerEnvValue(t, runner, "TMPDIR")
	relativeBodyPath, err := filepath.Rel(tempDir, bodyPath)
	if err != nil || relativeBodyPath == ".." || strings.HasPrefix(relativeBodyPath, ".."+string(filepath.Separator)) {
		t.Fatalf("fake gh body path = %q, escaped isolated temp directory %q", bodyPath, tempDir)
	}
	if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
		t.Fatalf("successful pr create left the temporary body file %q: %v", bodyPath, err)
	}

	cwdData, err := os.ReadFile(filepath.Join(ghRecordDir, "cwd"))
	if err != nil {
		t.Fatalf("read fake gh cwd record: %v", err)
	}
	if got := normalizeCLIPath(string(cwdData)); got != normalizeCLIPath(runner.repository) {
		t.Fatalf("fake gh cwd = %q, want the disposable repository %q", got, runner.repository)
	}
	bodyData, err := os.ReadFile(filepath.Join(ghRecordDir, "body"))
	if err != nil {
		t.Fatalf("read fake gh body record: %v", err)
	}
	if !strings.Contains(string(bodyData), "<!-- vcsentinel-attestation:v1") ||
		!strings.Contains(string(bodyData), evidenceHead) ||
		!strings.Contains(string(bodyData), "<b>ci</b> — not observed by vcSentinel") {
		t.Fatalf("fake gh body omitted the persisted attestation or deterministic CI evidence:\n%s", bodyData)
	}

	if got := runner.gitStatus(runner.repository); got != treeStatusBefore {
		t.Fatalf("successful pr create changed the disposable worktree:\nbefore: %q\nafter:  %q", treeStatusBefore, got)
	}
	if got := cliHead(t, runner); got != evidenceHead {
		t.Fatalf("successful pr create changed HEAD: got %q, want %q", got, evidenceHead)
	}
	if got := strings.TrimSpace(runner.mustGit("branch", "--show-current").Stdout); got != branchBefore {
		t.Fatalf("successful pr create changed branch: got %q, want %q", got, branchBefore)
	}
}

func cliPRCreateConfig() string {
	command := ":"
	if runtime.GOOS == "windows" {
		command = "exit /b 0"
	}
	return cliPRConfig() +
		"validation:\n" +
		"  capabilities:\n" +
		"    e2e:\n" +
		"      command: " + strconv.Quote(command) + "\n" +
		"  profiles:\n" +
		"    standard: [e2e]\n"
}

func stageCLIPositivePRFake(t *testing.T, runner *cliRunner) string {
	t.Helper()
	directory := t.TempDir()
	recordDir := filepath.Join(directory, "gh-record")
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		t.Fatalf("create fake gh record directory: %v", err)
	}
	runner.env = append(runner.env, "VCSENTINEL_CLI_PR_FAKE_GH_RECORD_DIR="+recordDir)
	if runtime.GOOS == "windows" {
		source := filepath.Join(directory, "fake_gh.go")
		const program = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	recordDir := os.Getenv("VCSENTINEL_CLI_PR_FAKE_GH_RECORD_DIR")
	if recordDir == "" {
		fmt.Fprintln(os.Stderr, "fake gh record directory is missing")
		os.Exit(2)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	if err := os.WriteFile(filepath.Join(recordDir, "cwd"), []byte(cwd+"\n"), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	args := os.Args[1:]
	if err := os.WriteFile(filepath.Join(recordDir, "argv"), []byte(strings.Join(args, "\n")+"\n"), 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(5)
	}
	bodyPath := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--body-file" {
			bodyPath = args[i+1]
			break
		}
	}
	if bodyPath == "" {
		fmt.Fprintln(os.Stderr, "fake gh did not receive --body-file")
		os.Exit(6)
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(7)
	}
	if err := os.WriteFile(filepath.Join(recordDir, "body"), body, 0644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(8)
	}
	fmt.Println("https://github.example.invalid/vcSentinel/pull/17")
}
`
		if err := os.WriteFile(source, []byte(program), 0o644); err != nil {
			t.Fatalf("write fake Windows gh source: %v", err)
		}
		buildCLIWindowsPRFake(t, runner, directory, source, filepath.Join(directory, "gh.exe"))
	} else {
		path := filepath.Join(directory, "gh")
		const script = `#!/bin/sh
set -eu
record_dir=${VCSENTINEL_CLI_PR_FAKE_GH_RECORD_DIR:?missing record directory}
pwd > "$record_dir/cwd"
: > "$record_dir/argv"
body_file=
previous=
for arg do
    printf '%s\n' "$arg" >> "$record_dir/argv"
    if [ "$previous" = "--body-file" ]; then
        body_file=$arg
    fi
    previous=$arg
done
if [ -z "$body_file" ]; then
    printf '%s\n' 'fake gh did not receive --body-file' >&2
    exit 6
fi
cat "$body_file" > "$record_dir/body"
printf '%s\n' 'https://github.example.invalid/vcSentinel/pull/17'
`
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake gh executable: %v", err)
		}
	}
	prependRunnerPath(t, runner, directory)
	return recordDir
}

func cliRunnerEnvValue(t *testing.T, runner *cliRunner, name string) string {
	t.Helper()
	prefix := name + "="
	for _, entry := range runner.env {
		if strings.HasPrefix(entry, prefix) {
			value := strings.TrimPrefix(entry, prefix)
			if value == "" {
				t.Fatalf("isolated runner environment has an empty %s", name)
			}
			return value
		}
	}
	t.Fatalf("isolated runner environment has no %s entry", name)
	return ""
}

type cliPRReviewReport struct {
	Branch                       string            `json:"rama"`
	Base                         string            `json:"base"`
	SHAs                         []string          `json:"shas"`
	Decision                     string            `json:"decision"`
	ReviewAdmissionFailures      int               `json:"review_admission_failures"`
	ReviewInfrastructureFailures int               `json:"review_infrastructure_failures"`
	NetUnavailableCauses         []json.RawMessage `json:"net_unavailable_causes"`
}

func cliPRConfig() string {
	return "version: \"2.0\"\n" +
		"active_agent: \"claude\"\n" +
		"agents:\n" +
		"  claude:\n" +
		"    model: \"" + cliPRE2EModel + "\"\n" +
		"    reasoning_effort: \"low\"\n" +
		"review:\n" +
		"  timeout: 5\n" +
		"  parallel: 1\n"
}

func stageCLIPRFakes(t *testing.T, runner *cliRunner) string {
	t.Helper()
	directory := t.TempDir()
	marker := filepath.Join(directory, "gh-invoked")
	if runtime.GOOS == "windows" {
		stageCLIWindowsPRFakes(t, runner, directory)
		runner.env = append(runner.env, "VCSENTINEL_CLI_PR_FAKE_GH_MARKER="+marker)
	} else {
		claude := filepath.Join(directory, "claude")
		if err := os.WriteFile(claude, []byte(cliPRPOSIXClaudeScript(t)), 0o755); err != nil {
			t.Fatalf("write fake Claude executable: %v", err)
		}
		gh := filepath.Join(directory, "gh")
		ghScript := "#!/bin/sh\nprintf '%s\\n' 'invoked' > " + shellSingleQuote(marker) + "\nexit 97\n"
		if err := os.WriteFile(gh, []byte(ghScript), 0o755); err != nil {
			t.Fatalf("write fake gh executable: %v", err)
		}
	}
	prependRunnerPath(t, runner, directory)
	return marker
}

func cliPRPOSIXClaudeScript(t *testing.T) string {
	t.Helper()
	var script strings.Builder
	script.WriteString("#!/bin/sh\n")
	script.WriteString("prompt=$(cat) || exit 2\n")
	script.WriteString("case \"$prompt\" in\n")
	script.WriteString("  *'What model are you actually using?'*)\n")
	script.WriteString("    printf '%s\\n' " + shellSingleQuote(cliPRE2EModel) + "\n")
	script.WriteString("    ;;\n")
	for _, dimension := range cliPRDimensions() {
		fmt.Fprintf(&script, "  *'against the \"%s\" dimension.'*)\n", dimension)
		fmt.Fprintf(&script, "    printf '%%s\\n' %s\n", shellSingleQuote(cliPRReviewEnvelope(t, dimension)))
		script.WriteString("    ;;\n")
	}
	// Intention prompts without a dimension use the canonical logic envelope.
	script.WriteString("  *'Pull request intention:'*)\n")
	fmt.Fprintf(&script, "    printf '%%s\\n' %s\n", shellSingleQuote(cliPRReviewEnvelope(t, "logic")))
	script.WriteString("    ;;\n")
	script.WriteString("  *)\n")
	fmt.Fprintf(&script, "    printf '%%s\\n' %s\n", shellSingleQuote(cliPRReviewEnvelope(t, "logic")))
	script.WriteString("    ;;\n")
	script.WriteString("esac\n")
	return script.String()
}

func cliPRReviewEnvelope(t *testing.T, dimension string) string {
	t.Helper()
	inner, err := json.Marshal(struct {
		Dim      string   `json:"dim"`
		Verdict  string   `json:"verdict"`
		Findings []string `json:"findings"`
	}{Dim: dimension, Verdict: "ok", Findings: []string{}})
	if err != nil {
		t.Fatalf("marshal fake review payload: %v", err)
	}
	payload, err := json.Marshal(struct {
		Type       string `json:"type"`
		Subtype    string `json:"subtype"`
		IsError    bool   `json:"is_error"`
		StopReason string `json:"stop_reason"`
		Result     string `json:"result"`
	}{
		Type:       "result",
		Subtype:    "success",
		IsError:    false,
		StopReason: "end_turn",
		Result:     "BEGIN_REVIEW\n" + string(inner) + "\nEND_REVIEW\n",
	})
	if err != nil {
		t.Fatalf("marshal fake Claude envelope: %v", err)
	}
	return string(payload)
}

func cliPRDimensions() []string {
	return []string{"logic", "style", "design", "tests", "security", "spec"}
}

func stageCLIWindowsPRFakes(t *testing.T, runner *cliRunner, directory string) {
	t.Helper()
	claudeSource := filepath.Join(directory, "fake_claude.go")
	claudeProgram := `package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const model = ` + strconv.Quote(cliPRE2EModel) + `

func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	prompt := string(input)
	if strings.Contains(prompt, "What model are you actually using?") {
		fmt.Println(model)
		return
	}
	dimension := "logic"
	for _, candidate := range []string{"logic", "style", "design", "tests", "security", "spec"} {
		if strings.Contains(prompt, "against the \""+candidate+"\" dimension.") {
			dimension = candidate
			break
		}
	}
	result := "BEGIN_REVIEW\n{\"dim\":\"" + dimension + "\",\"verdict\":\"ok\",\"findings\":[]}\nEND_REVIEW\n"
	fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"stop_reason\":\"end_turn\",\"result\":%q}\n", result)
}
`
	if err := os.WriteFile(claudeSource, []byte(claudeProgram), 0o644); err != nil {
		t.Fatalf("write fake Windows Claude source: %v", err)
	}
	buildCLIWindowsPRFake(t, runner, directory, claudeSource, filepath.Join(directory, "claude.exe"))

	ghSource := filepath.Join(directory, "fake_gh.go")
	const ghProgram = `package main

import (
	"fmt"
	"os"
)

func main() {
	marker := os.Getenv("VCSENTINEL_CLI_PR_FAKE_GH_MARKER")
	if marker == "" {
		fmt.Fprintln(os.Stderr, "fake gh marker is missing")
		os.Exit(2)
	}
	if err := os.WriteFile(marker, []byte("invoked\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(97)
}
`
	if err := os.WriteFile(ghSource, []byte(ghProgram), 0o644); err != nil {
		t.Fatalf("write fake Windows gh source: %v", err)
	}
	buildCLIWindowsPRFake(t, runner, directory, ghSource, filepath.Join(directory, "gh.exe"))
}

func buildCLIWindowsPRFake(t *testing.T, runner *cliRunner, directory, source, output string) {
	t.Helper()
	buildEnv := append([]string(nil), runner.env...)
	buildEnv = append(buildEnv,
		"GO111MODULE=off",
		"GOCACHE="+t.TempDir(),
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
	)
	build := exec.Command(lookupTool(t, "go"), "build", "-o", output, source)
	build.Dir = directory
	build.Env = buildEnv
	if combined, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake Windows PR executable %s: %v\n%s", filepath.Base(output), err, combined)
	}
}

func cliPRFatalDiagnostic(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, "?") || strings.Contains(lower, "fatal") || strings.Contains(lower, "panic:") {
			return trimmed
		}
	}
	return ""
}
