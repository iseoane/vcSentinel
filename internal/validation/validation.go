// Package validation runs validation profiles (T1.3): user-configurable
// capabilities (config.ValidationConfig, T1.2), grouped into profiles, with an
// optional variant scoped to a subset of packages. For the new
// capabilities/profiles model it is the successor of the dual verification in
// internal/ops.Verificar; internal/ops stays intact in this task (F2 will
// bring the complete finding v2).
package validation

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/agentshell"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/graph"
)

// Scope of a ValidationRun: full (unscoped command) or partial
// (scoped_command narrowed to the affected packages).
const (
	ScopeFull    = "full"
	ScopePartial = "partial"
)

// delegatedCapability identifies, within ValidationRun.Capability, the runs
// that come from the tested contract delegated to the agent (profile with no
// capabilities configured), not from a real yml capability.
const delegatedCapability = "delegated"

// packagesMarker is the literal marker scoped_command must contain; it
// matches the one config.CapabilityConfig.ScopedCommand documents.
const packagesMarker = "{packages}"

// ValidationRun is the result of running a capability. Exact shape requested
// by the T1.3 card: the evidence of a finding comes from here.
type ValidationRun struct {
	Capability  string
	Command     string
	Scope       string // full | partial
	ScopeReason string
	Exit        int
	DurationMs  int64
	Output      string // trimmed, for the finding's evidence
}

// Finding is the minimal validation finding of this phase: the complete v2
// type (structured evidence, typed severity) arrives in F2. Here it is enough
// to never invent a PASS and to show the real output of the failed command.
type Finding struct {
	Source     string // fixed "validation"
	Severity   string // fixed "CRITICAL": no severity grades in this phase
	Capability string
	Command    string
	Evidence   string
}

// CommandRunner executes a command and returns its exit code and the combined
// output (stdout+stderr): fails_when=output_not_empty needs the real output,
// not just the exit code (the "gofmt -l ." case, which exits 0 with files
// listed).
type CommandRunner func(command string) (exit int, output string, err error)

// RunOptions configures RunProfile. Run and Agent are injectable (same seam
// as internal/ops.OpcionesVerificar) so tests can run without launching real
// processes or depending on a real agent.
type RunOptions struct {
	Worktree string
	Cfg      config.Config
	// ProviderGraph is invoked only on the frozen snapshot. Nil keeps full
	// validation, just like an error or an incomplete analysis.
	ProviderGraph func(snapshot, treeOID string) graph.GraphProvider
	authorization graph.ScopeAuthorization
	// Run (nil = real shell) runs the command and returns exit + output.
	Run CommandRunner
	// Agent is the delegation channel (tested contract), used only when the
	// profile has no capabilities configured; nil = not delegable.
	Agent agentadapter.PromptAdapter
}

// RunProfile runs the capabilities of the profile in order. Only a valid
// graph.ScopeAuthorization can select ScopedCommand; any other state uses the
// full Command. If the profile has no capabilities (yml without configured
// capabilities), it delegates to the agent with the tested contract, exactly
// as internal/ops.Verificar did when there were no lint/test/build commands:
// it is the same configuration gap, now expressed as an empty profile instead
// of loose command lists.
func RunProfile(profile string, opts RunOptions) ([]ValidationRun, error) {
	names := opts.Cfg.Validation.Profiles[profile]
	if len(names) == 0 {
		return delegateWithoutCapabilities(opts)
	}

	run := opts.Run
	if run == nil {
		run = func(command string) (int, string, error) {
			return agentshell.Run(opts.Worktree, command)
		}
	}

	runs := make([]ValidationRun, 0, len(names))
	for _, name := range names {
		capability, ok := opts.Cfg.Validation.Capabilities[name]
		if !ok {
			return runs, fmt.Errorf("profile %q references capability %q, which is not configured", profile, name)
		}
		command, scopeLabel, reason := resolveCommand(capability, opts.authorization)

		start := time.Now()
		exit, output, err := run(command)
		duration := time.Since(start).Milliseconds()
		if err != nil {
			return runs, fmt.Errorf("could not run %q (capability %q): %w", command, name, err)
		}
		runs = append(runs, ValidationRun{
			Capability:  name,
			Command:     command,
			Scope:       scopeLabel,
			ScopeReason: reason,
			Exit:        exit,
			DurationMs:  duration,
			Output:      output,
		})
	}
	return runs, nil
}

// validScopeElement is the whitelist of safe characters for a scope element
// (Go package/path name): letters, digits, /, ., _, -. Unlike
// Command/ScopedCommand (literals from the user's vassentinel.yml, trusted by
// design, see agentshell.Run), the scope is computed at runtime and
// interpolated unquoted into a command that later runs through sh -c/cmd /c:
// any other character (;, `, $, " etc.) would allow injecting an arbitrary
// command. Trying to "escape" the string for the target shell is not
// attempted (fragile and different between cmd/sh): rejecting with an error
// whatever does not fit the whitelist is simpler and safer.
var validScopeElement = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)

