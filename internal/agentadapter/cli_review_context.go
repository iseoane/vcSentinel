package agentadapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/process"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
)

// ReviewWithContext runs the same restricted review as RunReview but
// derives its execution budget from the caller-supplied context: the adapter
// timeout still applies, and an earlier caller cancellation or deadline wins.
// This is the seam that lets the durable execution controller's worker
// context reach the spawned provider process, so Apply(ActionAbort) kills it
// instead of waiting for it to finish (R7 slice 1).
//
// The method name pairs with the legacy RunReview entry point it
// extends; reviewexec.ReviewAdapter discovers it structurally through its
// ContextualReviewer contract, so wrappers and chains forward it unchanged.
func (c *CLIAdapter) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	return c.reviewWithContextPolicy(ctx, prompt, sha, paths, reviewcontract.DefaultToolPolicy())
}

// ReviewWithContextAndPolicy executes a semantic review under the supplied
// provider-neutral contract policy while preserving controller cancellation.
func (c *CLIAdapter) ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	return c.reviewWithContextPolicy(ctx, prompt, sha, paths, policy)
}

// ReviewWithContextResult runs the same restricted review as
// ReviewWithContext and preserves the provider observations the run
// produced, so durable execution records can attribute token usage and
// outcome classes the way the ACP family already does. It satisfies
// reviewexec.ResultContextualReviewer structurally.
func (c *CLIAdapter) ReviewWithContextResult(ctx context.Context, prompt, sha string, paths []string) (acpadapter.Result, error) {
	return c.reviewWithContextResultPolicy(ctx, prompt, sha, paths, reviewcontract.DefaultToolPolicy())
}

// ReviewWithContextAndPolicyResult is the policy-bound counterpart of
// ReviewWithContextResult: the resolved dimension policy travels with the
// cancellation context, and the wire observations come back with both. It
// satisfies reviewexec.ResultPolicyContextualReviewer structurally.
func (c *CLIAdapter) ReviewWithContextAndPolicyResult(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (acpadapter.Result, error) {
	return c.reviewWithContextResultPolicy(ctx, prompt, sha, paths, policy)
}

// reviewWithContextResultPolicy is the single rich review flow. The string
// contracts project it down to its answer text, so every surface observes
// byte-identical text from the same run and the same scanner.
func (c *CLIAdapter) reviewWithContextResultPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (acpadapter.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = CommandTimeout
	}
	result := c.declaredResult()
	// The timeout-bounded context is derived ONCE, here, before snapshot
	// creation, and reused for both the snapshot and the provider run. That
	// used to happen only inside runBoundedReview, after the snapshot had
	// already been materialized from the audited commit's whole tree: the
	// caller's context bounded snapshot creation (if it carried a deadline
	// of its own at all), but the adapter's OWN timeout only ever started
	// applying once runBoundedReview began, letting a slow or pathological
	// tree consume unbounded wall-clock time first. Deriving boundedCtx here
	// closes that gap without weakening runBoundedReview's ownership of the
	// deadline/cancel distinction: runBoundedReview still derives its own
	// context.WithTimeout(parent, timeout) from boundedCtx, but since
	// boundedCtx's deadline was fixed at this earlier point, that inner
	// derivation always resolves to the SAME deadline (context.WithTimeout
	// takes the earlier of the two), never a later one — so the budget is
	// not extended by however long the snapshot took, and there is exactly
	// one effective deadline, not two independently-firing ones with
	// possibly different reported reasons.
	//
	// reviewsnapshot.Create already joins ctx's own error into whatever it
	// returns (see its doc comment), so a snapshot aborted by boundedCtx's
	// deadline classifies as context.DeadlineExceeded — a timeout, never a
	// cancellation — exactly like callerContextOutcome's discipline in
	// internal/acpadapter, with no extra handling needed here.
	boundedCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snapshot, safePaths, cleanup, err := createReviewSnapshot(boundedCtx, "", sha, paths)
	if err != nil {
		return result, err
	}
	defer cleanup()
	request := ReviewRequest{Prompt: prompt, SHA: sha, Paths: safePaths, SnapshotDir: snapshot, MaxToolCalls: defaultReviewToolCalls, ToolPolicy: policy}
	run, err := c.runBoundedReview(boundedCtx, request, timeout)
	if err != nil {
		return result, err
	}
	result.Output = run.output
	result.Usage = run.usage
	result.UsageJSON = run.usageJSON
	result.StopReason = run.stopReason
	if run.turns != nil {
		turns := *run.turns
		result.Turns = &turns
	}
	// A stop reason other than a completed turn ("end_turn"), a cancellation
	// (already handled by the caller's context plumbing), or the absence of
	// one at all (generic plain-text providers report no stop reason, which
	// is normal and not evidence of anything) means the provider ended the
	// process without finishing its turn.
	//
	// terminalEventObserved distinguishes the one case genuine absence alone
	// cannot: for OpenCode, an empty stop reason with NO terminal event
	// observed at all (the stream never produced a single step_finish) is
	// the most severe truncation there is, worse than a labeled reason like
	// "tool-calls" — even the provider's own end-of-turn signal never
	// arrived. Non-OpenCode paths report terminalEventObserved true
	// unconditionally, so this can never misclassify a generic provider or
	// Claude, neither of which this fix touches.
	//
	// The wire evidence above still lands on result regardless of the
	// classification: only the answer TEXT is untrustworthy when the turn
	// did not complete, which is why the legacy string-only surface
	// (reviewWithContextPolicy) empties its returned string on error instead
	// of forwarding result.Output — the rich Result kept here is evidence,
	// not a verdict, and the caller must not parse it as one.
	incomplete := run.stopReason != "" && run.stopReason != "end_turn" && run.stopReason != "cancelled"
	if !incomplete && !run.terminalEventObserved {
		incomplete = true
	}
	if incomplete {
		steps := 0
		if run.turns != nil {
			steps = *run.turns
		}
		return result, &TruncatedTurnError{
			StopReason:            run.stopReason,
			Steps:                 steps,
			TerminalEventObserved: run.terminalEventObserved,
			ToolCallErrors:        run.deniedToolCalls,
		}
	}
	return result, nil
}

