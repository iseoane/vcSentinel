package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/tui"
)

// Ticket 17: `runs attach` is an observation surface over one durable run.
// Without --follow it renders a plain-text snapshot (list mode walks the
// store directly). With --follow it launches the Bubble Tea program over the
// SAME data pipeline (Inspect snapshot + cursor-based Subscribe replay +
// RunView): the TUI owns rendering, keyboard actions, bounded reconnect, and
// terminal freeze from there on.

// listAttachableRuns renders the attach picker: every durable run whose
// reconciled projection is still non-terminal, i.e. a run that may still make
// progress and is therefore worth observing live.
//
// Listing choice note: this deliberately walks ListExecutionIDs plus
// ReadReconciledProjection instead of reusing store.ScanRecoveries. The
// recovery scan answers "what needs repair attention", not "what is still
// alive": it includes corrupt or operator-required heads whose stream may
// really be finished (terminal-but-unprojected), which would offer dead runs
// as live picks, and its settled exclusion is keyed to repair classes rather
// than lifecycle truth. The reconciled projection walk applies exactly the
// lifecycle definition of non-terminal, inherits the owner-death
// reconciliation discipline of `runs status`, and stays a bounded read-only
// pass. It always exits 0 — an empty picker is a valid answer, not a failure.
func listAttachableRuns(out io.Writer, backing *store.Store) int {
	ids, err := backing.ListExecutionIDs()
	if err != nil {
		fmt.Fprintf(out, "❌ Could not list executions: %v\n", err)
		return runExitCode(err)
	}
	rows := 0
	for _, id := range ids {
		projection, projectionErr := backing.ReadReconciledProjection(id)
		if projectionErr != nil {
			fmt.Fprintf(out, "❌ Could not read the projection of %s: %v\n", id, projectionErr)
			return runExitCode(projectionErr)
		}
		if projection.Terminal != agentrun.TerminalNone {
			continue // settled work is not attachable
		}
		line := fmt.Sprintf("• %s  state=%s revision=%d head=#%d",
			projection.RunID, projection.State, projection.Revision, projection.Sequence)
		if projection.OrphanedCancellation {
			line += " (orphaned-canceled)"
		}
		fmt.Fprintln(out, line)
		rows++
	}
	if rows == 0 {
		fmt.Fprintln(out, "📭 No non-terminal durable runs are available to attach.")
	}
	return runExitSuccess
}

