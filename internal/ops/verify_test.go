package ops

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

// Fictional sentinels for the tests of the error branches.
var (
	errMissingBinary = errors.New("verification binary missing")
	errNoAnswer      = errors.New("no answer from the notice")
)

// fakeVerifyAgent implements PromptAdapter for the delegation tests: it
// returns the programmed output.
type fakeVerifyAgent struct {
	output string
	err    error
}

func (a *fakeVerifyAgent) RunPrompt(prompt string) (string, error) {
	return a.output, a.err
}

// TestParseTestedContract: extracts the commands from the tested contract
// and rejects unavailable / a missing contract.
func TestParseTestedContract(t *testing.T) {
	commands, err := parseTestedContract("Done.\ntested: go test ./...; go build ./...")
	if err != nil {
		t.Fatalf("parseTestedContract failed: %v", err)
	}
	if len(commands) != 2 || commands[0] != "go test ./..." || commands[1] != "go build ./..." {
		t.Errorf("commands = %#v, expected [go test ./... go build ./...]", commands)
	}

	if _, err := parseTestedContract("I could not run anything\nunavailable"); err == nil {
		t.Error("parseTestedContract accepted unavailable")
	}
	if _, err := parseTestedContract("answer without contract"); err == nil {
		t.Error("parseTestedContract accepted output without a contract")
	}
}