// TruncatedTurnError reports that the restricted reviewer's process ended
// before completing its turn. It is non-retryable — see internal/review's
// permanentProviderFailures — because a turn ending this way, whether from a
// turn budget or a denied tool call, reproduces on retry against the same
// snapshot and the same restrictions.
//
// Its message states only what was observed, never a cause it cannot know.
// An earlier version claimed the reviewer "exhausted its turn budget" on
// every truncation; measured evidence (11 real review invocations) disproved
// that: truncations landed at 4 and 5 turns out of a 16-turn budget, every
// one correlated with at least one denied tool call, never with the budget
// being exhausted. ToolCallErrors carries that denial evidence when the
// stream recorded one, so the message can name the actual reason instead.
type TruncatedTurnError struct {
	// StopReason is the provider's terminal stop reason, already translated
	// onto the acpadapter vocabulary (see mapOpenCodeStopReason). Empty when
	// TerminalEventObserved is false.
	StopReason string
	// Steps is the number of model turns the run consumed before it was cut
	// off, as counted by the provider's stream scanner (opencodeReviewScan.Steps
	// for OpenCode). Zero when the provider format carries no turn count, or
	// when TerminalEventObserved is false.
	Steps int
	// TerminalEventObserved reports whether the provider's own end-of-turn
	// signal was ever seen at all (opencodeReviewScan.TerminalEventObserved
	// for OpenCode; true unconditionally for every other provider path,
	// which this error type does not reclassify). False is the most severe
	// truncation there is.
	TerminalEventObserved bool
	// ToolCallErrors lists every tool call the stream recorded as denied
	// (opencodeReviewScan.DeniedToolCalls for OpenCode), in stream order.
	// Empty when the truncation is not correlated with any observed denial.
	ToolCallErrors []DeniedToolCall
}

func (e *TruncatedTurnError) Error() string {
	var message strings.Builder
	message.WriteString("review turn truncated: the turn ended without completing")
	if e.TerminalEventObserved {
		fmt.Fprintf(&message, " after %d turn(s)", e.Steps)
	} else {
		message.WriteString(" before any terminal event was observed")
	}
	if e.StopReason != "" {
		fmt.Fprintf(&message, " (stop reason: %s)", e.StopReason)
	}
	if len(e.ToolCallErrors) > 0 {
		message.WriteString(": denied tool call")
		if len(e.ToolCallErrors) > 1 {
			message.WriteString("s")
		}
		message.WriteString(" — ")
		details := make([]string, len(e.ToolCallErrors))
		for i, denied := range e.ToolCallErrors {
			details[i] = fmt.Sprintf("%s: %s", denied.Tool, denied.Error)
		}
		message.WriteString(strings.Join(details, "; "))
	}
	return message.String()
}

// declaredResult carries the configured request declarations on every
// runtime path, including pre-spawn failures, mirroring acpadapter's
// discipline. Configured model/effort are request evidence, never wire
// observations: they travel in the Requested* fields, and Observed* stays
// empty because neither the OpenCode event stream nor the Claude result
// object exposes a wire identity.
func (c *CLIAdapter) declaredResult() acpadapter.Result {
	return acpadapter.Result{
		RequestedModel:  c.Config.Model,
		RequestedEffort: c.Config.ReasoningEffort,
	}
}

