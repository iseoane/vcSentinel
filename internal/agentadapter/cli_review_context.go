package agentadapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
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
// a provider that never answers. The child is born into an owned process
// tree, and the containment watchdog guarantees that even without the
// controller's escalation the tree never outlives its context by more than
// the shared grace budget plus a fixed margin.
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

	spawn, err := startOwnedCommand(ctx, c.BinaryName, args, env, dir, request.Prompt)
	if err != nil {
		return "", err
	}
	c.activeTree.Store(spawn.tree)
	defer c.activeTree.Store(nil)
	waitErr := spawn.cmd.Wait()
	spawn.tree.MarkExited()
	spawn.tree.Release()
	if waitErr != nil {
		detail := strings.TrimSpace(spawn.stderr.String())
		if detail != "" {
			return "", fmt.Errorf("run restricted reviewer: %w: %s", waitErr, detail)
		}
		return "", waitErr
	}
	return strings.TrimSpace(spawn.stdout.String()), nil
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
