// Package process owns spawned provider processes as whole trees from birth
// (ticket 08 slice 2). On Debian every child is born into its own process
// group; on Windows it is attached to a kill-on-close job object. One narrow
// build-tagged seam keeps the contract identical on both supported platforms:
// no best-effort fallbacks, no unowned children.
//
// The package also hosts the bounded escalation state machine: after a
// cancellation context fires, the tree receives one cooperative grace budget,
// then a hard whole-tree termination, and each transition is reported exactly
// once so callers can append durable evidence without double-writes.
package process

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"time"
)

const (
	// DefaultGrace is the cooperative window a canceled child gets to exit
	// on its own before the controller escalates against the whole tree.
	DefaultGrace = 5 * time.Second
	// DefaultFinalBudget bounds how long a hard termination may take before
	// the attempt is honestly declared orphaned instead of reaped.
	DefaultFinalBudget = 5 * time.Second
	// containmentMargin is the extra headroom the in-adapter containment
	// watchdog waits beyond the controller's grace budget, so controller-
	// authored escalation evidence always wins that race.
	containmentMargin = 1 * time.Second
)

// Tree is the ownership handle of one spawned child and its descendants.
// The zero value is not usable; trees are created only through Owner.Assign.
type Tree struct {
	mu       sync.Mutex
	pid      int
	exited   chan struct{}
	exitOnce sync.Once
	platform platformTree
}

// platformTree is the build-tagged payload of one owned tree. It is declared
// portably and implemented once per supported platform.
type platformTree interface {
	// attach binds post-start state (pid capture on Linux; job assignment
	// plus main-thread resume on Windows). It runs exactly once per tree.
	attach(proc *os.Process) error
	// terminate hard-kills every process of the tree identified by pid.
	terminate(pid int) error
	// alive reports whether any process of the tree may still be running.
	alive(pid int) bool
	// release frees platform resources after exit confirmation.
	release()
}

// Attach binds the started process to its ownership handle. Callers must run
// it exactly once after exec.Cmd.Start succeeds.
func (t *Tree) Attach(proc *os.Process) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pid = proc.Pid
	if t.platform == nil {
		return nil
	}
	return t.platform.attach(proc)
}

// Pid returns the recorded direct-child pid, or zero before Attach.
func (t *Tree) Pid() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pid
}

// Exited exposes the channel closed exactly once once the direct child's exit
// has been confirmed by the spawner through MarkExited.
func (t *Tree) Exited() <-chan struct{} {
	return t.exited
}

// MarkExited confirms the direct child has been waited on. Escalation treats
// closure as reap confirmation.
func (t *Tree) MarkExited() {
	t.exitOnce.Do(func() { close(t.exited) })
}

// Release frees platform resources. It is safe to call after exit
// confirmation and must never be called before: on Windows closing the job
// object kills its remaining processes by design.
func (t *Tree) Release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.platform != nil {
		t.platform.release()
	}
}

// Alive reports whether any process of this tree may still be running. A
// direct child that exited but has not been waited on still reports alive;
// exit confirmation is MarkExited's job.
func (t *Tree) Alive() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.platform == nil || t.pid <= 0 {
		return false
	}
	return t.platform.alive(t.pid)
}

// Terminate hard-kills the whole tree: SIGTERM to the negative pgid followed
// by SIGKILL to the negative pgid on Linux, TerminateJobObject on Windows.
// Calling it on an already-dead tree is harmless.
func Terminate(tree *Tree) error {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.platform == nil || tree.pid <= 0 {
		return nil
	}
	return tree.platform.terminate(tree.pid)
}

// OwnerConfig carries the construction options of the platform owner.
type OwnerConfig struct {
	// KillDelay separates the cooperative TERM signal from the hard KILL
	// inside Terminate on POSIX platforms. Zero selects a small default.
	KillDelay time.Duration
}

