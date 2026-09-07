// Strict per-subcommand flag parsing tests for `sentinel runs` (ticket 13
// hardening pool, JD-R10 W2): every subcommand × flag pair is asserted
// against the accept/reject matrix, so a flag declared for one subcommand
// becomes a usage error everywhere else (--older-than outside prune,
// --repair outside recover, and so on).
package main

import (
	"io"
	"strings"
	"testing"
)

// runsAllFlags enumerates every parseable flag of the runs CLI surface.
var runsAllFlags = []string{
	"--json", "--run", "--repair", "--text", "--prompt",
	"--policy-id", "--after", "--limit", "--expected-revision", "--older-than",
	"--reason",
}

// runsAcceptMatrix hardcodes, per subcommand, exactly the flags it declares.
// It deliberately does NOT read runsSubcommandFlags: an accidental table
// edit must fail this test instead of silently redefining the contract.
var runsAcceptMatrix = map[string]map[string]bool{
	"start":   {"--prompt": true, "--policy-id": true, "--json": true},
	"status":  {"--run": true, "--json": true},
	"logs":    {"--run": true, "--after": true, "--limit": true, "--json": true},
	"respond": {"--run": true, "--text": true, "--json": true},
	"abort":   {"--run": true, "--orphaned": true, "--reason": true, "--json": true},
	"retry":   {"--run": true, "--expected-revision": true, "--json": true},
	"recover": {"--run": true, "--expected-revision": true, "--repair": true, "--json": true},
	"verify":  {"--run": true, "--json": true},
	"prune":   {"--older-than": true, "--json": true},
}

func TestParseRunOptionsRejectsUndeclaredFlags(t *testing.T) {
	for _, subcommand := range []string{"start", "status", "logs", "respond", "abort", "retry", "recover", "verify", "prune"} {
		accepted := runsAcceptMatrix[subcommand]
		if accepted == nil {
			t.Fatalf("subcommand %q missing from the hardcoded accept matrix", subcommand)
		}
		for _, flag := range runsAllFlags {
			wantAccept := accepted[flag]
			var args []string
			if flag == "--json" {
				args = []string{flag}
			} else {
				args = []string{flag, "1"}
			}
			_, err := parseRunOptions(subcommand, args)
			if wantAccept && err != nil {
				t.Errorf("runs %s %s: unexpected error %v", subcommand, flag, err)
			}
			if !wantAccept {
				if err == nil {
					t.Errorf("runs %s accepts undeclared flag %s; it must be a usage error", subcommand, flag)
				} else if !strings.Contains(err.Error(), "not accepted by 'sentinel runs "+subcommand+"'") {
					t.Errorf("runs %s %s: error %v should name the subcommand and the rejected flag", subcommand, flag, err)
				}
			}
		}
	}
}

func TestParseRunOptionsAcceptedValuesLandInFields(t *testing.T) {
	cases := []struct {
		name       string
		subcommand string
		args       []string
		check      func(t *testing.T, options runOptions)
	}{
		{
			name: "prune parses its cutoff and json switch", subcommand: "prune",
			args: []string{"--older-than", "720h", "--json"},
			check: func(t *testing.T, o runOptions) {
				if !o.olderThanSet || o.olderThan != "720h" || !o.jsonOut {
					t.Fatalf("options = %+v, want olderThan 720h with json output", o)
				}
			},
		},
		{
			name: "recover keeps its repair special case", subcommand: "recover",
			args: []string{"--repair", "abc", "--json"},
			check: func(t *testing.T, o runOptions) {
				if !o.repairSet || o.repairID != "abc" || !o.jsonOut {
					t.Fatalf("options = %+v, want repair abc with json output", o)
				}
			},
		},
		{
			name: "logs parses cursor pagination", subcommand: "logs",
			args: []string{"--run", "r1", "--after", "7", "--limit", "20"},
			check: func(t *testing.T, o runOptions) {
				if o.runID != "r1" || o.afterCursor != 7 || o.limit != 20 {
					t.Fatalf("options = %+v, want run r1 after 7 limit 20", o)
				}
			},
		},
		{
			name: "start defaults policy id", subcommand: "start",
			args: []string{"--prompt", "do things"},
			check: func(t *testing.T, o runOptions) {
				if o.prompt != "do things" || o.policyID != runsDefaultPolicyID {
					t.Fatalf("options = %+v, want prompt with default policy", o)
				}
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			options, err := parseRunOptions(testCase.subcommand, testCase.args)
			if err != nil {
				t.Fatalf("parseRunOptions(%q, %v) error = %v", testCase.subcommand, testCase.args, err)
			}
			testCase.check(t, options)
		})
	}
}

func TestParseRunOptionsUnknownSubcommandFailsClosed(t *testing.T) {
	if _, err := parseRunOptions("teleport", nil); err == nil {
		t.Fatal("an unknown subcommand must fail closed instead of accepting every flag")
	}
}

// TestExecuteRunsRejectsForeignSubcommandFlags proves the strictness end to
// end through the dispatcher: --older-than outside prune exits through the
// documented usage code with a concrete reason on stderr/stdout.
func TestExecuteRunsRejectsForeignSubcommandFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"--older-than outside prune", []string{"status", "--older-than", "720h"}},
		{"--repair outside recover", []string{"verify", "--repair", "abc"}},
		{"--prompt outside start", []string{"respond", "--run", "x", "--prompt", "y", "--text", "z"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			output, code := captureRunsOutput(t, func(w io.Writer) int {
				return executeRuns(w, t.TempDir(), testCase.args)
			})
			if code != runExitUsage {
				t.Fatalf("exit code = %d, want %d (usage): %s", code, runExitUsage, output)
			}
			if !strings.Contains(output, "not accepted by 'sentinel runs") {
				t.Fatalf("output lacks the strict-flag reason: %s", output)
			}
		})
	}
}

// TestParseRunOptionsReasonDoesNotSwallowTheNextFlag pins the cursor invariant
// the parse loop owns: it advances exactly twice per flag/value pair, once in
// the shared post-switch step and once in the for statement. A case that
// advances on its own consumes a third token and silently drops whatever
// follows.
//
// Every existing --reason test placed the flag LAST, which is structurally
// blind to that class of bug: the swallowed token simply does not exist. The
// review found the defect the tests could not.
func TestParseRunOptionsReasonDoesNotSwallowTheNextFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"reason before json", []string{"--run", "r1", "--orphaned", "--reason", "x", "--json"}},
		{"reason before run", []string{"--orphaned", "--reason", "x", "--run", "r1", "--json"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			options, err := parseRunOptions("abort", c.args)
			if err != nil {
				t.Fatalf("parseRunOptions(%v): %v", c.args, err)
			}
			if options.reason != "x" {
				t.Errorf("reason = %q, want %q", options.reason, "x")
			}
			if options.runID != "r1" {
				t.Errorf("runID = %q, want %q: --reason swallowed the following flag", options.runID, "r1")
			}
			if !options.jsonOut {
				t.Error("jsonOut = false: --reason swallowed the following flag, so --json was never parsed")
			}
			if !options.orphaned {
				t.Error("orphaned = false, want true")
			}
		})
	}
}
