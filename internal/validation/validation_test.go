package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

// fakeAgent implements agentadapter.PromptAdapter for the tests of delegation
// with no capabilities configured.
type fakeAgent struct {
	output string
	err    error
}

func (a *fakeAgent) RunPrompt(prompt string) (string, error) {
	return a.output, a.err
}

// cfgWithCapability builds a minimal config with a single capability in a
// single "standard" profile.
func cfgWithCapability(name string, cap config.CapabilityConfig) config.Config {
	return config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{name: cap},
			Profiles:     map[string][]string{"standard": {name}},
		},
	}
}

// TestRunProfile_CapabilityFailsExitCode: a fake command with exit != 0
// produces a failed ValidationRun whose Finding carries the real output as
// evidence.
func TestRunProfile_CapabilityFailsExitCode(t *testing.T) {
	cfg := cfgWithCapability("lint", config.CapabilityConfig{
		Command:   "go vet ./...",
		FailsWhen: config.FailsWhenExitCode,
	})
	runs, err := RunProfile("standard", RunOptions{
		Cfg: cfg,
		Run: func(command string) (int, string, error) {
			return 1, "vet: broken package", nil
		},
	})
	if err != nil {
		t.Fatalf("RunProfile failed: %v", err)
	}
	if len(runs) != 1 || runs[0].Exit != 1 {
		t.Fatalf("runs = %+v, expected a run with exit 1", runs)
	}
	findings := Findings(runs, cfg.Validation.Capabilities)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, expected 1", findings)
	}
	f := findings[0]
	if f.Source != "validation" || f.Severity != "CRITICAL" {
		t.Errorf("finding = %+v, expected Source=validation Severity=CRITICAL", f)
	}
	if !strings.Contains(f.Evidence, "vet: broken package") {
		t.Errorf("Evidence = %q, expected it to include the real output", f.Evidence)
	}
}

// TestRunProfile_OutputNotEmptyFailsWithZeroExit: fails_when output_not_empty
// fails even when the exit code is 0 (gofmt -l case).
func TestRunProfile_OutputNotEmptyFailsWithZeroExit(t *testing.T) {
	cfg := cfgWithCapability("gofmt", config.CapabilityConfig{
		Command:   "gofmt -l .",
		FailsWhen: config.FailsWhenOutputNotEmpty,
	})
	runs, err := RunProfile("standard", RunOptions{
		Cfg: cfg,
		Run: func(command string) (int, string, error) {
			return 0, "unformatted_file.go\n", nil
		},
	})
	if err != nil {
		t.Fatalf("RunProfile failed: %v", err)
	}
	findings := Findings(runs, cfg.Validation.Capabilities)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, expected 1 (output_not_empty must fail even when exit is 0)", findings)
	}
}

// TestRunProfile_OutputNotEmptyDoesNotFailWhenEmpty: exit 0 and empty output
// does not fail with output_not_empty.
func TestRunProfile_OutputNotEmptyDoesNotFailWhenEmpty(t *testing.T) {
	cfg := cfgWithCapability("gofmt", config.CapabilityConfig{
		Command:   "gofmt -l .",
		FailsWhen: config.FailsWhenOutputNotEmpty,
	})
	runs, err := RunProfile("standard", RunOptions{
		Cfg: cfg,
		Run: func(command string) (int, string, error) {
			return 0, "", nil
		},
	})
	if err != nil {
		t.Fatalf("RunProfile failed: %v", err)
	}
	findings := Findings(runs, cfg.Validation.Capabilities)
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, expected 0", findings)
	}
}

// Without an opaque authorization from the graph, "partial" is impossible
// even if the capability supports scope.
func TestRunProfile_WithoutAuthorizationUsesFullCommand(t *testing.T) {
	cfg := cfgWithCapability("test", config.CapabilityConfig{
		Command:       "go test ./...",
		SupportsScope: true,
		ScopedCommand: "go test {packages}",
	})
	runs, err := RunProfile("standard", RunOptions{
		Cfg: cfg,
		Run: func(command string) (int, string, error) {
			return 0, "", nil
		},
	})
	if err != nil {
		t.Fatalf("RunProfile failed: %v", err)
	}
	if runs[0].Scope != ScopeFull || runs[0].Command != "go test ./..." || runs[0].ScopeReason == "" {
		t.Errorf("runs[0] = %+v, expected the full unscoped command", runs[0])
	}
}

func TestValidScopeElement_RejectsShellMetacharacters(t *testing.T) {
	for _, element := range []string{"pkg; rm -rf /tmp/something", "pkg`whoami`", "pkg$(whoami)", "pkg extra", `pkg"something"`} {
		if validScopeElement.MatchString(element) {
			t.Errorf("the whitelist accepted %q", element)
		}
	}
}

// TestRunProfile_WithoutCapabilitiesDelegatesTestedContract: with no
// capabilities configured for the profile, it delegates to the agent and
// produces runs equivalent to the tested contract (same path that
// internal/ops.Verificar already covered).
func TestRunProfile_WithoutCapabilitiesDelegatesTestedContract(t *testing.T) {
	agent := &fakeAgent{output: "all green\ntested: make test; go vet ./..."}
	runs, err := RunProfile("standard", RunOptions{
		Cfg:   config.Config{},
		Agent: agent,
	})
	if err != nil {
		t.Fatalf("RunProfile failed: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, expected 2 (tested contract with 2 commands)", runs)
	}
	if runs[0].Command != "make test" || runs[1].Command != "go vet ./..." {
		t.Errorf("commands = %+v, expected [make test, go vet ./...]", runs)
	}
	for _, r := range runs {
		if r.Capability != delegatedCapability {
			t.Errorf("Capability = %q, expected %q", r.Capability, delegatedCapability)
		}
	}
}

