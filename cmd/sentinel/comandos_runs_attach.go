package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Ticket 17 slice 1: `runs attach` is a pure observation surface over one
// durable run. Slice 1 renders plain-text snapshots; slice 2 replaces only
// the renderer with the Bubble Tea program while keeping this exact data
// pipeline (Inspect snapshot + cursor-based Subscribe replay + RunView).

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
// observation snapshot, optionally following the run live until its terminal
// state or a SIGINT/SIGTERM detaches cleanly.
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
	// cleanly, exactly as the command usage text promises.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	collector := attach.NewReplayCollector(options.afterCursor)
	identity := agentrun.Identity(options.runID)
	view, _, err := observeAttachSnapshot(ctx, host, identity, principal, collector)
	if err != nil {
		fmt.Fprintf(out, "❌ Could not observe %s: %v\n", options.runID, err)
		return runExitCode(err)
	}
	renderRunView(out, view)
	if !options.follow {
		return runExitSuccess
	}
	return followRunsView(ctx, out, host, identity, principal, collector, view)
}

// followRunsView polls one observed run until its terminal state or until ctx
// is cancelled (SIGINT/SIGTERM); detaching from a live run is a normal
// operator action and always exits cleanly with success.
func followRunsView(ctx context.Context, out io.Writer, host execution.RepositoryHost, identity agentrun.Identity, principal string, collector *attach.ReplayCollector, view attach.RunView) int {
	for !view.IsTerminal() {
		if interrupted(ctx) {
			// Detaching from a live run is a normal operator action, never a
			// failure: Ctrl-C exits cleanly with success.
			return runExitSuccess
		}
		time.Sleep(runsAttachPollInterval)
		next, nextChanged, observeErr := observeAttachSnapshot(ctx, host, identity, principal, collector)
		if observeErr != nil {
			if ctx.Err() != nil {
				return runExitSuccess
			}
			fmt.Fprintf(out, "❌ Could not follow %s: %v\n", string(identity), observeErr)
			return runExitCode(observeErr)
		}
		// Cursor movement is the complete change signal here: both the
		// durable projection and the admitted attempt outcomes are derived
		// from the same event stream Subscribe pages, and the store's
		// sidecar-before-log read order guarantees an observed outcome
		// record already has its terminal frame in the stream. A poll whose
		// cursor did not move can therefore only rebuild an identical view.
		if !nextChanged {
			continue
		}
		// Follow mode re-renders the FULL snapshot on every observed change;
		// slice 2 swaps this reprint for the TUI model update.
		view = next
		renderRunView(out, view)
	}
	return runExitSuccess
}

// interrupted reports whether the detach signal already fired.
func interrupted(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// observeAttachSnapshot rebuilds one full RunView: an Inspect snapshot for
// the projection and admitted outcomes, then Subscribe pages applied strictly
// after the collector's cursor until the stream is exhausted. The collector
// makes repeated observations idempotent, so polling between pages never
// duplicates evidence.
func observeAttachSnapshot(ctx context.Context, host execution.RepositoryHost, runID agentrun.Identity, principal string, collector *attach.ReplayCollector) (attach.RunView, bool, error) {
	before := collector.Cursor()
	inspection, err := host.Inspect(ctx, execution.InspectRequest{
		RunID:       runID,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if err != nil {
		return attach.RunView{}, false, err
	}
	pages := 0
	for {
		page, err := host.Subscribe(ctx, execution.SubscribeRequest{
			RunID:       runID,
			AfterCursor: collector.Cursor(),
			Limit:       runsLogsDefaultLimit,
			AuthContext: execution.AuthContext{Principal: principal},
		})
		if err != nil {
			return attach.RunView{}, false, err
		}
		collector.ApplyPage(page)
		if !page.HasMore {
			break
		}
		pages++
		if pages > runsAttachMaxPages {
			return attach.RunView{}, false, fmt.Errorf("event pagination for %s did not terminate after %d pages", runID, runsAttachMaxPages)
		}
	}
	view := attach.BuildRunView(collector.Frames(), inspection.Outcomes, inspection.Projection)
	return view, collector.Cursor() != before || view.IsTerminal(), nil
}

// renderRunView prints the stable multi-line plain-text observation report.
// It is deterministic by construction: no wall-clock timestamps are printed,
// so slice 2's golden views can pin this byte for byte.
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
