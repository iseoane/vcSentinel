package cli_e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLITUIPublicSmoke(t *testing.T) {
	runner := newCLIRunner(t)
	externalBefore := captureCLIExternalState(t, runner)
	defer assertCLIExternalStateUnchanged(t, runner, externalBefore)

	isolatedHome := cliRunnerEnvironmentValue(t, runner, "HOME")
	registryPath := filepath.Join(isolatedHome, ".vcsentinel", "repositories.json")
	disposableConfigPath := filepath.Join(runner.repository, ".vcsentinel")
	commonDir := cliCommonDir(t, runner)
	disposableCommonStatePath := filepath.Join(commonDir, "vcsentinel")
	disposableRepositoryBefore := runner.gitStatus(runner.repository)
	disposableConfigBefore := pathState(t, disposableConfigPath)
	disposableCommonStateBefore := pathState(t, disposableCommonStatePath)
	isolatedRegistryBefore := pathState(t, registryPath)

	helpVocabulary := []string{
		"Purpose:",
		"interactive dashboard",
		"registered repositories",
		"tracked tasks",
		"Usage:",
		"vcsentinel tui",
		"Flags:",
	}
	var firstHelp string
	for index, invocation := range []struct {
		name string
		args []string
	}{
		{name: "tui --help", args: []string{"tui", "--help"}},
		{name: "help tui", args: []string{"help", "tui"}},
	} {
		result := runner.run(invocation.args...)
		if result.ExitCode != 0 {
			t.Fatalf("%s exit code = %d, want 0:\n%s", invocation.name, result.ExitCode, result.Diagnostic())
		}
		if result.Stderr != "" {
			t.Fatalf("%s wrote unexpected stderr: %q", invocation.name, result.Stderr)
		}
		for _, vocabulary := range helpVocabulary {
			if !strings.Contains(result.Stdout, vocabulary) {
				t.Fatalf("%s omitted stable help vocabulary %q:\n%s", invocation.name, vocabulary, result.Stdout)
			}
		}
		if index == 0 {
			firstHelp = result.Stdout
		} else if result.Stdout != firstHelp {
			t.Fatalf("%s output differs from tui --help:\n%s", invocation.name, result.Stdout)
		}

		if got := runner.gitStatus(runner.repository); got != disposableRepositoryBefore {
			t.Fatalf("%s changed the disposable repository:\nbefore: %q\nafter:  %q", invocation.name, disposableRepositoryBefore, got)
		}
		if got := pathState(t, disposableConfigPath); got != disposableConfigBefore {
			t.Fatalf("%s changed repository-local vcSentinel state: before %q, after %q", invocation.name, disposableConfigBefore, got)
		}
		if got := pathState(t, disposableCommonStatePath); got != disposableCommonStateBefore {
			t.Fatalf("%s changed repository common state: before %q, after %q", invocation.name, disposableCommonStateBefore, got)
		}
		if got := pathState(t, registryPath); got != isolatedRegistryBefore {
			t.Fatalf("%s changed the isolated HOME registry: before %q, after %q", invocation.name, isolatedRegistryBefore, got)
		}
	}
	assertCLIExternalStateUnchanged(t, runner, externalBefore)

	seedCLIRepository(t, runner, "test: seed public TUI fixture")
	runPublicInit(t, runner)
	removeCLIFile(t, registryPath)
	if err := os.MkdirAll(registryPath, 0o755); err != nil {
		t.Fatalf("create isolated HOME registry directory: %v", err)
	}
	registryInfo, err := os.Stat(registryPath)
	if err != nil {
		t.Fatalf("inspect isolated HOME registry directory: %v", err)
	}
	if !registryInfo.IsDir() {
		t.Fatalf("isolated HOME registry path is not a directory: %s", registryPath)
	}

	// Do not start a real Bubble Tea session or detached TUI daemon here. PTY/
	// keyboard interaction is covered by existing package-level TUI/attach tests
	// and is outside this portable process smoke.
	tui := runner.run("tui")
	if tui.ExitCode != 5 {
		t.Fatalf("tui exit code = %d, want 5:\n%s", tui.ExitCode, tui.Diagnostic())
	}
	const overviewFailurePrefix = "❌ Could not load the repository overview:"
	if !strings.HasPrefix(tui.Stdout, overviewFailurePrefix) {
		t.Fatalf("tui diagnostic = %q, want prefix %q", tui.Stdout, overviewFailurePrefix)
	}
	if tui.Stderr != "" {
		t.Fatalf("tui wrote unexpected stderr: %q", tui.Stderr)
	}

	assertCLIDaemonNotRunning(t, "after TUI preflight failure", runner.run("runs", "daemon", "status"))
	assertCLIExternalStateUnchanged(t, runner, externalBefore)
}
