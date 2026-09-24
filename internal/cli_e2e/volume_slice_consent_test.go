package cli_e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCLIVolumeJSONAndStagedLimit(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed volume fixture")
	runPublicInit(t, runner)

	check := runner.run("check", "--json")
	report := decodeVolumeCheck(t, check)
	assertVolumeCheck(t, runner, report)

	const oversizedName = "volume-limit.go"
	runner.writeFile(oversizedName, volumeOversizedGoSource(405))
	runner.mustGit("add", "--", oversizedName)
	headBefore := cliHead(t, runner)

	staged := runner.run("check", "--staged")
	if staged.ExitCode != 1 {
		t.Fatalf("check --staged exit code = %d, want 1:\n%s", staged.ExitCode, staged.Diagnostic())
	}
	if output := staged.Stdout + "\n" + staged.Stderr; !strings.Contains(output, "Staged commit rejected") || !strings.Contains(output, "400-line review budget") {
		t.Fatalf("check --staged did not report the review-budget rejection:\n%s", staged.Diagnostic())
	}

	cleanStagedFile(t, runner, oversizedName)
	if got := cliHead(t, runner); got != headBefore {
		t.Fatalf("check --staged changed HEAD from %s to %s", headBefore, got)
	}
}

func TestCLISlicePlanApply(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed slice fixture")
	runPublicInit(t, runner)
	if setupCommit := runner.commit("chore: commit isolated setup"); setupCommit == "" {
		t.Fatal("setup commit returned an empty revision")
	}

	const intendedContent = "package main\n\nfunc target() {}\n"
	runner.writeFile("slice-target.go", intendedContent)
	planResult := runner.run("slice", "plan", "--json")
	plan := decodeSlicePlan(t, planResult)
	assertSmallSlicePlan(t, plan, planResult.Stdout)

	runner.writeFile("plan.json", planResult.Stdout)
	answers := fmt.Sprintf(`{"plan_id":%q,"answers":{}}`, plan.PlanID)
	runner.writeFile("answers.json", answers)

	commitsBefore := cliCommitCount(t, runner)
	apply := runner.run("slice", "apply", "--plan", "plan.json", "--answers", "answers.json")
	if apply.ExitCode != 0 {
		t.Fatalf("slice apply failed:\n%s", apply.Diagnostic())
	}
	if got := cliCommitCount(t, runner); got != commitsBefore+1 {
		t.Fatalf("slice apply created %d commits, want 1", got-commitsBefore)
	}
	headAfter := cliHead(t, runner)
	if got := runner.git("show", "HEAD:slice-target.go"); got.ExitCode != 0 || got.Stdout != intendedContent {
		t.Fatalf("the intended file was not committed:\n%s", got.Diagnostic())
	}
	message := runner.git("show", "-s", "--format=%s", "HEAD")
	if message.ExitCode != 0 || strings.TrimSpace(message.Stdout) != "chore(slice): auto-fragmented backend batch #1" {
		t.Fatalf("slice commit message was not the deterministic fallback:\n%s", message.Diagnostic())
	}

	reapply := runner.run("slice", "apply", "--plan", "plan.json", "--answers", "answers.json")
	if reapply.ExitCode == 0 {
		t.Fatalf("reapplying the plan unexpectedly succeeded:\n%s", reapply.Diagnostic())
	}
	if got := cliHead(t, runner); got != headAfter {
		t.Fatalf("reapplying the plan changed HEAD from %s to %s", headAfter, got)
	}
	if got := cliCommitCount(t, runner); got != commitsBefore+1 {
		t.Fatalf("reapplying the plan fabricated another commit: count = %d, want %d", got, commitsBefore+1)
	}
	removeCLIFile(t, filepath.Join(runner.repository, "plan.json"))
	removeCLIFile(t, filepath.Join(runner.repository, "answers.json"))
}

func TestCLIConsentDiffCycle(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed consent fixture")
	runPublicInit(t, runner)
	commonDir := cliCommonDir(t, runner)

	assertConsentState(t, runner.run("consent-diff", "status"), "revoked", commonDir)
	assertConsentState(t, runner.run("consent-diff", "grant"), "granted", commonDir)
	assertConsentState(t, runner.run("consent-diff", "status"), "granted", commonDir)
	assertConsentState(t, runner.run("consent-diff", "revoke"), "revoked", commonDir)

}

type cliExternalState struct {
	rootStatus   string
	homeRegistry string
}

