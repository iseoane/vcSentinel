// Package acpadapter drives an ACP-capable agent through the acpx CLI as a
// child process and normalizes its strict NDJSON protocol stream into a
// single result. It keeps every ACP concept inside this package boundary:
// nothing else in the repository learns the wire format.
//
// For terminal protocol results, stopReason determines semantic success,
// cancellation, and timeout. A non-zero child exit remains authoritative for
// the operational process-failure outcome, including after a terminal result;
// cancellation and runtime timeout still take precedence when their contexts
// are the source of termination.
package acpadapter

import (
	"context"
	"fmt"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// DefaultMaxRuntimeSeconds is the --timeout value used when the adapter is
// built without an explicit MaxRuntimeSeconds.
const DefaultMaxRuntimeSeconds = 300

// DefaultLineCapBytes is the generous per-line cap applied when scanning the
// stdout NDJSON stream.
const DefaultLineCapBytes = 1 << 20 // 1 MiB

// defaultLauncher mirrors how operators invoke acpx today: through npx so no
// global install is required.
var defaultLauncher = []string{"npx", "-y", "acpx@latest"}

// Config describes how to launch acpx and which agent it should serve.
type Config struct {
	// Launcher is the command plus base arguments that start acpx. An empty
	// Launcher selects DefaultLauncher (["npx","-y","acpx@latest"]).
	Launcher []string
	// Agent is the acpx agent token (for example "claude"). It is required.
	Agent string
	// Model is the configured model echo. It is reported verbatim when the
	// stream does not expose an observed model; it is never sent to acpx.
	Model string
	// Effort is the configured reasoning-effort echo. It is reported
	// verbatim or empty; it is never fabricated.
	Effort string
	// MaxRuntimeSeconds maps to the acpx --timeout global flag. Zero or
	// negative selects DefaultMaxRuntimeSeconds.
	MaxRuntimeSeconds int
	// MaxOutputBytes caps how many stdout bytes one turn may produce. Zero
	// means unlimited. While draining the stream, crossing the cap stops
	// reading, terminates the child, and classifies the turn as failure
	// ("output budget exceeded"), regardless of what the truncated stream
	// claimed.
	MaxOutputBytes int64
	// ChildEnv holds extra environment entries (KEY=VALUE) appended to the
	// child process environment. Operators use it for credential passthrough;
	// tests use it for the helper-process guard.
	ChildEnv []string
	// Enforcement declares which backend must guarantee any restriction
	// capabilities the run demands (C6 admission, ticket 16). Empty and
	// "none" admit the run as grant-by-design; "claude-sandbox" declares
	// the project's Claude Code native sandbox as containment (unsupported
	// on native Windows). An unknown or platform-unsatisfiable value fails
	// construction before anything can be launched.
	Enforcement string
}

// AcpxAdapter executes prompts through an acpx child process and retains the
// evidence needed by upper layers for hash admission and identity reporting.
type AcpxAdapter struct {
	launcher          []string
	agent             string
	model             string
	effort            string
	maxRuntimeSeconds int
	maxOutputBytes    int64
	childEnv          []string

	mu                 sync.Mutex
	lastObserved       string
	lastObservedEffort string
	observedKnown      bool

	// enforcement is the validated enforcement declaration this adapter was
	// built with (normalized empty -> EnforcementNone at construction).
	// Retaining it keeps the C6 admission verdict visible to every later
	// observer instead of discarding it once construction succeeds.
	enforcement string

	// liveTrees is the registry of currently owned process trees, in start
	// order. A single active slot would let concurrent runs hide each
	// other's live children (and an unconditional clear on one completion
	// would orphan a still-running sibling), so every spawned tree is
	// registered at birth and removed ONLY when its own run completes. The
	// execution controller reads the most recently started entry through
	// OwnedTree at abort time to escalate against the whole tree.
	treeMu    sync.Mutex
	liveTrees []*process.Tree
}

// NewAcpx validates cfg and returns a ready adapter. The agent token must be
// non-empty; everything else has a deterministic default.
func NewAcpx(cfg Config) (*AcpxAdapter, error) {
	if strings.TrimSpace(cfg.Agent) == "" {
		return nil, fmt.Errorf("acpadapter: agent token is required")
	}
	if err := validateEnforcementOnHost(cfg.Enforcement); err != nil {
		return nil, err
	}
	if cfg.Enforcement == EnforcementClaudeSandbox && strings.TrimSpace(cfg.Agent) != "claude" {
		return nil, fmt.Errorf("acpadapter: enforcement %q applies only to the claude agent token (the sandbox belongs to Claude Code settings), got agent %q", EnforcementClaudeSandbox, cfg.Agent)
	}
	launcher := cfg.Launcher
	if len(launcher) == 0 {
		launcher = defaultLauncher
	}
	for i, part := range launcher {
		if strings.TrimSpace(part) == "" {
			return nil, fmt.Errorf("acpadapter: launcher entry %d is empty", i)
		}
	}
	maxRuntime := cfg.MaxRuntimeSeconds
	if maxRuntime <= 0 {
		maxRuntime = DefaultMaxRuntimeSeconds
	}
	// Normalize the validated declaration (empty -> EnforcementNone) and
	// retain it: the admission verdict is protocol evidence and must stay
	// observable by every result and diagnostic produced later.
	enforcement := cfg.Enforcement
	if enforcement == "" {
		enforcement = EnforcementNone
	}
	return &AcpxAdapter{
		launcher:          append([]string(nil), launcher...),
		agent:             cfg.Agent,
		model:             cfg.Model,
		effort:            cfg.Effort,
		maxRuntimeSeconds: maxRuntime,
		maxOutputBytes:    cfg.MaxOutputBytes,
		childEnv:          append([]string(nil), cfg.ChildEnv...),
		enforcement:       enforcement,
	}, nil
}

// EnforcementDeclaration reports the validated enforcement declaration this
// adapter carries: the configured value verbatim, or EnforcementNone when
// construction admitted the run as grant-by-design. It lets bridges, logs,
// and diagnostics state which backend was declared to contain each run.
func (a *AcpxAdapter) EnforcementDeclaration() string {
	return a.enforcement
}

// Result is the normalized observation of one acpx turn. RawStream is kept
// verbatim so upper layers can hash-admit the evidence later; this package
// only retains it.
type Result struct {
	// Output is the assistant answer assembled from every
	// agent_message_chunk text in stream order.
	Output string
	// RawStream is the verbatim stdout bytes of the child process. When an
	// output-budget breach ended the turn early, it holds only the drained
	// prefix up to the breach.
	RawStream string
	// Violations counts framing violations skipped while parsing:
	// non-JSON lines and lines over the per-line cap. They are never fatal.
	Violations int
	// Agent is the configured ACP agent token that produced this result.
	Agent string
	// ObservedModel is the model captured from the initialize result
	// configOptions when the backend exposes it. Empty otherwise.
	ObservedModel string
	// RequestedModel is the configured request declaration. It remains
	// separate from ObservedModel when the provider omits wire identity.
	RequestedModel string
	// ObservedEffort is captured only when ACP exposes an effort value on the
	// wire (typically via configOptions). It is never filled from the request.
	ObservedEffort string
	// RequestedEffort is the configured declaration supplied to the adapter.
	// It remains separate from ObservedEffort when the provider omits it.
	RequestedEffort string
	// StopReason is the terminal result stopReason verbatim, or empty when
	// no terminal result arrived.
	StopReason string
	// UsageJSON is the raw usage member of the terminal result, verbatim,
	// or empty when absent.
	UsageJSON string
	// Usage contains only recognized numeric token fields from UsageJSON.
	Usage *Usage
	// Enforcement is the validated enforcement declaration of the adapter
	// that produced this turn (EnforcementNone when nothing was declared).
	Enforcement string
	// Turns is the observed model-turn count, when the provider's wire
	// format exposes one comparable to OpenCode's --format json
	// step_finish-per-turn stream (its own configured Steps agent budget).
	// Nil means the provider reports no turn count at all — never a bare
	// zero, which would be indistinguishable from a real "zero turns"
	// observation. The real ACP/acpx stream parsed by ParseStream carries no
	// such per-turn count, so it stays nil there; only agentadapter's
	// OpenCode review path populates it today.
	Turns *int
}

// ResultObservation is the provider-local normalized evidence shape. Upper
// layers map it into their own provider-neutral contract to avoid package
// cycles.
type ResultObservation struct {
	Agent           string
	Model           string
	RequestedModel  string
	Effort          string
	RequestedEffort string
	StopReason      string
	Enforcement     string
	Usage           *Usage
	Turns           *int
}

func (r Result) AdapterObservation() ResultObservation {
	return ResultObservation{
		Agent: r.Agent, Model: r.ObservedModel,
		RequestedModel: r.RequestedModel, Effort: r.ObservedEffort,
		RequestedEffort: r.RequestedEffort,
		StopReason:      r.StopReason, Enforcement: r.Enforcement,
		Usage: r.Usage, Turns: r.Turns,
	}
}

// Class returns the agentrun outcome class implied by the result. Only the
// stopReason participates in the decision.
func (r Result) Class() agentrun.OutcomeClass { return Classify(r.StopReason) }

// Classify maps a terminal stopReason onto the shared outcome vocabulary.
// "end_turn" is success, "cancelled" is cancellation, anything else
// (including empty, meaning no terminal result) is failure.
func Classify(stopReason string) agentrun.OutcomeClass {
	switch stopReason {
	case "end_turn":
		return agentrun.OutcomeSuccess
	case "cancelled":
		return agentrun.OutcomeCancellation
	default:
		return agentrun.OutcomeFailure
	}
}

// Args builds the exact command-line tail handed to the launcher: the acpx
// global flags first, then the agent token, then the exec subcommand, then
// the prompt. Arguments are built programmatically; no shell is involved.
func (a *AcpxAdapter) Args(prompt string) []string {
	return a.buildArgs(prompt, "")
}

// buildArgs assembles the launcher argument tail. A non-empty dir inserts
// the --cwd global option among the global flags, BEFORE the agent token, so
// revision runs scope the agent's working domain to the isolated review
// snapshot directory.
func (a *AcpxAdapter) buildArgs(prompt, dir string) []string {
	args := append([]string(nil), a.launcher[1:]...)
	args = append(args,
		"--format", "json",
		"--json-strict",
		"--timeout", strconv.Itoa(a.maxRuntimeSeconds),
	)
	if dir != "" {
		args = append(args, "--cwd", dir)
	}
	args = append(args, a.agent, "exec", prompt)
	return args
}

// Command builds the exec.Cmd for one prompt without starting it. The
// returned command uses os/exec directly and is Windows-safe: no shell
// interpretation happens at any point. Production execution paths use the
// owned-tree spawner in run() instead; Command remains the transparent
// description of what would be executed.
func (a *AcpxAdapter) Command(ctx context.Context, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, a.launcher[0], a.Args(prompt)...) // #nosec G204 -- launcher comes from trusted local configuration
	if len(a.childEnv) > 0 {
		cmd.Env = append(os.Environ(), a.childEnv...)
	}
	return cmd
}