// resolveCommand decides which variant to use: scoped only with an opaque
// authorization from the graph AND declared support; otherwise the exact full
// command. Before interpolating the scope into scoped_command, it validates
// each element against validScopeElement; if any does not fit, it keeps the
// exact Command (it never builds a command from an unvalidated element).
func resolveCommand(capability config.CapabilityConfig, authorization graph.ScopeAuthorization) (command, label, reason string) {
	if !capability.SupportsScope {
		return capability.Command, ScopeFull, "full command: the capability does not declare supports_scope"
	}
	if !authorization.Authorized() {
		return capability.Command, ScopeFull, "full command: graph absent, errored, incomplete or unauthorized"
	}
	if !strings.Contains(capability.ScopedCommand, packagesMarker) {
		return capability.Command, ScopeFull, "full command: invalid scoped_command"
	}
	scope := authorization.Packages()
	for _, element := range scope {
		if !validScopeElement.MatchString(element) {
			return capability.Command, ScopeFull, fmt.Sprintf("full command: authorized package %q is not safe for shell interpolation", element)
		}
	}
	scoped := strings.ReplaceAll(capability.ScopedCommand, packagesMarker, strings.Join(scope, " "))
	return scoped, ScopePartial, fmt.Sprintf("complete graph authorized %d affected package(s): %s", len(scope), strings.Join(authorization.Explanation(), "; "))
}

// Failed determines whether a ValidationRun counts as failed according to
// fails_when (config.FailsWhenExitCode is the default, including the empty
// case).
//
// A delegated run (Capability == delegatedCapability) NEVER fails through
// this path, and that decision is explicit, not a side effect of Exit staying
// at its zero value: delegatedCapability is an internal marker, not a real
// yml capability, so there is no fails_when to start from for it. The agent's
// tested contract is narrative evidence (a warning, never a block, same
// criterion as internal/ops.Verificar), never a locally verified exit code;
// treating it as one would amount to inventing a PASS or a FAIL at
// convenience. That is why we short-circuit here before looking at
// capability.FailsWhen, instead of trusting Exit to be 0.
func Failed(run ValidationRun, capability config.CapabilityConfig) bool {
	if run.Capability == delegatedCapability {
		return false
	}
	if capability.FailsWhen == config.FailsWhenOutputNotEmpty {
		return strings.TrimSpace(run.Output) != ""
	}
	return run.Exit != 0
}

// Findings translates the failed ValidationRuns (according to Failed) into
// minimal findings: fixed CRITICAL severity (no grades in this phase) and the
// real output as evidence, never an invented PASS.
//
// Delegated runs are excluded here, BEFORE looking at Failed:
// delegatedCapability is not a real "capabilities" key (it comes from the
// agent's tested contract, not from the yml), so there is no fails_when to
// apply to them. They are informational evidence of the tested contract, not
// local verification, and by explicit design they can never produce a Finding
// through this path (see the Failed comment).
func Findings(runs []ValidationRun, capabilities map[string]config.CapabilityConfig) []Finding {
	var findings []Finding
	for _, run := range runs {
		if run.Capability == delegatedCapability {
			continue
		}
		if !Failed(run, capabilities[run.Capability]) {
			continue
		}
		findings = append(findings, Finding{
			Source:     "validation",
			Severity:   "CRITICAL",
			Capability: run.Capability,
			Command:    run.Command,
			Evidence:   run.Output,
		})
	}
	return findings
}

// delegateWithoutCapabilities is the fallback when the profile has no
// capabilities configured: it delegates to the agent with the same tested
// contract internal/ops.Verificar already used. Validation never blocks:
// without an agent, or if the agent does not respond or breaks the contract,
// it degrades to an empty list without error (the same "warning, never block"
// criterion as internal/ops).
func delegateWithoutCapabilities(opts RunOptions) ([]ValidationRun, error) {
	if opts.Agent == nil {
		return nil, nil
	}
	output, err := opts.Agent.RunPrompt(delegationPrompt())
	if err != nil {
		return nil, nil
	}
	tested, err := agentshell.ParseTestedContract(output)
	if err != nil {
		return nil, nil
	}
	runs := make([]ValidationRun, 0, len(tested))
	for _, command := range tested {
		runs = append(runs, ValidationRun{
			Capability:  delegatedCapability,
			Command:     command,
			Scope:       ScopeFull,
			ScopeReason: "delegated to the agent (tested contract): the profile has no capabilities configured",
		})
	}
	return runs, nil
}

// delegationPrompt is the same tested contract as internal/ops.Verificar:
// free shell, one final "tested: <command>; ..." line, or unavailable.
func delegationPrompt() string {
	return "You are the validation step of VAS Sentinel.\n" +
		"You have a free shell: discover the project's tests (Makefile, go.mod, scripts, language conventions) and run them.\n" +
		"Return ONLY one final line with the tested contract, with the executed commands separated by ;:\n" +
		"tested: <command>; <command>\n" +
		"If you cannot run the tests, return ONLY: unavailable"
}

// The parsing of the tested contract and the combined-output shell execution
// live in internal/agentshell: they are the same logic, byte for byte in the
// parsing case, that internal/ops.Verificar already used. Before this
// extraction each package had its own copy; now both import
// internal/agentshell (with no dependency on ops or validation, so no cycle
// is created).
