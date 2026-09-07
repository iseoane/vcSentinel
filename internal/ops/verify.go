package ops

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentshell"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// Verification modes (guide §12.3): deterministic when commands are
// configured; delegated / skipped / configure are the outcomes of the
// notice offered when there is no test_commands in the yml.
//
// The VALUES below are historical wire strings kept for compatibility:
// internal/review/renderer.go switches on them and the pr-verify event
// detail stores them, so they must never change.
const (
	ModeDeterministic = "determinista"
	ModeDelegated     = "delegado"
	ModeSkipped       = "omitido"
	ModeConfigure     = "configurar"
)

// errAgentUnresponsive is the degradation reason when the verification
// agent does not respond: a notice, never a blocker (declared risk of the
// guide).
var errAgentUnresponsive = errors.New("verification agent did not respond")

// errRelativeGitDir signals a non-empty but relative GitDir: a caller
// wiring error. Unexported (same convention as errAgentUnresponsive): the
// package uses errors.Is in its own tests to tell it apart from a domain
// failure (a command that could not run, an event that could not be
// written); an external caller only sees the generic wrapped error.
var errRelativeGitDir = errors.New("GitDir is not an absolute path")

// CommandResult is the real exit code of a configured command.
type CommandResult struct {
	Command string
	Exit    int
}

// VerificationResult describes what ran (or why not): the PR template can
// only show real evidence, never an invented PASS.
type VerificationResult struct {
	Mode     string // wire value: "determinista" | "delegado" | "omitido" | "configurar"
	Commands []CommandResult
	// Tested is EVIDENCE shown in the template, never a list to run
	// again: the agent already launched those commands with a free shell.
	Tested []string
	Reason string // wire value: "no_configurado" | "omitido" | "agente_no_respondio" | ...
}

// VerifyOptions configures verification. Run and Questionr are injectable
// so that tests can run without launching real commands or reading stdin.
type VerifyOptions struct {
	Worktree string
	// GitDir, when not empty, records the pr-verify event (§13) at the
	// end: detail {cmd, exit} per command, tested, or reason. It must be
	// an absolute path (git.GetGitDir guarantees that); a non-empty
	// relative path makes Verify return an error instead of skipping the
	// record silently.
	GitDir string
	Cfg    config.Config
	// Run (nil = real shell) returns the exit code of a command.
	Run func(command string) (int, error)
	// Agent is the delegation path (tested contract); nil = cannot delegate.
	Agent agentadapter.PromptAdapter
	// Questionr asks the notice with a choice; nil = non-interactive → skip.
	Questionr func(notice string) (string, error)
}

// Verify implements the dual verification: path 1 deterministic when
// lint/test/build commands are configured; otherwise a notice with a
// choice (configure | skip | delegate) and delegation with a free shell if
// chosen. With an absolute GitDir, it records the pr-verify event (§13)
// after the computation; with a non-empty relative GitDir, it returns an
// error (never silently: the guide for this package is that a verification
// failure is seen, not lost).
func Verify(opts VerifyOptions) (VerificationResult, error) {
	// record decides, in a single place, whether GitDir takes part in the
	// event record: empty means "do not record" (pre-existing behavior),
	// any other value must be absolute or it is a caller wiring error.
	// Checked BEFORE verifyInternal (which does have effects: real shell
	// commands, interactive notice, agent) so that a badly wired caller
	// fails fast, without paying that full verification before learning
	// about its own error.
	record := opts.GitDir != ""
	if record && !filepath.IsAbs(opts.GitDir) {
		return VerificationResult{}, fmt.Errorf("%q: %w", opts.GitDir, errRelativeGitDir)
	}
	result, err := verifyInternal(opts)
	if err != nil || !record {
		return result, err
	}
	if err := recordPrVerifyEvent(opts.GitDir, opts.Worktree, result); err != nil {
		return result, fmt.Errorf("verification in mode %s, but the pr-verify event could not be recorded: %w", result.Mode, err)
	}
	return result, nil
}

