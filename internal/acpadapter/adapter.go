// Package acpadapter drives an ACP-capable agent through the acpx CLI as a
// child process and normalizes its strict NDJSON protocol stream into a
// single result. It keeps every ACP concept inside this package boundary:
// nothing else in the repository learns the wire format.
//
// Outcome classification is decided exclusively by the terminal result's
// stopReason; the child's exit code is never authoritative because real
// backends exit 0 for both completed and cancelled turns.
package acpadapter

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
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
	// ChildEnv holds extra environment entries (KEY=VALUE) appended to the
	// child process environment. Operators use it for credential passthrough;
	// tests use it for the helper-process guard.
	ChildEnv []string
}

// AcpxAdapter executes prompts through an acpx child process and retains the
// evidence needed by upper layers for hash admission and identity reporting.
type AcpxAdapter struct {
	launcher          []string
	agent             string
	model             string
	effort            string
	maxRuntimeSeconds int
	childEnv          []string

	mu            sync.Mutex
	lastObserved  string
	observedKnown bool
}

// NewAcpx validates cfg and returns a ready adapter. The agent token must be
// non-empty; everything else has a deterministic default.
func NewAcpx(cfg Config) (*AcpxAdapter, error) {
	if strings.TrimSpace(cfg.Agent) == "" {
		return nil, fmt.Errorf("acpadapter: agent token is required")
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
	return &AcpxAdapter{
		launcher:          append([]string(nil), launcher...),
		agent:             cfg.Agent,
		model:             cfg.Model,
		effort:            cfg.Effort,
		maxRuntimeSeconds: maxRuntime,
		childEnv:          append([]string(nil), cfg.ChildEnv...),
	}, nil
}

// Result is the normalized observation of one acpx turn. RawStream is kept
// verbatim so upper layers can hash-admit the evidence later; this package
// only retains it.
type Result struct {
	// Output is the assistant answer assembled from every
	// agent_message_chunk text in stream order.
	Output string
	// RawStream is the verbatim stdout bytes of the child process.
	RawStream string
	// Violations counts framing violations skipped while parsing:
	// non-JSON lines and lines over the per-line cap. They are never fatal.
	Violations int
	// ObservedModel is the model captured from the initialize result
	// configOptions when the backend exposes it. Empty otherwise.
	ObservedModel string
	// StopReason is the terminal result stopReason verbatim, or empty when
	// no terminal result arrived.
	StopReason string
	// UsageJSON is the raw usage member of the terminal result, verbatim,
	// or empty when absent.
	UsageJSON string
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
	args := append([]string(nil), a.launcher[1:]...)
	args = append(args,
		"--format", "json",
		"--json-strict",
		"--timeout", strconv.Itoa(a.maxRuntimeSeconds),
		a.agent,
		"exec",
		prompt,
	)
	return args
}

// Command builds the exec.Cmd for one prompt without starting it. The
// returned command uses os/exec directly and is Windows-safe: no shell
// interpretation happens at any point.
func (a *AcpxAdapter) Command(ctx context.Context, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, a.launcher[0], a.Args(prompt)...) // #nosec G204 -- launcher comes from trusted local configuration
	if len(a.childEnv) > 0 {
		cmd.Env = append(os.Environ(), a.childEnv...)
	}
	return cmd
}

// Run executes one prompt through acpx and normalizes the resulting stream.
//
// The Go error is reserved for infrastructure problems (the child could not
// start or its pipes failed). Semantic outcomes travel in the Result: on any
// non-success classification Run also returns an *OutcomeError carrying the
// class, including cancellation, so callers classify by Outcome() rather
// than by error presence.
func (a *AcpxAdapter) Run(ctx context.Context, prompt string) (Result, error) {
	cmd := a.Command(ctx, prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, &OutcomeError{
			Class:  agentrun.OutcomeProcessError,
			Detail: fmt.Sprintf("acpx: stdout pipe unavailable: %v", err),
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw := &bytes.Buffer{}
	reader := bufio.NewReaderSize(io.TeeReader(stdout, raw), 64*1024)

	if err := cmd.Start(); err != nil {
		return Result{}, &OutcomeError{
			Class:  agentrun.OutcomeProcessError,
			Detail: fmt.Sprintf("acpx: launch %q failed: %v", filepath.Base(a.launcher[0]), err),
		}
	}
	stream := ParseStream(reader, DefaultLineCapBytes)
	waitErr := cmd.Wait()

	res := Result{
		Output:        stream.Output,
		RawStream:     raw.String(),
		Violations:    stream.Violations,
		ObservedModel: stream.ObservedModel,
		StopReason:    stream.StopReason,
		UsageJSON:     stream.UsageJSON,
	}
	a.recordObserved(stream.ObservedModel)
	if res.StopReason == "" {
		class := agentrun.OutcomeFailure
		detail := "acpx: no terminal result"
		if waitErr != nil {
			detail = fmt.Sprintf("acpx: no terminal result (child error: %v)", waitErr)
		}
		if ctx.Err() != nil {
			class = agentrun.OutcomeTimeout
			detail = fmt.Sprintf("acpx: no terminal result before context end (%v)", ctx.Err())
		}
		return res, &OutcomeError{Class: class, Detail: detail}
	}
	if class := res.Class(); class != agentrun.OutcomeSuccess {
		return res, &OutcomeError{
			Class:  class,
			Detail: fmt.Sprintf("acpx: terminal stopReason %q", res.StopReason),
		}
	}
	return res, nil
}

func (a *AcpxAdapter) recordObserved(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastObserved = model
	a.observedKnown = true
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