// OwnedTree reports one live owned acpx process tree — preferring the most
// recently started run — or nil when the adapter is idle. It satisfies the
// execution-style TreeProvider contract so controller escalation reaches the
// deep npx -> node(acpx) -> npm exec -> node(<agent>-acp) chain. With
// concurrent runs, the registry keeps every sibling visible until its own
// run completes, so abort escalation always receives a live tree while any
// child is still running.
func (a *AcpxAdapter) OwnedTree() *process.Tree {
	a.treeMu.Lock()
	defer a.treeMu.Unlock()
	if len(a.liveTrees) == 0 {
		return nil
	}
	return a.liveTrees[len(a.liveTrees)-1]
}

// registerTree publishes a freshly spawned tree BEFORE the child can produce
// output, so no window exists in which a running child is invisible to
// abort escalation.
func (a *AcpxAdapter) registerTree(tree *process.Tree) {
	a.treeMu.Lock()
	defer a.treeMu.Unlock()
	a.liveTrees = append(a.liveTrees, tree)
}

// unregisterTree removes exactly this run's tree at that run's completion;
// sibling trees registered by concurrent runs stay untouched.
func (a *AcpxAdapter) unregisterTree(tree *process.Tree) {
	a.treeMu.Lock()
	defer a.treeMu.Unlock()
	for i, live := range a.liveTrees {
		if live == tree {
			a.liveTrees = append(a.liveTrees[:i], a.liveTrees[i+1:]...)
			return
		}
	}
}

