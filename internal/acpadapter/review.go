package acpadapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/process"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewsnapshot"
)

// RunPrompt runs one arbitrary prompt through acpx and returns the
// normalized assistant output. It mirrors the prompt-adapter shape of
// internal/agentadapter so upper layers can treat both adapter kinds alike.
// Non-success outcomes return an *OutcomeError carrying the outcome class;
// callers classify by Outcome(), never by error presence. The returned
// string is the assistant output ONLY, by legacy contract: programmatic
// consumers holding *AcpxAdapter receive the full evidence Result (raw
// stream, observed model, stop reason, usage, enforcement declaration)
// through Run.
// RunPrompt and RunReview keep their Spanish names by
// INTERFACE-CONFORMANCE EXCEPTION: cmd/sentinel and the review engine assert
// these exact method names structurally (legacy AuditorAgente contract).
// AGENTS.md's English rule governs new vocabulary; these identifiers are a
// legacy protocol this adapter must speak to be substitutable.
func (a *AcpxAdapter) RunPrompt(prompt string) (string, error) {
	return a.RunPromptWithContext(context.Background(), prompt)
}

// RunPromptWithContext runs the same arbitrary-prompt core as
// RunPrompt but honors the caller-supplied context instead of a
// detached background one, so controller abort reaches the spawned acpx
// tree through cancellation. Nil contexts fall back to Background. The
// result contract matches RunPrompt: assistant output only, with
// non-success outcomes carried as *OutcomeError.
func (a *AcpxAdapter) RunPromptWithContext(ctx context.Context, prompt string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := a.Run(ctx, prompt)
	return res.Output, err
}

// RunReview runs a semantic review under the SAME snapshot discipline
// as CLIAdapter.RunReview: the audited paths are materialized read-only
// from committed content into the published shared snapshot for the audited
// SHA, acpx is pointed at that directory through its --cwd global option
// (before the agent token), and the caller-owned cleanup releases the
// retained snapshot's lease — the lock-aware stale reaper owns its removal.
// The legacy contract carries no context; context-carrying callers go
// through ReviewWithContext.
func (a *AcpxAdapter) RunReview(prompt, sha string, paths []string) (string, error) {
	return a.ReviewWithContext(context.Background(), prompt, sha, paths)
}

// ReviewWithContext runs RunReview's restricted review but derives the
// execution budget from the caller-supplied context: the adapter runtime
// budget still applies, and an earlier caller cancellation wins. Cancellation
// reaches the child through the owned-tree containment watchdog (and the
// controller's escalation when one is wired), and classifies as the canceled
// outcome class wrapping the context error.
//
// The method name pairs with the legacy RunReview entry point it
// extends; it satisfies reviewexec.ContextualReviewer structurally.
func (a *AcpxAdapter) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	res, err := a.ReviewWithContextResult(ctx, prompt, sha, paths)
	return res.Output, err
}

// ReviewWithContextResult preserves the normalized ACP result, including
// partial output and wire observations when the provider returns an outcome
// error after producing them.
func (a *AcpxAdapter) ReviewWithContextResult(ctx context.Context, prompt, sha string, paths []string) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot, _, cleanup, err := reviewsnapshot.Create(ctx, "", sha, paths)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// A snapshot abort caused by the caller's own context ending is
			// not a process failure: it must classify exactly like a
			// cancellation or timeout observed later in run, or a caller that
			// interrupts an audit while the (potentially whole-tree) snapshot
			// is still being materialized — before any provider process is
			// even spawned — gets an OutcomeError-less plain error here that
			// downstream classifiers cannot attribute, or worse, the ambient
			// context ending races the eventual spawn attempt into an
			// unrelated *OutcomeError with the wrong class. callerContextOutcome
			// keeps the deadline/cancellation distinction this package
			// maintains everywhere else.
			class, reason := callerContextOutcome(err)
			return a.declaredResult(), &OutcomeError{
				Class:  class,
				Detail: fmt.Sprintf("acpx: %s before review snapshot ready: %v", reason, err),
			}
		}
		return a.declaredResult(), fmt.Errorf("acpx: create review snapshot: %w", err)
	}
	defer cleanup()
	return a.run(ctx, a.buildArgs(prompt, snapshot))
}

// outputOf reduces a full Result to its assistant output for the legacy
// string-returning review contracts while preserving partial output on
// provider outcome errors.
func (a *AcpxAdapter) outputOf(res Result, err error) (string, error) {
	return res.Output, err
}

// stderrExcerptLimit caps how much stderr text a failure detail may carry.
const stderrExcerptLimit = 500

