package cli_e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIValidationLint(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed lint fixture")
	runPublicInit(t, runner)
	passCommand, failCommand := shellValidationCommands()

	writeProjectConfig(t, runner, lintConfig(passCommand))
	passing := runner.run("lint")
	if passing.ExitCode != 0 || !strings.Contains(passing.Stdout, "lint-pass") {
		t.Fatalf("passing lint did not preserve the shell result:\n%s", passing.Diagnostic())
	}

	writeProjectConfig(t, runner, lintConfig(failCommand))
	failing := runner.run("lint")
	if failing.ExitCode != 1 || !strings.Contains(failing.Stdout, "lint-failure") {
		t.Fatalf("failing lint did not preserve the shell result:\n%s", failing.Diagnostic())
	}
}

func TestCLIValidationGate(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed gate fixture")
	runPublicInit(t, runner)
	passCommand, failCommand := shellValidationCommands()

	writeProjectConfig(t, runner, gateConfig(passCommand))
	passing := runner.run("gate", "--stage", "pre-push")
	assertGateResult(t, passing, 0, "PASS", "Validation green.")

	writeProjectConfig(t, runner, gateConfig(failCommand))
	failing := runner.run("gate", "--stage", "pre-push")
	assertGateResult(t, failing, 1, "VALIDATION_FAILED", "lint-failure")
}

func TestCLIDoctorFakeProbes(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed doctor fixture")
	runPublicInit(t, runner)
	stageDoctorFakeExecutables(t, runner)
	writeProjectConfig(t, runner, doctorConfig())

	result := runner.run("doctor")
	assertDoctorResult(t, result)
}

func TestCLIExplainJSON(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed explain fixture")
	runPublicInit(t, runner)
	if setupCommit := runner.commit("chore: commit explain setup"); setupCommit == "" {
		t.Fatal("explain setup commit returned an empty revision")
	}
	base := cliHead(t, runner)
	runner.writeFile("explain-fixture.go", "package main\n\nfunc ExplainFixture() int { return 42 }\n")
	head := runner.commit("feat: add explain fixture")

	result := runner.run("explain", fmt.Sprintf("%s..%s", base, head), "--json")
	report := decodeExplainReport(t, result)
	assertExplainReport(t, report, base, head)
}

func TestCLIStatusAndMetricsJSON(t *testing.T) {
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, before)

	seedCLIRepository(t, runner, "test: seed state fixture")
	runPublicInit(t, runner)

	status := runner.run("status", "--json")
	statusReport := decodeStatusReport(t, status)
	assertStatusReport(t, runner, statusReport)

	metrics := runner.run("metrics", "--json")
	metricsReport := decodeMetricsReport(t, metrics)
	assertMetricsReport(t, runner, metricsReport)
}

func writeProjectConfig(t *testing.T, runner *cliRunner, content string) {
	t.Helper()
	runner.writeFile(filepath.Join(".vcsentinel", "vcsentinel.yml"), content)
}

func shellValidationCommands() (pass, fail string) {
	if runtime.GOOS == "windows" {
		return "echo lint-pass", "echo lint-failure && exit /b 7"
	}
	return "printf 'lint-pass'", "printf 'lint-failure'; exit 7"
}

func lintConfig(command string) string {
	return fmt.Sprintf("version: \"2.0\"\nlint_commands:\n  - %q\n", command)
}

func gateConfig(command string) string {
	return fmt.Sprintf("version: \"2.0\"\nvalidation:\n  capabilities:\n    lint:\n      command: %q\n      fails_when: \"exit_code\"\n  profiles:\n    standard: [\"lint\"]\n", command)
}

func doctorConfig() string {
	return "version: \"2.0\"\nactive_agent: \"auto\"\nagents:\n  claude:\n    model: \"fake-claude\"\n    reasoning_effort: \"low\"\n  opencode:\n    model: \"fake-opencode\"\n    reasoning_effort: \"low\"\n"
}

func assertGateResult(t *testing.T, result commandResult, wantExit int, wantState, wantEvidence string) {
	t.Helper()
	if result.ExitCode != wantExit {
		t.Fatalf("gate exit code = %d, want %d:\n%s", result.ExitCode, wantExit, result.Diagnostic())
	}
	output := result.Stdout + "\n" + result.Stderr
	if !strings.Contains(output, wantState) || !strings.Contains(output, wantEvidence) {
		t.Fatalf("gate output does not contain state %q and evidence %q:\n%s", wantState, wantEvidence, result.Diagnostic())
	}
}