type volumeCheckReport struct {
	Worktree      string `json:"worktree"`
	AuthoredLines int    `json:"authored_lines"`
	State         string `json:"state"`
	Advisory      bool   `json:"advisory"`
}

type slicePlanReport struct {
	PlanID           string            `json:"plan_id"`
	WorktreeState    string            `json:"worktree_state"`
	Batches          []sliceBatch      `json:"batches"`
	PendingDecisions []json.RawMessage `json:"pending_decisions"`
	Automatic        []json.RawMessage `json:"automatic_decisions"`
}

type sliceBatch struct {
	Layer       string   `json:"layer"`
	Paths       []string `json:"paths"`
	Lines       int      `json:"lines"`
	Message     string   `json:"message"`
	IsOversized bool     `json:"is_oversized"`
}

func captureCLIExternalState(t *testing.T, runner *cliRunner) cliExternalState {
	t.Helper()
	return cliExternalState{
		rootStatus:   runner.gitStatus(repositoryRoot(t)),
		homeRegistry: pathState(t, filepath.Join(realHome(t), ".vcsentinel", "repositories.json")),
	}
}

func assertCLIExternalStateUnchanged(t *testing.T, runner *cliRunner, before cliExternalState) {
	t.Helper()
	if got := runner.gitStatus(repositoryRoot(t)); got != before.rootStatus {
		t.Errorf("real repository status changed:\nbefore: %q\nafter:  %q", before.rootStatus, got)
	}
	if got := pathState(t, filepath.Join(realHome(t), ".vcsentinel", "repositories.json")); got != before.homeRegistry {
		t.Errorf("real home repository registry changed:\nbefore: %q\nafter:  %q", before.homeRegistry, got)
	}
}

func seedCLIRepository(t *testing.T, runner *cliRunner, message string) {
	t.Helper()
	runner.writeFile("base.txt", "base\n")
	if commit := runner.commit(message); commit == "" {
		t.Fatal("fixture seed commit returned an empty revision")
	}
}

func runPublicInit(t *testing.T, runner *cliRunner) {
	t.Helper()
	result := runner.run("init")
	if result.ExitCode != 0 {
		t.Fatalf("public init failed:\n%s", result.Diagnostic())
	}
	if _, err := os.Stat(filepath.Join(runner.repository, ".vcsentinel", "vcsentinel.yml")); err != nil {
		t.Fatalf("public init did not create the project configuration: %v", err)
	}
}

func decodeVolumeCheck(t *testing.T, result commandResult) volumeCheckReport {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("check --json failed:\n%s", result.Diagnostic())
	}
	if strings.TrimSpace(result.Stderr) != "" {
		t.Fatalf("check --json wrote unexpected stderr: %q", result.Stderr)
	}
	var report volumeCheckReport
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("check --json output is not valid JSON: %v\n%s", err, result.Stdout)
	}
	return report
}

func assertVolumeCheck(t *testing.T, runner *cliRunner, report volumeCheckReport) {
	t.Helper()
	if got := normalizeCLIPath(report.Worktree); got != normalizeCLIPath(runner.repository) {
		t.Errorf("check worktree = %q, want %q", got, runner.repository)
	}
	if report.State != "SMALL" || !report.Advisory {
		t.Errorf("check report = state %q advisory %t, want SMALL/true", report.State, report.Advisory)
	}
	if report.AuthoredLines < 0 || report.AuthoredLines >= 400 {
		t.Errorf("initial authored volume = %d, want a value below 400", report.AuthoredLines)
	}
}

func volumeOversizedGoSource(declarations int) string {
	var source strings.Builder
	source.WriteString("package main\n\n")
	for index := 0; index < declarations; index++ {
		fmt.Fprintf(&source, "var volumeValue%d = %d\n", index, index)
	}
	return source.String()
}

func cleanStagedFile(t *testing.T, runner *cliRunner, name string) {
	t.Helper()
	removedFromIndex := runner.git("rm", "--cached", "--force", "--", name)
	if removedFromIndex.ExitCode != 0 {
		t.Fatalf("clean %s from the index failed:\n%s", name, removedFromIndex.Diagnostic())
	}
	path := filepath.Join(runner.repository, name)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove disposable file %s: %v", name, err)
	}
	if staged := runner.git("diff", "--cached", "--quiet"); staged.ExitCode != 0 {
		t.Fatalf("the disposable index is not clean:\n%s", staged.Diagnostic())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disposable file %s remains after cleanup: %v", name, err)
	}
}