// Run executes one prompt through acpx and normalizes the resulting stream.
//
// The Go error is reserved for infrastructure problems (the child could not
// start or its pipes failed). Semantic outcomes travel in the Result: on any
// non-success classification Run also returns an *OutcomeError carrying the
// class, including cancellation, so callers classify by Outcome() rather
// than by error presence.
func (a *AcpxAdapter) Run(ctx context.Context, prompt string) (Result, error) {
	return a.run(ctx, a.Args(prompt))
}

func (a *AcpxAdapter) recordObserved(model, effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastObserved = model
	a.lastObservedEffort = effort
	a.observedKnown = true
}

// observedRun returns the model and effort captured during the most recent
// run. Both come from the same parsed stream, so they share one known flag:
// either a run has completed and reported what it reported, or none has.
func (a *AcpxAdapter) observedRun() (model, effort string, known bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastObserved, a.lastObservedEffort, a.observedKnown
}

// observedModel returns the model captured during the most recent run, if
// any run has completed.
func (a *AcpxAdapter) observedModel() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastObserved, a.observedKnown
}

// OutcomeError reports an adapter-level outcome with its shared class. It
// mirrors the classification semantics of execution.AdapterError without
// importing the execution controller.
type OutcomeError struct {
	Class  agentrun.OutcomeClass
	Detail string
}

func (e *OutcomeError) Error() string {
	if e.Detail == "" {
		return string(e.Class)
	}
	return e.Detail
}

// Outcome exposes the classified outcome for errors.As-style inspection.
func (e *OutcomeError) Outcome() agentrun.OutcomeClass { return e.Class }