func stageDoctorFakeExecutables(t *testing.T, runner *cliRunner) {
	t.Helper()
	directory := t.TempDir()
	if runtime.GOOS == "windows" {
		stageWindowsDoctorBinaries(t, runner, directory)
	} else {
		for name, content := range map[string]string{
			"claude":   "#!/bin/sh\nprintf 'fake-claude-probe\\n'\n",
			"opencode": "#!/bin/sh\nprintf 'fake-opencode-probe\\n'\n",
			"rg":       "#!/bin/sh\nexit 0\n",
		} {
			path := filepath.Join(directory, name)
			if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
				t.Fatalf("write fake doctor executable %s: %v", name, err)
			}
		}
	}
	prependRunnerPath(t, runner, directory)
}

func stageWindowsDoctorBinaries(t *testing.T, runner *cliRunner, directory string) {
	t.Helper()
	source := filepath.Join(directory, "fake_doctor.go")
	const program = `package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), filepath.Ext(os.Args[0]))
	switch name {
	case "claude", "opencode":
		fmt.Println("fake-" + name + "-probe")
	}
}
`
	if err := os.WriteFile(source, []byte(program), 0o644); err != nil {
		t.Fatalf("write fake Windows doctor source: %v", err)
	}
	goPath := lookupTool(t, "go")
	buildEnv := append([]string(nil), runner.env...)
	buildEnv = append(buildEnv,
		"GO111MODULE=off",
		"GOCACHE="+t.TempDir(),
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
	)
	for _, name := range []string{"claude", "opencode", "rg"} {
		output := filepath.Join(directory, name+".exe")
		build := exec.Command(goPath, "build", "-o", output, source)
		build.Dir = directory
		build.Env = buildEnv
		if combined, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fake Windows doctor executable %s: %v\n%s", name, err, combined)
		}
	}
}

func prependRunnerPath(t *testing.T, runner *cliRunner, directory string) {
	t.Helper()
	for index, entry := range runner.env {
		if strings.HasPrefix(entry, "PATH=") {
			runner.env[index] = "PATH=" + directory + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
			return
		}
	}
	t.Fatal("isolated runner environment has no PATH entry")
}

func assertDoctorResult(t *testing.T, result commandResult) {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("doctor exit code = %d, want 0:\n%s", result.ExitCode, result.Diagnostic())
	}
	output := result.Stdout + "\n" + result.Stderr
	for _, evidence := range []string{
		"Doctor: preflight of the review environment (advisory, exit 0)",
		"fake-claude-probe",
		"fake-opencode-probe",
		"rg resolves to",
	} {
		if !strings.Contains(output, evidence) {
			t.Fatalf("doctor output omitted %q:\n%s", evidence, result.Diagnostic())
		}
	}
}