// reviewWithContextPolicy keeps the legacy string contract: one restricted
// review, answer text only, empty output on error. It is the rich flow
// projected down, so the string and rich surfaces share both the scanner and
// the single TrimSpace text boundary.
func (c *CLIAdapter) reviewWithContextPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	result, err := c.reviewWithContextResultPolicy(ctx, prompt, sha, paths, policy)
	if err != nil {
		// The rich Result above keeps Output as evidence even on error (a
		// truncated turn's partial narration is still useful for diagnosis),
		// but this legacy surface's own contract promises empty output on
		// error: an earlier version returned result.Output unconditionally,
		// letting an untrustworthy partial answer cross this boundary as if
		// it were a verdict.
		return "", err
	}
	return result.Output, nil
}

// reviewExecution is the rich observation of one restricted review run: the
// observable answer text plus the wire observations the provider's output
// format carries — the OpenCode --format json event stream and the Claude
// Code --output-format json result object. Generic providers answer in plain
// text and populate the output alone.
type reviewExecution struct {
	output     string
	usage      *acpadapter.Usage
	usageJSON  string
	stopReason string
	// turns is the single source of truth for the observed per-turn count:
	// non-nil only for OpenCode, where an observed count (including a
	// legitimate small one) is real evidence; nil for Claude (no comparable
	// per-turn count exists) and for generic providers (nothing to scan).
	// It is the pointer this run's caller carries forward onto
	// acpadapter.Result.Turns. TruncatedTurnError.Steps on the error path
	// stays a plain int derived from this pointer at its construction site
	// in reviewWithContextResultPolicy, rather than duplicating the value in
	// a second field here.
	turns *int
	// terminalEventObserved reports whether the provider's own end-of-turn
	// signal was ever seen at all (opencodeReviewScan.TerminalEventObserved
	// for OpenCode). True unconditionally for Claude and generic providers,
	// which this fix does not touch: their stopReason semantics are left
	// exactly as they were.
	terminalEventObserved bool
	// deniedToolCalls carries OpenCode's observed tool-call denials
	// (opencodeReviewScan.DeniedToolCalls) forward so a truncated turn's
	// error can name the cause. Always empty for Claude and generic
	// providers.
	deniedToolCalls []DeniedToolCall
}

// runBoundedReview spawns the restricted reviewer under a combined budget:
// the parent context governs cooperative cancellation and the timeout bounds
// a provider that never answers. The child is born into an owned process
// tree, and the containment watchdog guarantees that even without the
// controller's escalation the tree never outlives its context by more than
// the shared grace budget plus a fixed margin. The returned observation
// adds the extracted answer text and — for OpenCode and Claude — the wire
// usage and terminal stop reason the provider's output format reports.
func (c *CLIAdapter) runBoundedReview(parent context.Context, request ReviewRequest, timeout time.Duration) (reviewExecution, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	args, restrictions, err := c.reviewCommand(request)
	if err != nil {
		return reviewExecution{}, err
	}
	env, cleanupEnvironment, err := c.newRestrictedReviewEnvironment(restrictions["OPENCODE_CONFIG_CONTENT"], request.SnapshotDir)
	if err != nil {
		return reviewExecution{}, err
	}
	defer cleanupEnvironment()
	dir := ""
	if c.isClaude() {
		// Claude Code has no "--dir"-style flag (unlike OpenCode's --pure +
		// --dir), so it runs with the snapshot as its working directory. Tool
		// authorization is configured by reviewCommand; this is not an OS
		// sandbox. Tests verify the generated CLI arguments and snapshot cwd,
		// not live provider permission enforcement or path-matcher behavior.
		dir = request.SnapshotDir
	}

	spawn, err := startOwnedCommand(ctx, c.BinaryName, args, env, dir, request.Prompt)
	if err != nil {
		return reviewExecution{}, err
	}
	c.activeTree.Store(spawn.tree)
	defer c.activeTree.Store(nil)
	waitErr := spawn.cmd.Wait()
	spawn.tree.MarkExited()
	spawn.tree.Release()
	if waitErr != nil {
		detail := strings.TrimSpace(spawn.stderr.String())
		if detail == "" {
			// stderr yielded nothing, but the Claude branch reports a
			// failure as a result object on stdout (see
			// claudeFailureDetail): read that fallback before returning a
			// bare exit status. An empty, non-JSON, or message-free stdout
			// keeps the current behaviour exactly.
			if fallback, ok := claudeFailureDetail(spawn.stdout.String()); ok {
				detail = fallback
			}
		}
		// This function owns the deadline, so it is the ONLY place that can
		// tell a provider crash from a provider that ran out of time. When the
		// deadline fires, the containment watchdog SIGTERMs the process group
		// and cmd.Wait reports "signal: terminated" — a text that says nothing
		// about a timeout. Joining ctx.Err() keeps errors.Is(err,
		// context.DeadlineExceeded) true end to end, so reviewexec's classifier
		// records OutcomeTimeout instead of OutcomeFailure and the durable
		// record stops misreporting why the dimension failed.
		if ctxErr := ctx.Err(); ctxErr != nil {
			reason := "timed out after " + timeout.String()
			if errors.Is(ctxErr, context.Canceled) {
				reason = "canceled"
			}
			if detail != "" {
				return reviewExecution{}, fmt.Errorf("run restricted reviewer %s: %w: %s", reason, errors.Join(waitErr, ctxErr), detail)
			}
			return reviewExecution{}, fmt.Errorf("run restricted reviewer %s: %w", reason, errors.Join(waitErr, ctxErr))
		}
		if detail != "" {
			return reviewExecution{}, fmt.Errorf("run restricted reviewer: %w: %s", waitErr, detail)
		}
		return reviewExecution{}, waitErr
	}
	raw := spawn.stdout.String()
	switch {
	case c.isOpenCode():
		scan, err := scanOpenCodeReview(strings.NewReader(raw))
		if err != nil {
			return reviewExecution{}, err
		}
		turns := scan.Steps
		return reviewExecutionFromScan(scan.Output, scan.Usage, scan.UsageJSON, scan.StopReason, &turns, scan.TerminalEventObserved, scan.DeniedToolCalls), nil
	case c.isClaude():
		scan, err := scanClaudeReview(strings.NewReader(raw))
		if err != nil {
			return reviewExecution{}, err
		}
		// Claude Code exposes no per-turn step count comparable to OpenCode's
		// Steps budget (reviewCommand's own comment: there is no confirmed
		// flag to cap or observe it), so turns stays nil rather than a
		// fabricated zero. terminalEventObserved stays true unconditionally:
		// this fix is scoped to OpenCode's stream and must not reclassify
		// Claude's own stopReason semantics.
		return reviewExecutionFromScan(scan.Output, scan.Usage, scan.UsageJSON, scan.StopReason, nil, true, nil), nil
	default:
		// generic providers answer in plain text; there is nothing to scan,
		// so this is a literal with output and terminalEventObserved set and
		// every other field (including turns) left at its zero value —
		// nil for the turns pointer. terminalEventObserved stays true: a
		// generic provider legitimately reports no stop reason at all, and
		// that must keep working exactly as before.
		return reviewExecution{output: strings.TrimSpace(raw), terminalEventObserved: true}, nil
	}
}

