package agentadapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = TimeoutComando
	}
	snapshot, safePaths, cleanup, err := createReviewSnapshot("", sha, paths)
	if err != nil {
		return "", err
	}
	defer cleanup()
	request := ReviewRequest{Prompt: prompt, SHA: sha, Paths: safePaths, SnapshotDir: snapshot, MaxToolCalls: defaultReviewToolCalls}
	return c.ejecutarRevision(ctx, request, timeout)
}

// ejecutarRevision spawns the restricted reviewer under a combined budget:
// the parent context governs cooperative cancellation and the timeout bounds
// a provider that never answers. Whichever limit fires first kills the direct
// child through runCapturedCommand's context-bound exec.
func (c *CLIAdapter) ejecutarRevision(parent context.Context, request ReviewRequest, timeout time.Duration) (string, error) {
	ctx, cancelar := context.WithTimeout(parent, timeout)
	defer cancelar()

	args, restrictions, err := c.reviewCommand(request)
	if err != nil {
		return "", err
	}
	env := os.Environ()
	dir := ""
	if c.esClaude() {
		// Claude Code has no "--dir"-style flag (unlike OpenCode's --pure +
		// --dir), so confinement to the read-only snapshot happens through
		// the working directory. --safe-mode already disables CLAUDE.md,
		// skills, plugins, hooks, MCP servers, and custom agents, so unlike
		// OpenCode's reviewEnvironment (which redirects HOME/XDG because
		// opencode has no equivalent flag) no HOME/XDG redirection is needed
		// here.
		dir = request.SnapshotDir
	} else {
		env = reviewEnvironment(restrictions["OPENCODE_CONFIG_CONTENT"], request.SnapshotDir, c.Config.Model)
	}

	out, stderr, err := runCapturedCommand(ctx, c.BinaryName, args, env, dir, request.Prompt)
	if err != nil {
		if detail := strings.TrimSpace(stderr); detail != "" {
			return "", fmt.Errorf("run restricted reviewer: %w: %s", err, detail)
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runCapturedCommand is the single spawn point of the restricted reviewer:
// one context-bound exec.CommandContext whose direct child dies whenever ctx
// is canceled, feeding stdin and capturing stdout and stderr. Keeping it as a
// plain function gives tests a real-subprocess seam without adding any
// interface indirection to production callers.
func runCapturedCommand(ctx context.Context, name string, args []string, env []string, dir, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Dir = dir
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader(stdin)
	if err := cmd.Run(); err != nil {
		return out.String(), stderr.String(), err
	}
	return out.String(), "", nil
}