type cliExplainReport struct {
	Profile struct {
		Base string `json:"base"`
		Head string `json:"head"`
		Kind string `json:"kind"`
		Size struct {
			Files   int `json:"files"`
			Added   int `json:"added"`
			Deleted int `json:"deleted"`
			Hunks   int `json:"hunks"`
		} `json:"size"`
	} `json:"profile"`
	Characteristics []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"characteristics"`
	Risk struct {
		Level   string `json:"level"`
		Explain string `json:"explain"`
	} `json:"risk"`
	Cohesion struct {
		Clusters       int     `json:"clusters"`
		Score          float64 `json:"score"`
		SuggestedSplit bool    `json:"suggested_split"`
	} `json:"cohesion"`
}

func decodeExplainReport(t *testing.T, result commandResult) cliExplainReport {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("explain --json failed:\n%s", result.Diagnostic())
	}
	if strings.TrimSpace(result.Stderr) != "" {
		t.Fatalf("explain --json wrote unexpected stderr: %q", result.Stderr)
	}
	var report cliExplainReport
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("explain --json output is not valid JSON: %v\n%s", err, result.Stdout)
	}
	return report
}

func assertExplainReport(t *testing.T, report cliExplainReport, base, head string) {
	t.Helper()
	if report.Profile.Base != base || report.Profile.Head != head {
		t.Fatalf("explain range = %q..%q, want %q..%q", report.Profile.Base, report.Profile.Head, base, head)
	}
	if report.Profile.Kind == "" || report.Profile.Size.Files != 1 || report.Profile.Size.Added <= 0 || report.Profile.Size.Deleted != 0 || report.Profile.Size.Hunks != 1 {
		t.Fatalf("explain profile = %+v, want one added one-hunk file", report.Profile)
	}
	if report.Risk.Level != "none" && report.Risk.Level != "low" && report.Risk.Level != "standard" && report.Risk.Level != "elevated" && report.Risk.Level != "high" {
		t.Fatalf("explain risk level %q is outside the public vocabulary", report.Risk.Level)
	}
	if report.Risk.Explain == "" {
		t.Fatal("explain risk omitted its explanation")
	}
	if report.Cohesion.Clusters < 1 || report.Cohesion.Score <= 0 || report.Cohesion.Score > 1 {
		t.Fatalf("explain cohesion = %+v, want a positive normalized result", report.Cohesion)
	}
	for _, characteristic := range report.Characteristics {
		if characteristic.Name == "behavior_change" && characteristic.State == "present" {
			return
		}
	}
	t.Fatalf("explain characteristics omitted a present behavior_change: %+v", report.Characteristics)
}

type cliStatusReport struct {
	Worktree     string            `json:"worktree"`
	Lines        int               `json:"lines"`
	State        string            `json:"state"`
	Records      []json.RawMessage `json:"records"`
	Orphans      int               `json:"orphans"`
	LatestEvents json.RawMessage   `json:"latestEvents"`
}

func decodeStatusReport(t *testing.T, result commandResult) cliStatusReport {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("status --json failed:\n%s", result.Diagnostic())
	}
	if strings.TrimSpace(result.Stderr) != "" {
		t.Fatalf("status --json wrote unexpected stderr: %q", result.Stderr)
	}
	var report cliStatusReport
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("status --json output is not valid JSON: %v\n%s", err, result.Stdout)
	}
	return report
}

func assertStatusReport(t *testing.T, runner *cliRunner, report cliStatusReport) {
	t.Helper()
	if normalizeCLIPath(report.Worktree) != normalizeCLIPath(runner.repository) {
		t.Fatalf("status worktree = %q, want %q", report.Worktree, runner.repository)
	}
	if report.State == "" || report.Lines < 0 || report.Records == nil || len(report.LatestEvents) == 0 {
		t.Fatalf("status report lacks repository/store state: %+v", report)
	}
	assertRunnerStoreIdentity(t, runner)
}

type cliMetricsReport struct {
	Costs       []json.RawMessage `json:"costs"`
	Executions  json.RawMessage   `json:"executions"`
	Findings    json.RawMessage   `json:"findings"`
	Remediation json.RawMessage   `json:"remediation"`
	Stages      []json.RawMessage `json:"stages"`
}

func decodeMetricsReport(t *testing.T, result commandResult) cliMetricsReport {
	t.Helper()
	if result.ExitCode != 0 {
		t.Fatalf("metrics --json failed:\n%s", result.Diagnostic())
	}
	if strings.TrimSpace(result.Stderr) != "" {
		t.Fatalf("metrics --json wrote unexpected stderr: %q", result.Stderr)
	}
	var report cliMetricsReport
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		t.Fatalf("metrics --json output is not valid JSON: %v\n%s", err, result.Stdout)
	}
	return report
}

func assertMetricsReport(t *testing.T, runner *cliRunner, report cliMetricsReport) {
	t.Helper()
	if report.Costs == nil || len(report.Executions) == 0 || len(report.Findings) == 0 || len(report.Remediation) == 0 || report.Stages == nil {
		t.Fatalf("metrics report lacks the expected machine-readable store sections: %+v", report)
	}
	var executions struct {
		LogicalRuns int64 `json:"logical_runs"`
	}
	if err := json.Unmarshal(report.Executions, &executions); err != nil {
		t.Fatalf("decode metrics executions: %v", err)
	}
	if executions.LogicalRuns != 0 {
		t.Fatalf("fresh disposable store has %d logical runs, want 0", executions.LogicalRuns)
	}
	assertRunnerStoreIdentity(t, runner)
}

func assertRunnerStoreIdentity(t *testing.T, runner *cliRunner) {
	t.Helper()
	commonDir := cliCommonDir(t, runner)
	relative, err := filepath.Rel(runner.repository, commonDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("Git common directory %q is outside the disposable repository %q", commonDir, runner.repository)
	}
}