// Owner is the platform process-tree ownership seam. Implementations live in
// build-tagged files so no other package ever touches platform specifics.
type Owner interface {
	// Assign installs platform ownership into a not-yet-started command and
	// returns its tree handle.
	Assign(cmd *exec.Cmd) (*Tree, error)
	// Terminate hard-kills an owned tree.
	Terminate(tree *Tree) error
}

// NewOwner creates the platform owner with the given configuration. Every
// owned spawn uses its own owner instance, which matches the platform model:
// one Linux process group or one Windows job object per spawned tree.
func NewOwner(config OwnerConfig) Owner {
	return newPlatformOwner(config)
}

// Spawn starts name/args as an owned tree bound to ctx. When the stamped
// policy permits whole-tree termination, the automatic CommandContext kill is
// disabled: cooperative-first semantics are provided by the escalation
// watchdogs, because the default kill would strike only the direct child and
// leak descendants. When the policy restricts kills to the direct child, the
// default CommandContext kill stays active and is exactly the sanctioned
// mechanism. The configure hook runs before Start so callers can wire
// environment, directory, and stdio; after Spawn returns the child is running
// and callers must call cmd.Wait and then tree.MarkExited().
func Spawn(ctx context.Context, name string, args []string, configure func(*exec.Cmd)) (*exec.Cmd, *Tree, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if configure != nil {
		configure(cmd)
	}
	if WholeTreeTermination(ctx) {
		// Cooperative-first: no automatic direct-child kill; escalation and
		// the containment watchdog own every termination below this point.
		cmd.Cancel = func() error { return nil }
	}
	tree, err := NewOwner(OwnerConfig{}).Assign(cmd)
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		// Abandon the assigned ownership: on Windows Release closes the
		// kill-on-close job handle instead of leaking it; on Linux it is a
		// no-op.
		tree.Release()
		return nil, nil, err
	}
	if err := tree.Attach(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		tree.Release()
		return nil, nil, err
	}
	return cmd, tree, nil
}

// WithContainmentGrace stamps an enabled containment policy onto the worker
// context: the shared grace budget plus permission to terminate the whole
// tree when nobody confirms exit within grace+margin.
func WithContainmentGrace(ctx context.Context, grace time.Duration) context.Context {
	return WithContainmentPolicy(ctx, grace, true)
}

// WithContainmentPolicy stamps the cancellation-termination policy onto a
// worker context so the spawn seam and the containment watchdog share one
// budget and one kill scope with the controller. wholeTree=false restricts
// every kill downstream of this context to the DIRECT CHILD: the automatic
// CommandContext kill stays active and no code path may signal the tree.
func WithContainmentPolicy(ctx context.Context, grace time.Duration, wholeTree bool) context.Context {
	if grace <= 0 {
		grace = DefaultGrace
	}
	return context.WithValue(ctx, containmentKey{}, containmentPolicy{grace: grace, wholeTree: wholeTree})
}

type containmentPolicy struct {
	grace     time.Duration
	wholeTree bool
}

func containmentFrom(ctx context.Context) containmentPolicy {
	if policy, ok := ctx.Value(containmentKey{}).(containmentPolicy); ok {
		if policy.grace <= 0 {
			policy.grace = DefaultGrace
		}
		return policy
	}
	return containmentPolicy{grace: DefaultGrace, wholeTree: true}
}

// ContainmentGrace reads the stamped grace budget, defaulting when absent.
func ContainmentGrace(ctx context.Context) time.Duration {
	return containmentFrom(ctx).grace
}

// WholeTreeTermination reports whether the stamped policy permits signaling
// beyond the direct child. Absence of any policy means yes: callers that
// never restricted themselves keep full escalation capability.
func WholeTreeTermination(ctx context.Context) bool {
	return containmentFrom(ctx).wholeTree
}

// ContainmentDeadline returns how long after context cancellation the
// containment watchdog waits before hard-killing the tree on its own.
func ContainmentDeadline(ctx context.Context) time.Duration {
	return ContainmentGrace(ctx) + containmentMargin
}

type containmentKey struct{}
