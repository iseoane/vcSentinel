package main

import (
	"strings"
	"testing"
)

// TestSubcommandsWithoutFlagsRejectExtraArguments walks the nine
// subcommands that accept no arguments (H1/B6). Until T0.5, `vcsentinel check
// --whatever` exited 0 without complaining.
func TestSubcommandsWithoutFlagsRejectExtraArguments(t *testing.T) {
	subcommands := []string{
		"init", "uninit", "check", "slice",
		"lint", "rebase", "install", "upgrade", "uninstall",
	}
	for _, subcommand := range subcommands {
		t.Run(subcommand, func(t *testing.T) {
			if message := validateArguments(subcommand, nil); message != "" {
				t.Errorf("no arguments should be accepted, got: %s", message)
			}
			message := validateArguments(subcommand, []string{"--whatever"})
			if message == "" {
				t.Fatal("accepted an unknown argument silently")
			}
			if !strings.Contains(message, "--whatever") {
				t.Errorf("the message does not name the rejected argument: %s", message)
			}
			if !strings.Contains(message, subcommand) {
				t.Errorf("the message does not name the subcommand: %s", message)
			}
		})
	}
}

// TestSliceAcceptsItsSubcommands: bare slice accepts no arguments, but
// `slice plan` and `slice apply` are valid and have their own flags.
func TestSliceAcceptsItsSubcommands(t *testing.T) {
	scenarios := []struct {
		name    string
		extras  []string
		allowed bool
	}{
		{"bare slice", nil, true},
		{"slice plan", []string{"plan"}, true},
		{"slice plan with flags", []string{"plan", "--json"}, true},
		{"slice apply with flags", []string{"apply", "--plan", "p.json"}, true},
		{"slice with garbage", []string{"whatever"}, false},
		{"slice with loose flag", []string{"--json"}, false},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			message := validateArguments("slice", scenario.extras)
			if scenario.allowed && message != "" {
				t.Errorf("should be accepted, got: %s", message)
			}
			if !scenario.allowed && message == "" {
				t.Error("should be rejected but was accepted silently")
			}
		})
	}
}

// TestSubcommandsWithOwnFlagsAreUntouched: review, status, and pr parse
// their own arguments, so the dispatcher must not reject them prematurely.
func TestSubcommandsWithOwnFlagsAreUntouched(t *testing.T) {
	for _, subcommand := range []string{"review", "status", "pr", "consent-diff"} {
		if message := validateArguments(subcommand, []string{"--json", "HEAD~1"}); message != "" {
			t.Errorf("%s: the dispatcher must not validate its flags, got: %s", subcommand, message)
		}
	}
}

// TestRejectionMessagePointsToHelp: the message has to say what to do, not
// just that something is wrong.
func TestRejectionMessagePointsToHelp(t *testing.T) {
	message := validateArguments("check", []string{"--whatever"})
	if !strings.Contains(message, "vcsentinel help") {
		t.Errorf("the message does not point to the help: %s", message)
	}
}

func TestCheckAcceptsSupportedModes(t *testing.T) {
	if message := validateArguments("check", []string{"--json"}); message != "" {
		t.Fatalf("check should accept --json: %s", message)
	}
	if message := validateArguments("check", []string{"--staged"}); message != "" {
		t.Fatalf("check should accept --staged: %s", message)
	}
	if message := validateArguments("check", []string{"--unknown"}); message == "" {
		t.Fatal("check accepted an unknown option")
	}
}
