package main

// Tests for the central -h/--help interception (ticket 15): every registered
// command and subcommand answers -h/--help with its dedicated text on stdout,
// nothing on stderr, and the caller stops with exit 0 before any side effect.

import (
	"bytes"
	"strings"
	"testing"
)

// expectedHelpKeys enumerates every help key the dispatcher must serve. It
// deliberately does NOT range over commandHelpTexts: deleting a text or forgetting
// a new subcommand must fail this test instead of silently degrading to the
// legacy error paths.
var expectedHelpKeys = []string{
	"version", "help", "init", "uninit", "check",
	"slice", "slice plan", "slice apply",
	"review", "gate", "lint", "rebase", "status", "metrics", "doctor", "explain",
	"consent-diff",
	"pr", "pr create", "pr review",
	"install", "upgrade", "uninstall",
	"runs", "runs start", "runs status", "runs logs", "runs respond",
	"runs abort", "runs retry", "runs recover", "runs verify", "runs prune",
	"runs attach", "runs daemon",
}

// invocationOfKey translates a help key into the dispatcher arguments that
// must produce it: "runs logs --help" comes from (runs, [logs --help]).
func invocationOfKey(key string) (string, []string) {
	parts := strings.Fields(key)
	return parts[0], append(parts[1:], "--help")
}

// capturedHandleHelp runs the interceptor capturing both streams. A true
// result maps one-to-one onto main() returning naturally, i.e. exit code 0.
func capturedHandleHelp(t *testing.T, subcommand string, args []string) (string, string, bool) {
	t.Helper()
	var out, errOut bytes.Buffer
	served := handleHelp(&out, &errOut, subcommand, args)
	return out.String(), errOut.String(), served
}

// firstLineIsUsage accepts the texts (runsUsage) that open directly with the
// usage line instead of a "Usage:" section.
func firstLineIsUsage(output string) bool {
	first := strings.SplitN(output, "\n", 2)[0]
	return strings.HasPrefix(first, "vcsentinel ")
}

func TestHandleHelpServesEveryRegisteredCommand(t *testing.T) {
	for _, key := range expectedHelpKeys {
		for _, flag := range []string{"--help", "-h"} {
			subcommand, rest := invocationOfKey(key)
			rest[len(rest)-1] = flag
			out, errOut, served := capturedHandleHelp(t, subcommand, rest)

			if !served {
				t.Errorf("%s %s: help did not intercept; the command would keep running", subcommand, strings.Join(rest, " "))
			}
			if strings.TrimSpace(out) == "" {
				t.Errorf("%s %s: empty stdout", key, flag)
			}
			if !strings.Contains(out, "Usage") && !firstLineIsUsage(out) {
				t.Errorf("%s %s: the help does not contain a usage line:\n%s", key, flag, out)
			}
			if errOut != "" {
				t.Errorf("%s %s: stderr must stay empty, received %q", key, flag, errOut)
			}
		}
	}
}

// TestHandleHelpInterceptsAtAnyPosition: in commands with flags the help
// request wins wherever it appears, even mixed with valid flags.
func TestHandleHelpInterceptsAtAnyPosition(t *testing.T) {
	scenarios := []struct {
		subcommand string
		args       []string
	}{
		{"check", []string{"--staged", "--help"}},
		{"check", []string{"--json", "--staged", "--help"}},
		{"slice", []string{"plan", "--json", "--help"}},
		{"slice", []string{"apply", "--plan", "p.json", "--answers", "a.json", "--help"}},
		{"pr", []string{"review", "--base", "main", "--help"}},
		{"pr", []string{"create", "--force", "--reason", "x", "--help"}},
		{"runs", []string{"logs", "--run", "r1", "--limit", "5", "--help"}},
		{"runs", []string{"recover", "--repair", "r1", "--help"}},
		{"review", []string{"HEAD", "--dims", "logic", "--help"}},
		{"gate", []string{"--stage", "pre-push", "--profile", "standard", "--help"}},
	}
	for _, scenario := range scenarios {
		out, errOut, served := capturedHandleHelp(t, scenario.subcommand, scenario.args)
		if !served {
			t.Errorf("%s %v: help was supposed to intercept", scenario.subcommand, scenario.args)
		}
		if !strings.Contains(out, "Usage") {
			t.Errorf("%s %v: missing the usage line:\n%s", scenario.subcommand, scenario.args, out)
		}
		if errOut != "" {
			t.Errorf("%s %v: stderr must stay empty, received %q", scenario.subcommand, scenario.args, errOut)
		}
	}
}