// stderrExcerpt renders the bounded "stderr: ..." excerpt carried by FAILURE
// and TIMEOUT outcome details, mirroring CLIAdapter's discipline of wrapping
// errors with captured stderr. Trimming removes surrounding whitespace; the
// cap keeps the LAST 500 characters, because provider diagnostics put the
// actionable cause at the end. Empty stderr yields no excerpt at all.
func stderrExcerpt(stderr *bytes.Buffer) string {
	excerpt := strings.TrimSpace(stderr.String())
	if excerpt == "" {
		return ""
	}
	if len(excerpt) > stderrExcerptLimit {
		excerpt = excerpt[len(excerpt)-stderrExcerptLimit:]
	}
	return "stderr: " + excerpt
}

// outcomeDetail enriches a FAILURE or TIMEOUT outcome detail with the bounded
// stderr excerpt when the child produced any. Cancellation and success
// outcomes must not pass through here: their details stay byte-identical to
// the pre-enrichment contract regardless of what the child wrote to stderr.
func outcomeDetail(class agentrun.OutcomeClass, detail string, stderr *bytes.Buffer) string {
	if class != agentrun.OutcomeFailure && class != agentrun.OutcomeTimeout {
		return detail
	}
	if excerpt := stderrExcerpt(stderr); excerpt != "" {
		return detail + ": " + excerpt
	}
	return detail
}

// run executes one fully built acpx command line and normalizes its stream.
// declaredResult preserves configured declarations on every runtime path,
// including pre-spawn failures where no wire observation exists.
// callerContextOutcome separates the two ways a caller context ends. An
// expired deadline is a budget exhaustion, so reporting it as a cancellation
// would file a timeout under the class reserved for a deliberate stop and
// blur exactly the distinction the surrounding classifier maintains.
func callerContextOutcome(err error) (agentrun.OutcomeClass, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return agentrun.OutcomeTimeout, "caller deadline exceeded"
	}
	return agentrun.OutcomeCancellation, "canceled"
}

func (a *AcpxAdapter) declaredResult() Result {
	return Result{
		Agent:           a.agent,
		RequestedModel:  a.model,
		RequestedEffort: a.effort,
		Enforcement:     a.enforcement,
	}
}