// reviewExecutionFromScan projects a provider scan onto the rich review
// observation. The single TrimSpace lives here so every provider's
// stdout->answer boundary behaves byte-identically.
func reviewExecutionFromScan(output string, usage *acpadapter.Usage, usageJSON, stopReason string, turns *int, terminalEventObserved bool, deniedToolCalls []DeniedToolCall) reviewExecution {
	return reviewExecution{
		output:                strings.TrimSpace(output),
		usage:                 usage,
		usageJSON:             usageJSON,
		stopReason:            stopReason,
		turns:                 turns,
		terminalEventObserved: terminalEventObserved,
		deniedToolCalls:       deniedToolCalls,
	}
}

// ownedCommand carries one started child with its ownership handle and
// capture buffers until the spawner has waited for it.
type ownedCommand struct {
	cmd    *exec.Cmd
	tree   *process.Tree
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

// startOwnedCommand is the single spawn point of the restricted reviewer: one
// context-bound, process-group/job-owned exec whose whole tree is accounted
// for from birth, feeding stdin and capturing stdout and stderr. The shared
// containment watchdog (process.ContainAfterCancellation) kills the tree if
// the context stays canceled past the shared grace budget plus margin;
// controller-authored escalation normally wins that race and appends the
// durable evidence instead.
func startOwnedCommand(ctx context.Context, name string, args []string, env []string, dir, stdin string) (*ownedCommand, error) {
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd, tree, err := process.Spawn(ctx, name, args, func(c *exec.Cmd) {
		c.Env = env
		c.Dir = dir
		c.Stdout = &out
		c.Stderr = &stderr
		c.Stdin = strings.NewReader(stdin)
	})
	if err != nil {
		return nil, err
	}
	go process.ContainAfterCancellation(ctx, tree)
	return &ownedCommand{cmd: cmd, tree: tree, stdout: &out, stderr: &stderr}, nil
}

// runCapturedCommand is the direct capture seam kept for callers and tests
// that spawn without the adapter's tree tracking: same owned birth, same
// containment watchdog, plain stdout/stderr/stdin wiring.
func runCapturedCommand(ctx context.Context, name string, args []string, env []string, dir, stdin string) (string, string, error) {
	spawn, err := startOwnedCommand(ctx, name, args, env, dir, stdin)
	if err != nil {
		return "", "", err
	}
	defer spawn.tree.Release()
	err = spawn.cmd.Wait()
	spawn.tree.MarkExited()
	return spawn.stdout.String(), spawn.stderr.String(), err
}