// TestHandleHelpDoesNotInterceptWithoutRequest: without -h/--help the
// dispatch stays intact, including the legacy paths and the commands without
// arguments.
func TestHandleHelpDoesNotInterceptWithoutRequest(t *testing.T) {
	scenarios := []struct {
		subcommand string
		args       []string
	}{
		{"check", []string{"--staged"}},
		{"slice", []string{"plan", "--json"}},
		{"pr", nil},
		{"pr", []string{"review", "--base", "main"}},
		{"runs", nil},
		{"runs", []string{"verify", "--run", "r1"}},
		{"init", nil},
		{"explain", []string{"HEAD~1..HEAD"}},
	}
	for _, scenario := range scenarios {
		if _, _, served := capturedHandleHelp(t, scenario.subcommand, scenario.args); served {
			t.Errorf("%s %v: intercepted without a help request", scenario.subcommand, scenario.args)
		}
	}
}

// TestPrHelpDoesNotRunCleanup: 'vcsentinel pr --help' killed the legacy gh
// passthrough, which purges orphan records BEFORE publishing. The
// interception must cut before: pr text on stdout, zero trace of the
// cleanup message or the passthrough.
func TestPrHelpDoesNotRunCleanup(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--title", "x", "--help"}} {
		out, errOut, served := capturedHandleHelp(t, "pr", args)
		if !served {
			t.Fatalf("pr %v: help was supposed to intercept before the legacy passthrough", args)
		}
		if !strings.Contains(out, "Usage") || !strings.Contains(out, "vcsentinel pr create") {
			t.Errorf("pr %v: the output is not pr's dedicated help:\n%s", args, out)
		}
		if strings.Contains(out, "prior cleanup") || strings.Contains(out, "gh pr create finished") {
			t.Errorf("pr %v: the legacy passthrough left side-effect traces:\n%s", args, out)
		}
		if errOut != "" {
			t.Errorf("pr %v: stderr must stay empty, received %q", args, errOut)
		}
	}
}

// TestHelpCommandMatchesHelpFlag: 'vcsentinel help <topic>' resolves through
// the same map as '<topic> --help': byte-for-byte identical for every
// registered command and subcommand.
func TestHelpCommandMatchesHelpFlag(t *testing.T) {
	for _, key := range expectedHelpKeys {
		var viaHelp bytes.Buffer
		if !writeCommandHelp(&viaHelp, key) {
			t.Fatalf("%s: 'vcsentinel help %s' resolved no text", key, key)
		}
		subcommand, rest := invocationOfKey(key)
		viaFlag, _, served := capturedHandleHelp(t, subcommand, rest)
		if !served {
			t.Fatalf("%s: '%s --help' did not intercept", key, key)
		}
		if viaHelp.String() != viaFlag {
			t.Errorf("%s: 'help %s' and '%s --help' differ.\n--- help ---\n%s\n--- flag ---\n%s",
				key, key, key, viaHelp.String(), viaFlag)
		}
	}
}

// TestHelpTextsQuoteDeclaredUsageLines: the extracted helps must quote the
// same usage line the runtime errors already emit, so the extraction into
// constants prevents drift between both paths.
func TestHelpTextsQuoteDeclaredUsageLines(t *testing.T) {
	scenarios := map[string]string{
		"slice apply":  sliceApplyUsage,
		"explain":      explainUsageArgs,
		"runs start":   runsStartUsage,
		"runs status":  runsStatusUsage,
		"runs logs":    runsLogsUsage,
		"runs respond": runsRespondUsage,
		"runs abort":   runsAbortUsage,
		"runs retry":   runsRetryUsage,
		"runs verify":  runsVerifyUsage,
		"runs prune":   runsPruneUsage,
		"runs attach":  runsAttachUsage,
		"runs daemon":  runsDaemonUsage,
	}
	for key, usageLine := range scenarios {
		text, ok := commandHelpTexts[key]
		if !ok {
			t.Fatalf("%s: missing the help text", key)
		}
		if !strings.Contains(text, usageLine) {
			t.Errorf("%s: the help does not quote the shared usage line %q:\n%s", key, usageLine, text)
		}
	}
	if commandHelpTexts["runs"] != runsUsage {
		t.Error("'runs' must integrate runsUsage, not duplicate it")
	}
}