// TestRunProfile_WithoutCapabilitiesWithoutAgentDoesNotBlock: with no
// capabilities and no agent, it degrades to an empty list without error:
// validation never blocks.
func TestRunProfile_WithoutCapabilitiesWithoutAgentDoesNotBlock(t *testing.T) {
	runs, err := RunProfile("standard", RunOptions{Cfg: config.Config{}})
	if err != nil {
		t.Fatalf("RunProfile failed: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("runs = %+v, expected empty", runs)
	}
}

// TestRunProfile_AgentFailsDoesNotBlock: an unavailable agent or one with an
// error does not block validation (same criterion as internal/ops.Verificar).
func TestRunProfile_AgentFailsDoesNotBlock(t *testing.T) {
	cases := map[string]*fakeAgent{
		"unavailable":      {output: "unavailable"},
		"error":            {output: "", err: errors.New("no response")},
		"without contract": {output: "response without contract"},
	}
	for name, agent := range cases {
		runs, err := RunProfile("standard", RunOptions{Cfg: config.Config{}, Agent: agent})
		if err != nil {
			t.Fatalf("%s: RunProfile failed: %v", name, err)
		}
		if len(runs) != 0 {
			t.Errorf("%s: runs = %+v, expected empty", name, runs)
		}
	}
}

// TestRunProfile_UnknownCapabilityInProfile: if the profile references a
// capability that does not exist in Capabilities, it is a configuration
// error.
func TestRunProfile_UnknownCapabilityInProfile(t *testing.T) {
	cfg := config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{},
			Profiles:     map[string][]string{"standard": {"nonexistent"}},
		},
	}
	_, err := RunProfile("standard", RunOptions{
		Cfg: cfg,
		Run: func(command string) (int, string, error) {
			return 0, "", nil
		},
	})
	if err == nil {
		t.Error("RunProfile accepted an unconfigured capability without error")
	}
}

// TestRunProfile_RunErrorIsPropagated: if the command cannot be launched (not
// an exit code, but a run failure), the error propagates.
func TestRunProfile_RunErrorIsPropagated(t *testing.T) {
	cfg := cfgWithCapability("test", config.CapabilityConfig{Command: "go test ./..."})
	_, err := RunProfile("standard", RunOptions{
		Cfg: cfg,
		Run: func(command string) (int, string, error) {
			return 0, "", errors.New("missing binary")
		},
	})
	if err == nil {
		t.Error("RunProfile accepted a run error without propagating it")
	}
}

// --- CRITICAL 1: the delegated path can never fail, and that must be
// explicit, not a coincidence of Exit staying at its zero value. ---

// TestFailed_DelegatedRunNeverFailsByExplicitDesign: a delegated run with
// Exit != 0 (deliberately simulated: the real delegated path never sets
// Exit, but if it ever did, this must still not fail) and a REAL capability
// (not the zero value that would come from a map without the "delegated" key)
// must not be considered failed. If Failed depended on the zero value of Exit
// to "never fail", this Exit=1 would betray it.
func TestFailed_DelegatedRunNeverFailsByExplicitDesign(t *testing.T) {
	run := ValidationRun{Capability: delegatedCapability, Exit: 1, Output: "this simulates a failure"}
	realCapability := config.CapabilityConfig{FailsWhen: config.FailsWhenExitCode}
	if Failed(run, realCapability) {
		t.Error("Failed considered a delegated run with Exit != 0 failed: the delegated path must never fail by explicit design, not by the coincidence of Exit's zero value")
	}
}

// TestFindings_DelegatedRunExcludedEvenWithNonZeroExit: same case as above
// but through Findings, with a REAL capability deliberately registered under
// the "delegated" key in the map (to rule out the behavior depending on that
// key not existing and returning the zero value of config.CapabilityConfig{}).
func TestFindings_DelegatedRunExcludedEvenWithNonZeroExit(t *testing.T) {
	runs := []ValidationRun{
		{Capability: delegatedCapability, Exit: 1, Output: "simulated failure"},
	}
	capabilities := map[string]config.CapabilityConfig{
		delegatedCapability: {FailsWhen: config.FailsWhenExitCode},
	}
	findings := Findings(runs, capabilities)
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, expected 0: the delegated path never produces findings, by design", findings)
	}
}

// TestFindings_DelegatedProfileWithoutFindings: real end-to-end use case, the
// same one that already passed before the fix (due to the bug): a delegated
// profile with the agent reporting "tested: command-x" produces no Finding.
// Kept as a regression now that the behavior is explicit.
func TestFindings_DelegatedProfileWithoutFindings(t *testing.T) {
	agent := &fakeAgent{output: "tested: command-x"}
	runs, err := RunProfile("standard", RunOptions{
		Cfg:   config.Config{},
		Agent: agent,
	})
	if err != nil {
		t.Fatalf("RunProfile failed: %v", err)
	}
	findings := Findings(runs, nil)
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, expected 0 for a delegated profile", findings)
	}
}
