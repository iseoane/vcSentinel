package cli_e2e

import (
	"fmt"
	"strings"
	"testing"
)

func TestCLISetupBoundaryRejectsUnexpectedArguments(t *testing.T) {
	runner := newCLIRunner(t)
	externalBefore := captureCLIExternalState(t, runner)
	disposableRepositoryBefore := runner.gitStatus(runner.repository)
	isolatedHome := cliRunnerEnvironmentValue(t, runner, "HOME")
	isolatedHomeBefore := pathState(t, isolatedHome)

	// The no-argument setup paths are intentionally not exercised: production
	// setup targets fixed /usr/local/bin and user PATH locations, while release
	// URLs have no injectable public CLI endpoint. This test must not perform
	// those side effects or real network calls.
	for _, subcommand := range []string{"install", "upgrade", "uninstall"} {
		t.Run(subcommand, func(t *testing.T) {
			result := runner.run(subcommand, "--unexpected")
			if result.ExitCode != 1 {
				t.Fatalf("%s --unexpected exit code = %d, want 1:\n%s", subcommand, result.ExitCode, result.Diagnostic())
			}

			want := fmt.Sprintf("❌ 'vcsentinel %s' accepts no arguments and received: --unexpected. Run 'vcsentinel help' to see the correct usage.", subcommand)
			if got := strings.TrimSpace(result.Stdout); got != want {
				t.Fatalf("%s --unexpected diagnostic = %q, want %q", subcommand, got, want)
			}
			if result.Stderr != "" {
				t.Fatalf("%s --unexpected wrote unexpected stderr: %q", subcommand, result.Stderr)
			}
			if got := runner.gitStatus(runner.repository); got != disposableRepositoryBefore {
				t.Errorf("disposable repository changed after %s --unexpected:\nbefore: %q\nafter:  %q", subcommand, disposableRepositoryBefore, got)
			}
			if got := pathState(t, isolatedHome); got != isolatedHomeBefore {
				t.Errorf("isolated HOME changed after %s --unexpected:\nbefore: %q\nafter:  %q", subcommand, isolatedHomeBefore, got)
			}
			assertCLIExternalStateUnchanged(t, runner, externalBefore)
		})
	}
}

func cliRunnerEnvironmentValue(t *testing.T, runner *cliRunner, name string) string {
	t.Helper()
	prefix := name + "="
	for _, entry := range runner.env {
		if strings.HasPrefix(entry, prefix) {
			value := strings.TrimPrefix(entry, prefix)
			if value == "" {
				t.Fatalf("isolated environment variable %s is empty", name)
			}
			return value
		}
	}
	t.Fatalf("isolated environment does not define %s", name)
	return ""
}