// verifyInternal computes the result without side effects (no event).
func verifyInternal(opts VerifyOptions) (VerificationResult, error) {
	commands := append([]string{}, opts.Cfg.LintCommands...)
	commands = append(commands, opts.Cfg.TestCommands...)
	commands = append(commands, opts.Cfg.BuildCommands...)

	if len(commands) > 0 {
		run := opts.Run
		if run == nil {
			run = func(command string) (int, error) {
				return runShell(opts.Worktree, command)
			}
		}
		result := VerificationResult{Mode: ModeDeterministic}
		for _, command := range commands {
			exit, err := run(command)
			if err != nil {
				return result, fmt.Errorf("could not run %q: %w", command, err)
			}
			result.Commands = append(result.Commands, CommandResult{Command: command, Exit: exit})
		}
		return result, nil
	}

	// No commands configured: notice with a choice (guide §12.3).
	if opts.Questionr == nil {
		return VerificationResult{Mode: ModeSkipped, Reason: "no_configurado"}, nil
	}
	ciDetected := false
	if opts.Worktree != "" {
		ciDetected = git.DetectCI(opts.Worktree)
	}
	answer, err := opts.Questionr(verificationNoticeText(ciDetected))
	if err != nil {
		// Verification never blocks: if the notice could not be read, it
		// degrades to skipped with its own reason and no error.
		return VerificationResult{Mode: ModeSkipped, Reason: "aviso_no_respondio"}, nil
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "configure", "c":
		return VerificationResult{Mode: ModeConfigure}, nil
	case "skip", "s":
		return VerificationResult{Mode: ModeSkipped, Reason: "omitido"}, nil
	case "delegate", "d":
		if opts.Agent == nil {
			return VerificationResult{Mode: ModeSkipped, Reason: "sin_agente"}, nil
		}
		output, err := opts.Agent.RunPrompt(verificationPrompt())
		if err != nil {
			return VerificationResult{Mode: ModeSkipped, Reason: "agente_no_respondio"}, nil
		}
		tested, err := parseTestedContract(output)
		if err != nil {
			// Distinguish the agent's inability (unavailable) from a
			// protocol violation: different reasons for the event.
			if strings.Contains(strings.ToLower(output), "unavailable") {
				return VerificationResult{Mode: ModeSkipped, Reason: "agente_unavailable"}, nil
			}
			return VerificationResult{Mode: ModeSkipped, Reason: "contrato_invalido"}, nil
		}
		return VerificationResult{Mode: ModeDelegated, Tested: tested}, nil
	}
	return VerificationResult{Mode: ModeSkipped, Reason: "eleccion_invalida"}, nil
}

// verificationNoticeText describes the situation and the three choices; it
// distinguishes whether CI was detected (external verification will cover
// the PR).
func verificationNoticeText(ciDetected bool) string {
	var b strings.Builder
	b.WriteString("No verification commands are configured (lint_commands/test_commands/build_commands in vassentinel.yml).\n")
	if ciDetected {
		b.WriteString("CI was detected in the repository: the PR will have external automatic verification.\n")
	} else {
		b.WriteString("No CI was detected: this PR will not have any automatic verification.\n")
	}
	b.WriteString("Reply: configure (stop and edit the yml), skip (continue without running tests) or delegate (run the tests with the agent).")
	return b.String()
}

// verificationPrompt is the prompt DISTINCT from the audit one: the audit
// forbids tools (so that the agent does not hang on builds/tests); this
// delegation needs a free shell and is a dedicated step after the review.
// It returns the tested contract with the executed commands.
func verificationPrompt() string {
	return "You are the verification step of VAS Sentinel.\n" +
		"You have a free shell: discover the project's tests (Makefile, go.mod, scripts, language conventions) and run them.\n" +
		"Return ONLY one final line with the tested contract, with the executed commands separated by ;:\n" +
		"tested: <command>; <command>\n" +
		"If you cannot run the tests, return ONLY: unavailable"
}

// parseTestedContract extracts the commands from the "tested: ..." line of
// the agent's output and rejects unavailable / a missing contract. It
// delegates to internal/agentshell (shared with internal/validation) to
// avoid duplicating the parsing; this unexported name is kept because this
// package's tests call it directly.
func parseTestedContract(output string) ([]string, error) {
	return agentshell.ParseTestedContract(output)
}

// runShell launches a command through the system shell in the worktree and
// returns its exit code (0 on success; -1 when the failure was in the
// execution itself, not in the command). It delegates to
// internal/agentshell.Run (shared with internal/validation) and discards
// the combined output: this package only needs the exit code for the
// deterministic mode.
func runShell(worktree, command string) (int, error) {
	exit, _, err := agentshell.Run(worktree, command)
	return exit, err
}

// recordPrVerifyEvent persists the pr-verify event (§5) with the schema
// detail: per command {cmd, exit}; or the tested contract; or the reason.
func recordPrVerifyEvent(gitDir, worktree string, result VerificationResult) error {
	switch result.Mode {
	case ModeDeterministic:
		commands := make([]map[string]any, 0, len(result.Commands))
		worst := 0
		for _, c := range result.Commands {
			commands = append(commands, map[string]any{"cmd": c.Command, "exit": c.Exit})
			if c.Exit > worst {
				worst = c.Exit
			}
		}
		return RecordEvent(gitDir, "pr-verify", worst, nil, EventDetail{"comandos": commands}, worktree)
	case ModeDelegated:
		return RecordEvent(gitDir, "pr-verify", 0, nil, EventDetail{"tested": result.Tested}, worktree)
	default:
		return RecordEvent(gitDir, "pr-verify", 0, nil, EventDetail{"reason": result.Reason}, worktree)
	}
}
