package agentadapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// ReviewWithContext runs the same restricted review as EjecutarRevision but
// derives its execution budget from the caller-supplied context: the adapter
// timeout still applies, and an earlier caller cancellation or deadline wins.
// This is the seam that lets the durable execution controller's worker
// context reach the spawned provider process, so Apply(ActionAbort) kills it
// instead of waiting for it to finish (R7 slice 1).
//
// The method name pairs with the legacy EjecutarRevision entry point it
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
		timeout = TimeoutComando
	}
	result := c.declaredResult()
	snapshot, safePaths, cleanup, err := createReviewSnapshot("", sha, paths)
	if err != nil {
		return result, err
	}
	defer cleanup()
	request := ReviewRequest{Prompt: prompt, SHA: sha, Paths: safePaths, SnapshotDir: snapshot, MaxToolCalls: defaultReviewToolCalls, ToolPolicy: policy}
	run, err := c.ejecutarRevision(ctx, request, timeout)
	if err != nil {
		return result, err
	}
	result.Output = run.output
	result.Usage = run.usage
	result.UsageJSON = run.usageJSON
	result.StopReason = run.stopReason
	return result, nil
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
	return result.Output, err
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
}

// ejecutarRevision spawns the restricted reviewer under a combined budget:
// the parent context governs cooperative cancellation and the timeout bounds
// a provider that never answers. The child is born into an owned process
// tree, and the containment watchdog guarantees that even without the
// controller's escalation the tree never outlives its context by more than
// the shared grace budget plus a fixed margin. The returned observation
// adds the extracted answer text and — for OpenCode and Claude — the wire
// usage and terminal stop reason the provider's output format reports.
func (c *CLIAdapter) ejecutarRevision(parent context.Context, request ReviewRequest, timeout time.Duration) (reviewExecution, error) {
	ctx, cancelar := context.WithTimeout(parent, timeout)
	defer cancelar()

	args, restrictions, err := c.reviewCommand(request)
	if err != nil {
		return reviewExecution{}, err
	}
	env := os.Environ()
	dir := ""
	if c.esClaude() {
		// Claude Code has no "--dir"-style flag (unlike OpenCode's --pure +
		// --dir), so it runs with the snapshot as its working directory. Tool
		// authorization is configured by reviewCommand; this is not an OS
		// sandbox. Tests verify the generated CLI arguments and snapshot cwd,
		// not live provider permission enforcement or path-matcher behavior.
		dir = request.SnapshotDir
	} else {
		env = reviewEnvironment(restrictions["OPENCODE_CONFIG_CONTENT"], request.SnapshotDir, c.Config.Model)
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
		// This function owns the deadline, so it is the ONLY place that can
		// tell a provider crash from a provider that ran out of time. When the
		// deadline fires, the containment watchdog SIGTERMs the process group
		// and cmd.Wait reports "signal: terminated" — a text that says nothing
		// about a timeout. Joining ctx.Err() keeps errors.Is(err,
		// context.DeadlineExceeded) true end to end, so reviewexec's classifier
		// records OutcomeTimeout instead of OutcomeFailure and the durable
		// record stops misreporting why the dimension failed.
		if ctxErr := ctx.Err(); ctxErr != nil {
			motivo := "timed out after " + timeout.String()
			if errors.Is(ctxErr, context.Canceled) {
				motivo = "canceled"
			}
			if detail != "" {
				return reviewExecution{}, fmt.Errorf("run restricted reviewer %s: %w: %s", motivo, errors.Join(waitErr, ctxErr), detail)
			}
			return reviewExecution{}, fmt.Errorf("run restricted reviewer %s: %w", motivo, errors.Join(waitErr, ctxErr))
		}
		if detail != "" {
			return reviewExecution{}, fmt.Errorf("run restricted reviewer: %w: %s", waitErr, detail)
		}
		return reviewExecution{}, waitErr
	}
	raw := spawn.stdout.String()
	switch {
	case c.esOpenCode():
		scan, err := scanOpenCodeReview(strings.NewReader(raw))
		if err != nil {
			return reviewExecution{}, err
		}
		return reviewExecutionFromScan(scan.Output, scan.Usage, scan.UsageJSON, scan.StopReason), nil
	case c.esClaude():
		scan, err := scanClaudeReview(strings.NewReader(raw))
		if err != nil {
			return reviewExecution{}, err
		}
		return reviewExecutionFromScan(scan.Output, scan.Usage, scan.UsageJSON, scan.StopReason), nil
	default:
		// generic providers answer in plain text; there is nothing to scan.
		return reviewExecution{output: strings.TrimSpace(raw)}, nil
	}
}

// reviewExecutionFromScan projects a provider scan onto the rich review
// observation. The single TrimSpace lives here so every provider's
// stdout->answer boundary behaves byte-identically.
func reviewExecutionFromScan(output string, usage *acpadapter.Usage, usageJSON, stopReason string) reviewExecution {
	return reviewExecution{
		output:     strings.TrimSpace(output),
		usage:      usage,
		usageJSON:  usageJSON,
		stopReason: stopReason,
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