// Ownership (R7): the child is born into an owned process group/job through
// process.Spawn — exactly like the CLI adapter's reviewer spawn — so the
// whole npx -> node(acpx) -> npm exec -> node(<agent>-acp) chain stays
// accounted for from birth. The live tree is published through OwnedTree for
// controller escalation, and a containment watchdog hard-terminates the tree
// if the context stays canceled past the shared grace budget plus margin.
// Cooperative session/cancel is OUT OF SCOPE by design: acpx exec is a
// stateless one-shot, so context-driven containment plus controller
// escalation covers cancellation completely.
func (a *AcpxAdapter) run(parent context.Context, args []string) (Result, error) {
	if parent == nil {
		parent = context.Background()
	}
	// Defense in depth: --timeout bounds the child from its own side while
	// the same runtime budget bounds our context, so whichever fires first
	// still ends the turn deterministically.
	runCtx, cancel := context.WithTimeout(parent, time.Duration(a.maxRuntimeSeconds)*time.Second)
	defer cancel()

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return a.declaredResult(), &OutcomeError{
			Class:  agentrun.OutcomeProcessError,
			Detail: fmt.Sprintf("acpx: stdout pipe unavailable: %v", err),
		}
	}
	var stderr bytes.Buffer
	raw := &bytes.Buffer{}

	cmd, tree, err := process.Spawn(runCtx, a.launcher[0], args, func(c *exec.Cmd) {
		if len(a.childEnv) > 0 {
			c.Env = append(os.Environ(), a.childEnv...)
		}
		c.Stdout = stdoutW
		c.Stderr = &stderr
	})
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		// A spawn that never started because the CALLER's own context had
		// already ended (os/exec's Cmd.Start checks ctx.Err() up front and
		// returns it immediately) is not a process failure: it is the exact
		// same cancellation/timeout the switch below classifies once a
		// terminal result exists, just observed earlier, before there was
		// ever a child to wait on. runCtx is derived from parent with the
		// runtime budget layered on top, so parent.Err() — not runCtx.Err()
		// — is what keeps the deadline/cancel distinction callerContextOutcome
		// exists to preserve; consulting runCtx here would misclassify a
		// caller cancellation as a runtime-budget timeout whenever both
		// happen to be set.
		//
		// The caller-context class applies only when err IS the context
		// ending, not merely concurrent with it: os/exec resolves the
		// executable and stores any lookup failure BEFORE Start() ever
		// checks ctx.Done(), so a genuine launch failure (missing binary,
		// permission error, ...) returns its own error even when the
		// caller's context happens to have ended around the same moment.
		// Inferring the class from parent.Err() alone — without checking
		// that err itself is the context's own sentinel error — would
		// misreport that broken installation as a cancellation or timeout.
		if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && parent.Err() != nil {
			class, reason := callerContextOutcome(parent.Err())
			return a.declaredResult(), &OutcomeError{
				Class:  class,
				Detail: fmt.Sprintf("acpx: %s before launch %q could start: %v", reason, filepath.Base(a.launcher[0]), err),
			}
		}
		return a.declaredResult(), &OutcomeError{
			Class:  agentrun.OutcomeProcessError,
			Detail: fmt.Sprintf("acpx: launch %q failed: %v", filepath.Base(a.launcher[0]), err),
		}
	}
	a.registerTree(tree)
	defer a.unregisterTree(tree)

	// Drop the parent's write end so the drain sees EOF once the child exits.
	stdoutW.Close()

	exceeded := false
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		defer stdoutR.Close()
		chunk := make([]byte, 64*1024)
		var total int64
		for {
			n, readErr := stdoutR.Read(chunk)
			if n > 0 {
				total += int64(n)
				retain := int64(n)
				if a.maxOutputBytes > 0 && total > a.maxOutputBytes {
					// Exact retention: never hold more than the configured
					// budget in memory. The final chunk is truncated at the
					// budget boundary while `total` keeps counting true bytes,
					// so the breach verdict below still reflects everything
					// the child actually produced.
					if room := a.maxOutputBytes - (total - int64(n)); room < retain {
						retain = room
					}
				}
				if retain > 0 {
					raw.Write(chunk[:retain])
				}
				if a.maxOutputBytes > 0 && total > a.maxOutputBytes {
					// Budget breach: stop reading, break the pipe, and take
					// the whole tree down. Closing the read end also makes
					// any further child write fail on its own. The verdict
					// below is failure regardless of what the truncated
					// stream claimed afterwards.
					exceeded = true
					_ = process.Terminate(tree)
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	go process.ContainAfterCancellation(runCtx, tree)

	waitErr := cmd.Wait()
	tree.MarkExited()
	tree.Release()
	<-drained

	stream := ParseStream(bytes.NewReader(raw.Bytes()), DefaultLineCapBytes)
	res := Result{
		Output:          stream.Output,
		RawStream:       raw.String(),
		Violations:      stream.Violations,
		Agent:           a.agent,
		ObservedModel:   stream.ObservedModel,
		RequestedModel:  a.model,
		ObservedEffort:  stream.ObservedEffort,
		RequestedEffort: a.effort,
		StopReason:      stream.StopReason,
		UsageJSON:       stream.UsageJSON,
		Usage:           stream.Usage,
		Enforcement:     a.enforcement,
	}
	a.recordObserved(stream.ObservedModel, stream.ObservedEffort)

	switch {
	case exceeded:
		return res, &OutcomeError{
			Class:  agentrun.OutcomeFailure,
			Detail: outcomeDetail(agentrun.OutcomeFailure, "acpx: output budget exceeded", &stderr),
		}
	case parent.Err() != nil:
		class, reason := callerContextOutcome(parent.Err())
		return res, &OutcomeError{
			Class:  class,
			Detail: fmt.Sprintf("acpx: %s before terminal result (%v)", reason, parent.Err()),
		}
	case runCtx.Err() != nil:
		return res, &OutcomeError{
			Class:  agentrun.OutcomeTimeout,
			Detail: outcomeDetail(agentrun.OutcomeTimeout, fmt.Sprintf("acpx: no terminal result before the runtime budget (%v)", runCtx.Err()), &stderr),
		}
	case waitErr != nil:
		return res, &OutcomeError{
			Class:  agentrun.OutcomeProcessError,
			Detail: fmt.Sprintf("acpx: child process failed after terminal result: %v", waitErr),
		}
	case res.StopReason == "":
		class := agentrun.OutcomeFailure
		detail := "acpx: no terminal result"
		switch {
		case parent.Err() != nil:
			reason := ""
			class, reason = callerContextOutcome(parent.Err())
			detail = fmt.Sprintf("acpx: %s before terminal result (%v)", reason, parent.Err())
		case runCtx.Err() != nil:
			class = agentrun.OutcomeTimeout
			detail = outcomeDetail(class, fmt.Sprintf("acpx: no terminal result before the runtime budget (%v)", runCtx.Err()), &stderr)
		default:
			detail = outcomeDetail(class, detail, &stderr)
		}
		return res, &OutcomeError{Class: class, Detail: detail}
	}
	if class := res.Class(); class != agentrun.OutcomeSuccess {
		return res, &OutcomeError{
			Class:  class,
			Detail: outcomeDetail(class, fmt.Sprintf("acpx: terminal stopReason %q", res.StopReason), &stderr),
		}
	}
	return res, nil
}