// TestVerifyDeterministic: with commands configured, all of them run in
// order and the real exit codes are collected (injected path).
func TestVerifyDeterministic(t *testing.T) {
	executed := []string{}
	result, err := Verify(VerifyOptions{
		Cfg: config.Config{
			LintCommands:  []string{"go vet ./..."},
			TestCommands:  []string{"go test ./..."},
			BuildCommands: []string{"go build ./..."},
		},
		Run: func(command string) (int, error) {
			executed = append(executed, command)
			if strings.Contains(command, "test") {
				return 1, nil
			}
			return 0, nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.Mode != ModeDeterministic {
		t.Errorf("Mode = %q, expected %q", result.Mode, ModeDeterministic)
	}
	if len(executed) != 3 {
		t.Fatalf("%d commands ran, expected 3: %v", len(executed), executed)
	}
	if result.Commands[0].Exit != 0 || result.Commands[1].Exit != 1 || result.Commands[2].Exit != 0 {
		t.Errorf("exit codes = %+v, expected [0 1 0]", result.Commands)
	}
}

// TestVerifyDeterministicRunError: if a command cannot be launched (a
// missing binary), verification fails with the error, not an exit code.
func TestVerifyDeterministicRunError(t *testing.T) {
	_, err := Verify(VerifyOptions{
		Cfg: config.Config{TestCommands: []string{"go test ./..."}},
		Run: func(command string) (int, error) {
			return 0, errMissingBinary
		},
	})
	if err == nil {
		t.Error("Verify accepted a run error without propagating it")
	}
}

// TestVerifyNoConfigDelegateNoAgent: choosing delegate without an agent
// degrades to skipped, never blocks.
func TestVerifyNoConfigDelegateNoAgent(t *testing.T) {
	result, err := Verify(VerifyOptions{
		Questionr: func(notice string) (string, error) {
			return "delegate", nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.Mode != ModeSkipped || result.Reason != "sin_agente" {
		t.Errorf("Mode = %q Reason = %q, expected skipped/sin_agente", result.Mode, result.Reason)
	}
}

// TestVerifyQuestionrErrorDoesNotBlock: if the notice cannot be read, it
// degrades to skipped with its own reason and WITHOUT an error (the
// verification never blocks the flow).
func TestVerifyQuestionrErrorDoesNotBlock(t *testing.T) {
	result, err := Verify(VerifyOptions{
		Questionr: func(notice string) (string, error) {
			return "", errNoAnswer
		},
	})
	if err != nil {
		t.Fatalf("Verify must not propagate the notice error: %v", err)
	}
	if result.Mode != ModeSkipped || result.Reason != "aviso_no_respondio" {
		t.Errorf("Mode = %q Reason = %q, expected skipped/aviso_no_respondio", result.Mode, result.Reason)
	}
}

// TestVerifyAgentFailureDoesNotBlock: an agent that does not respond (or
// returns unavailable) degrades to skipped with a reason: a notice, never
// a blocker.
func TestVerifyAgentFailureDoesNotBlock(t *testing.T) {
	cases := map[string]struct {
		agent  *fakeVerifyAgent
		reason string
	}{
		"unavailable":      {&fakeVerifyAgent{output: "unavailable"}, "agente_unavailable"},
		"invalid contract": {&fakeVerifyAgent{output: "answer without contract"}, "contrato_invalido"},
		"error":            {&fakeVerifyAgent{output: "", err: errAgentUnresponsive}, "agente_no_respondio"},
	}
	for name, c := range cases {
		result, err := Verify(VerifyOptions{
			Agent: c.agent,
			Questionr: func(notice string) (string, error) {
				return "delegate", nil
			},
		})
		if err != nil {
			t.Fatalf("%s: Verify failed: %v", name, err)
		}
		if result.Mode != ModeSkipped || result.Reason != c.reason {
			t.Errorf("%s: Mode = %q Reason = %q, expected skipped/%s", name, result.Mode, result.Reason, c.reason)
		}
	}
}

// TestVerifyNoConfigDelegate: without configured commands, the notice
// offers delegate and the agent returns the tested contract.
func TestVerifyNoConfigDelegate(t *testing.T) {
	agent := &fakeVerifyAgent{output: "all green\ntested: make test; go vet ./..."}
	result, err := Verify(VerifyOptions{
		Agent: agent,
		Questionr: func(notice string) (string, error) {
			if !strings.Contains(notice, "test_commands") {
				t.Errorf("the notice does not mention test_commands: %q", notice)
			}
			return "delegate", nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.Mode != ModeDelegated {
		t.Errorf("Mode = %q, expected %q", result.Mode, ModeDelegated)
	}
	if len(result.Tested) != 2 || result.Tested[0] != "make test" {
		t.Errorf("Tested = %#v", result.Tested)
	}
}

// TestVerifyNoConfigSkip: the skip choice runs nothing and does not
// delegate.
func TestVerifyNoConfigSkip(t *testing.T) {
	result, err := Verify(VerifyOptions{
		Agent: &fakeVerifyAgent{output: "never called"},
		Questionr: func(notice string) (string, error) {
			return "skip", nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.Mode != ModeSkipped {
		t.Errorf("Mode = %q, expected %q", result.Mode, ModeSkipped)
	}
	if len(result.Tested) != 0 || len(result.Commands) != 0 {
		t.Errorf("skip must not run anything: %+v", result)
	}
}

// TestVerifyRelativeGitDirReturnsError: a non-empty but relative GitDir is
// a badly wired caller (contract: git.GetGitDir always returns an
// absolute path), and it must fail loudly and fast (before paying
// verifyInternal, which does have real effects) instead of skipping the
// record of the pr-verify event silently.
func TestVerifyRelativeGitDirReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())
	// Without Questionr/Run/Agent: verifyInternal would silently degrade
	// to ModeSkipped if it ever ran. The guard must reject before reaching
	// that point, so the only valid outcome is the wiring error.
	_, err := Verify(VerifyOptions{GitDir: "relative-gitdir"})

	// Unconditional regression guard, BEFORE the t.Fatal below: if the
	// fail-fast guard ever stopped checking filepath.IsAbs, err would be
	// nil and the t.Fatal would cut the test short before reaching here,
	// leaving this check unexecuted in exactly the scenario it must watch.
	// t.Chdir isolates the cwd in a disposable directory: if the regression
	// ever wrote the event, it lands there and not in the repository tree.
	events, eventsErr := RecentEvents("relative-gitdir", 1)
	if eventsErr != nil {
		t.Fatalf("RecentEvents failed: %v", eventsErr)
	}
	if len(events) != 0 {
		t.Errorf("no event must be recorded with a relative GitDir: %+v", events)
	}

	if err == nil {
		t.Fatal("Verify with a relative GitDir must return an error, not skip silently")
	}
	if !errors.Is(err, errRelativeGitDir) {
		t.Errorf("the error must wrap errRelativeGitDir (distinguishable with errors.Is), got: %v", err)
	}
	if !strings.Contains(err.Error(), "relative-gitdir") {
		t.Errorf("the error must quote the received path, got: %v", err)
	}
}

// TestVerifyNoConfigConfigure: the configure choice returns the mode so
// that the caller stops and edits the yml.
func TestVerifyNoConfigConfigure(t *testing.T) {
	result, err := Verify(VerifyOptions{
		Questionr: func(notice string) (string, error) {
			return "configure", nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.Mode != ModeConfigure {
		t.Errorf("Mode = %q, expected %q", result.Mode, ModeConfigure)
	}
}

// TestVerificationNoticeText: mentions the choice and distinguishes
// detected CI.
func TestVerificationNoticeText(t *testing.T) {
	noCI := verificationNoticeText(false)
	if !strings.Contains(noCI, "CI") || !strings.Contains(noCI, "delegate") {
		t.Errorf("notice without CI is incomplete: %q", noCI)
	}
	withCI := verificationNoticeText(true)
	if !strings.Contains(withCI, "detected") {
		t.Errorf("notice with CI does not mention it: %q", withCI)
	}
}

// TestVerifyReadsTheValidationProfileThisProjectDeclares is item 2: this
// repository configures verification under validation.capabilities, not under
// the legacy lint/test/build lists, and translation only ever runs from the
// legacy keys INTO capabilities. The command list therefore came out empty and
// the PR body announced that no verification was configured — minutes after
// the gate had run those very commands and reported PASS.
func TestVerifyReadsTheValidationProfileThisProjectDeclares(t *testing.T) {
	executed := []string{}
	result, err := Verify(VerifyOptions{
		Cfg: config.Config{
			Validation: config.ValidationConfig{
				Capabilities: map[string]config.CapabilityConfig{
					"format":    {Command: "gofmt -l ."},
					"lint":      {Command: "go vet ./..."},
					"build":     {Command: "go build ./..."},
					"unit_test": {Command: "go test ./..."},
				},
				Profiles: map[string][]string{"standard": {"format", "lint", "build", "unit_test"}},
			},
		},
		Run: func(command string) (int, error) {
			executed = append(executed, command)
			return 0, nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if result.Mode != ModeDeterministic {
		t.Fatalf("Mode = %q, expected %q: a declared profile is configured verification", result.Mode, ModeDeterministic)
	}
	if len(executed) != 4 {
		t.Fatalf("%d commands ran, expected the profile's 4: %v", len(executed), executed)
	}
}

// TestVerifyKeepsLegacyCommandListsAuthoritative guards the compatibility
// direction: a configuration that still declares the legacy lists must run
// exactly those, in that order, and must not gain the profile's commands.
func TestVerifyKeepsLegacyCommandListsAuthoritative(t *testing.T) {
	executed := []string{}
	_, err := Verify(VerifyOptions{
		Cfg: config.Config{
			LintCommands: []string{"legacy lint"},
			Validation: config.ValidationConfig{
				Capabilities: map[string]config.CapabilityConfig{"lint": {Command: "profile lint"}},
				Profiles:     map[string][]string{"standard": {"lint"}},
			},
		},
		Run: func(command string) (int, error) {
			executed = append(executed, command)
			return 0, nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if len(executed) != 1 || executed[0] != "legacy lint" {
		t.Fatalf("executed = %v, expected only the legacy list", executed)
	}
}

// TestVerifyInventsNothingWithoutADeclaredProfile keeps the fallback honest: an
// absent profile, or capabilities carrying no command, must leave verification
// unconfigured rather than report commands nobody declared.
func TestVerifyInventsNothingWithoutADeclaredProfile(t *testing.T) {
	for name, validation := range map[string]config.ValidationConfig{
		"no profile at all": {},
		"profile naming an undeclared capability": {
			Profiles: map[string][]string{"standard": {"missing"}},
		},
		"capability with an empty command": {
			Capabilities: map[string]config.CapabilityConfig{"lint": {}},
			Profiles:     map[string][]string{"standard": {"lint"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			executed := []string{}
			result, err := Verify(VerifyOptions{
				Cfg: config.Config{Validation: validation},
				Run: func(command string) (int, error) {
					executed = append(executed, command)
					return 0, nil
				},
			})
			if err != nil {
				t.Fatalf("Verify failed: %v", err)
			}
			if len(executed) != 0 {
				t.Fatalf("executed %v, expected nothing to run", executed)
			}
			if result.Mode == ModeDeterministic {
				t.Fatalf("Mode = %q: verification must not claim to be configured", result.Mode)
			}
		})
	}
}