// executeRunsAttach dispatches `sentinel runs attach`. Without --run it lists
// non-terminal attach candidates; with --run it prints one plain-text
// observation snapshot, or — under --follow — hands the observed run to the
// Bubble Tea attach program, which exits 0 on any clean quit (q, ctrl+c,
// SIGINT/SIGTERM) and through the infrastructure code only when the bounded
// reconnect budget exhausts.
func executeRunsAttach(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("attach", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if options.follow && options.runID == "" {
		fmt.Fprintln(out, "❌ flag --follow requires --run")
		return runExitUsage
	}
	backing, controller, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	if options.runID == "" {
		return listAttachableRuns(out, backing)
	}
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()

	// Same signal pair as the daemon: both SIGINT and SIGTERM detach
	// cleanly, exactly as the command usage text promises. The TUI watches
	// this context too, so a mid-session signal quits as politely as q.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	collector := attach.NewReplayCollector(options.afterCursor)
	identity := agentrun.Identity(options.runID)
	view, _, err := tui.ObserveSnapshot(ctx, host, identity, principal, collector)
	if err != nil {
		fmt.Fprintf(out, "❌ Could not observe %s: %v\n", options.runID, err)
		return runExitCode(err)
	}
	renderRunView(out, view)
	if !options.follow {
		return runExitSuccess
	}
	return runAttachFollow(out, worktree, controller, host, closeRemote, identity, principal, collector, view, ctx)
}

// attachHostLease pairs one resolved repository host with its own teardown
// provenance: the release closure returned beside that exact host at dial
// time (the remote connection's Close, or a no-op for the in-process
// fallback). Keeping host and release together is what makes the replacement
// lifecycle structural: a lease can only ever be torn down through the
// teardown that belongs to IT, never through whatever release slot happens
// to be current when someone notices a failure.
type attachHostLease struct {
	host    execution.RepositoryHost
	release func()
}

// empty reports whether the lease holds no host at all.
func (l attachHostLease) empty() bool { return l.host == nil }

// close releases the leased host through the host's OWN Close() whenever its
// implementation exposes one, falling back to the paired release closure for
// hosts without a closer. For daemon.RemoteHost both paths are literally the
// same call, because its registered teardown is `func() { _ = host.Close() }`.
func (l attachHostLease) close() {
	if closer, ok := l.host.(io.Closer); ok {
		_ = closer.Close()
		return
	}
	if l.release != nil {
		l.release()
	}
}

// attachHostProvider implements tui.HostProvider over the CLI resolver. It
// caches ONE healthy host: repeated resolutions hand back the cached host
// without dialing or releasing anything, so concurrent observe and action
// commands can never have their connection closed underneath them.
//
// Replacement lifecycle (structural, not incidental): Reset never releases
// anything — it marks the current lease POISONED, meaning no future Host
// resolution may hand that connection out again while any exchange already
// holding it runs to completion untouched. The next Host dials the fresh
// host, SWAPS it into the current slot, and only then releases the poisoned
// lease via its own Close(). That ordering is safe by construction: an
// exchange can only be running on a host it resolved BEFORE the poisoning,
// and daemon.RemoteHost serializes every exchange behind its internal mutex,
// so Close acquires that lock only after any in-flight exchange completes —
// and a completed exchange cannot be harmed by a subsequent close. The
// provider therefore never invokes a teardown concurrently with a *startable*
// exchange (poisoned hosts are unresolvable), and the single overlap window
// that remains is arbitrated by the host's own lock instead of by hope.
type attachHostProvider struct {
	mu       sync.Mutex
	dial     func() (execution.RepositoryHost, func())
	current  attachHostLease // handed out to every resolution while healthy
	poisoned attachHostLease // replaced-but-unreleased lease awaiting the swap
}

// newAttachHostProvider adopts the initially dialed host as its first lease;
// its teardown runs when a reconnect replaces that host or when Close drains
// the session. Fresh dials after a Reset go through
// runsHostWithDaemonPreference, so they prefer a healthy daemon endpoint and
// degrade to the in-process host.
func newAttachHostProvider(worktree string, controller *execution.Controller, initial execution.RepositoryHost, releaseInitial func()) *attachHostProvider {
	return &attachHostProvider{
		dial: func() (execution.RepositoryHost, func()) {
			return runsHostWithDaemonPreference(worktree, controller)
		},
		current: attachHostLease{host: initial, release: releaseInitial},
	}
}

// Host resolves the session's repository host. While the cached lease is
// healthy every resolution returns it untouched; only a Reset in between
// makes this redial once. On the redial path the fresh host is swapped in
// FIRST (so every future resolution lands on it) and the poisoned lease is
// released AFTER the swap, through its own Close — see the type comment for
// why that ordering cannot harm an exchange still draining on the old
// connection. Resolution itself cannot fail — the dialer degrades to the
// in-process host — but the signature matches the TUI seam so scripted test
// doubles fit the same shape.
func (p *attachHostProvider) Host() (execution.RepositoryHost, error) {
	p.mu.Lock()
	cached := p.current
	if !cached.empty() && p.poisoned.empty() {
		p.mu.Unlock()
		return cached.host, nil
	}
	nextHost, nextRelease := p.dial()
	fresh := attachHostLease{host: nextHost, release: nextRelease}
	stale := p.poisoned
	p.poisoned = attachHostLease{}
	p.current = fresh // SWAP first: the poisoned host is now unreachable
	p.mu.Unlock()
	stale.close() // THEN release it via its own Close (mutex-serialized)
	return fresh.host, nil
}

// Reset poisons the cached host without any I/O: the marked connection stops
// being resolvable immediately, but its release is deferred to the next Host
// swap. Deferral is deliberate — releasing here could race an exchange that
// resolved before the failure was observed, while the deferred release goes
// through the host's own Close, whose internal lock orders it strictly after
// any such exchange completes.
func (p *attachHostProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.current.empty() {
		p.poisoned = p.current
		p.current = attachHostLease{}
	}
}

// Close drains whichever leases the provider still holds — a poisoned one
// awaiting its swap, plus the current one — each released exactly once
// through its own Close. Idempotent: later calls find nothing left.
func (p *attachHostProvider) Close() {
	p.mu.Lock()
	current, poisoned := p.current, p.poisoned
	p.current, p.poisoned = attachHostLease{}, attachHostLease{}
	p.mu.Unlock()
	poisoned.close()
	current.close()
}

// startAttachProgram launches the Bubble Tea attach program and returns the
// final model. It is a var-indirected seam: driving a real terminal
// headlessly is not possible, so tests stub this single point and pin the
// construction contract instead.
var startAttachProgram = func(model tui.Model) (tui.Model, error) {
	program := tea.NewProgram(model, tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return model, err
	}
	if typed, ok := final.(tui.Model); ok {
		return typed, nil
	}
	return model, nil
}

// runAttachFollow runs the TUI session over the already-observed initial
// snapshot. Clean quits exit 0 — detaching from a live run is a normal
// operator action; losing the endpoint beyond the bounded reconnect budget
// reports infrastructure failure honestly instead of looping forever.
func runAttachFollow(out io.Writer, worktree string, controller *execution.Controller, host execution.RepositoryHost, closeHost func(), identity agentrun.Identity, principal string, collector *attach.ReplayCollector, view attach.RunView, detach context.Context) int {
	provider := newAttachHostProvider(worktree, controller, host, closeHost)
	defer provider.Close()
	model := tui.New(identity, principal, provider, collector, view, detach)
	final, err := startAttachProgram(model)
	if err != nil {
		fmt.Fprintf(out, "❌ attach session for %s failed: %v\n", string(identity), err)
		return runExitInfrastructure
	}
	if final.LostContact() {
		fmt.Fprintf(out, "❌ lost contact with run %s after %d reconnect attempts\n",
			string(identity), tui.MaxReconnectAttempts)
		return runExitInfrastructure
	}
	return runExitSuccess
}

// renderRunView prints the stable multi-line plain-text observation report
// for the non-follow snapshot mode. It is deterministic by construction: no
// wall-clock timestamps are printed.
func renderRunView(out io.Writer, view attach.RunView) {
	fmt.Fprintf(out, "🔎 Run %s\n", view.RunID)
	if view.JobID != "" {
		fmt.Fprintf(out, "   job %s\n", view.JobID)
	}
	fmt.Fprintf(out, "   state %s (sequence %d, revision %d)\n", view.State, view.Sequence, view.Revision)
	if view.IsTerminal() {
		fmt.Fprintf(out, "   🏁 outcome %s\n", view.Outcome)
		if view.Error != "" {
			fmt.Fprintf(out, "      error: %s\n", view.Error)
		}
	} else {
		fmt.Fprintln(out, "   ⏳ still in flight")
	}
	fmt.Fprintf(out, "   invocations %d, responses %d\n", len(view.Invocations), len(view.Responses))
	for _, invocation := range view.Invocations {
		outcome := string(invocation.OutcomeClass)
		if outcome == "" {
			outcome = "-"
		}
		line := fmt.Sprintf("   • #%d %s decision=%s outcome=%s",
			invocation.Order, invocation.InvocationID, invocation.Decision, outcome)
		if invocation.ParentInvocationID != "" {
			line += fmt.Sprintf(" parent=%s", invocation.ParentInvocationID)
		}
		if invocation.HasOutputHash {
			line += " output-hash=present"
		}
		fmt.Fprintln(out, line)
		if invocation.Error != "" {
			fmt.Fprintf(out, "       error: %s\n", invocation.Error)
		}
	}
	for _, response := range view.Responses {
		fmt.Fprintf(out, "   • response → invocation=%s hash=%s\n", response.InvocationID, response.ResponseHash)
	}
}