func decodeSlicePlan(t *testing.T, result commandResult) slicePlanReport {
	t.Helper()
	if result.ExitCode == 3 {
		t.Fatalf("unexpected pending decisions in the small fixture; exact plan:\n%s", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Fatalf("slice plan failed:\n%s", result.Diagnostic())
	}
	if strings.TrimSpace(result.Stderr) != "" {
		t.Fatalf("slice plan wrote unexpected stderr: %q", result.Stderr)
	}
	var plan slicePlanReport
	if err := json.Unmarshal([]byte(result.Stdout), &plan); err != nil {
		t.Fatalf("slice plan output is not valid JSON: %v\n%s", err, result.Stdout)
	}
	return plan
}

func assertSmallSlicePlan(t *testing.T, plan slicePlanReport, raw string) {
	t.Helper()
	if len(plan.PendingDecisions) != 0 {
		t.Fatalf("unexpected pending decisions in the small fixture; exact plan:\n%s", raw)
	}
	if len(plan.Automatic) != 0 {
		t.Fatalf("unexpected automatic decisions in the small fixture; exact plan:\n%s", raw)
	}
	if plan.PlanID == "" || plan.WorktreeState == "" {
		t.Fatalf("slice plan omitted its identity: %+v", plan)
	}
	if len(plan.Batches) != 1 {
		t.Fatalf("small slice plan batches = %d, want 1: %s", len(plan.Batches), raw)
	}
	batch := plan.Batches[0]
	if len(batch.Paths) != 1 || batch.Paths[0] != "slice-target.go" {
		t.Fatalf("slice plan paths = %v, want [slice-target.go]", batch.Paths)
	}
	if batch.Layer != "backend" || batch.Lines != 3 || batch.IsOversized {
		t.Fatalf("slice batch = %+v, want backend/3/non-oversized", batch)
	}
	if batch.Message != "chore(slice): auto-fragmented backend batch #1" {
		t.Fatalf("slice batch message = %q, want deterministic fallback", batch.Message)
	}
}

func cliHead(t *testing.T, runner *cliRunner) string {
	t.Helper()
	result := runner.git("rev-parse", "HEAD")
	if result.ExitCode != 0 {
		t.Fatalf("resolve HEAD:\n%s", result.Diagnostic())
	}
	return strings.TrimSpace(result.Stdout)
}

func cliCommitCount(t *testing.T, runner *cliRunner) int {
	t.Helper()
	result := runner.git("rev-list", "--count", "HEAD")
	if result.ExitCode != 0 {
		t.Fatalf("count commits:\n%s", result.Diagnostic())
	}
	count, err := strconv.Atoi(strings.TrimSpace(result.Stdout))
	if err != nil {
		t.Fatalf("parse commit count %q: %v", result.Stdout, err)
	}
	return count
}

func cliCommonDir(t *testing.T, runner *cliRunner) string {
	t.Helper()
	result := runner.git("rev-parse", "--git-common-dir")
	if result.ExitCode != 0 {
		t.Fatalf("resolve Git common directory:\n%s", result.Diagnostic())
	}
	path := filepath.FromSlash(strings.TrimSpace(result.Stdout))
	if !filepath.IsAbs(path) {
		path = filepath.Join(runner.repository, path)
	}
	return filepath.Clean(path)
}

func assertConsentState(t *testing.T, result commandResult, expected, expectedRepository string) {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("consent-diff command failed:\n%s", result.Diagnostic())
	}
	output := result.Stdout
	if got := cliOutputField(output, "External diff consent: "); got != expected {
		t.Fatalf("consent state = %q, want %q:\n%s", got, expected, output)
	}
	if got := normalizeCLIPath(cliOutputField(output, "Repository: ")); got != normalizeCLIPath(expectedRepository) {
		t.Fatalf("consent repository = %q, want %q:\n%s", got, expectedRepository, output)
	}
	if cliOutputField(output, "Local user: ") == "" {
		t.Fatalf("consent output omitted the local user:\n%s", output)
	}
	if expected == "granted" && !strings.Contains(output, "Granted at: ") {
		t.Fatalf("granted consent omitted its timestamp:\n%s", output)
	}
	if expected == "revoked" && strings.Contains(output, "Granted at: ") {
		t.Fatalf("revoked consent still reports a grant timestamp:\n%s", output)
	}
}

func cliOutputField(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func normalizeCLIPath(path string) string {
	return filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))
}

func removeCLIFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove disposable file %s: %v", path, err)
	}
}
