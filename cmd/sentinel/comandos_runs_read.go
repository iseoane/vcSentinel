package main

import (
	"context"
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func executeRunsStatus(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("status", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	backing, controller, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	if options.runID == "" {
		return listExecutions(out, backing, options.jsonOut)
	}
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
	return inspectExecution(out, host, backing, agentrun.Identity(options.runID), principal, options.jsonOut)
}

func listExecutions(out io.Writer, backing *store.Store, asJSON bool) int {
	ids, err := backing.ListExecutionIDs()
	if err != nil {
		fmt.Fprintf(out, "❌ Could not list executions: %v\n", err)
		return runExitCode(err)
	}
	entries := make([]runsListEntry, 0, len(ids))
	for _, id := range ids {
		// Ticket 08 slice 3: the listing is a next-observation surface, so it
		// reads through restart reconciliation — an owner death during
		// cancellation surfaces as its honest terminal view instead of a
		// phantom live run.
		projection, projectionErr := backing.ReadReconciledProjection(id)
		if projectionErr != nil {
			fmt.Fprintf(out, "❌ Could not read the projection of %s: %v\n", id, projectionErr)
			return runExitCode(projectionErr)
		}
		entries = append(entries, runsListEntry{
			RunID: projection.RunID, State: projection.State,
			OutcomeClass: projection.Terminal, Revision: projection.Revision,
			OrphanedCancellation: projection.OrphanedCancellation,
		})
	}
	if asJSON {
		if encodeErr := encodeStableJSON(out, struct {
			Runs []runsListEntry `json:"runs"`
		}{Runs: entries}); encodeErr != nil {
			fmt.Fprintf(out, "❌ Could not serialize the listing: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "📭 No durable runs are recorded.")
		return runExitSuccess
	}
	for _, entry := range entries {
		outcome := string(entry.OutcomeClass)
		if outcome == "" {
			outcome = "-"
		}
		line := fmt.Sprintf("• %s  state=%s outcome=%s revision=%d", entry.RunID, entry.State, outcome, entry.Revision)
		if entry.OrphanedCancellation {
			line += " (orphaned-canceled)"
		}
		fmt.Fprintln(out, line)
	}
	return runExitSuccess
}

func inspectExecution(out io.Writer, host execution.RepositoryHost, backing *store.Store, runID agentrun.Identity, principal string, asJSON bool) int {
	inspection, err := host.Inspect(context.Background(), execution.InspectRequest{
		RunID:       runID,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if err != nil {
		fmt.Fprintf(out, "❌ Could not inspect %s: %v\n", runID, err)
		return runExitCode(err)
	}
	state := inspection.Projection.State
	sequence := inspection.Projection.Sequence
	revision := inspection.Projection.Revision
	orphanedCanceled := false
	// Ticket 08 slice 3: when the durable stream proves owner death during
	// cancellation, the inspection shows the honest reconciled view instead
	// of the raw non-terminal head. Discarding the reconcile error here is
	// deliberate best-effort refinement: Inspect already succeeded on the
	// raw head, so a reconcile failure downgrades to showing that raw honest
	// state instead of failing the whole inspection.
	if reconciled, reconcileErr := backing.ReadReconciledProjection(string(runID)); reconcileErr == nil && reconciled.OrphanedCancellation {
		// Coherence rule: once reconciliation applies, every displayed
		// projection field comes from the reconciled view — never a mix of
		// raw and derived values.
		state = reconciled.State
		sequence = reconciled.Sequence
		revision = reconciled.Revision
		orphanedCanceled = true
	}
	summary := runsStatusSummary{
		RunID: string(runID), State: state,
		Sequence: sequence, Revision: revision,
		EventCount: len(inspection.Events), Outcomes: inspection.Outcomes, Responses: inspection.Responses,
		OrphanedCancellation: orphanedCanceled,
	}
	if len(inspection.Events) > 0 {
		summary.JobID = inspection.Events[0].JobID
	}
	if asJSON {
		if encodeErr := encodeStableJSON(out, summary); encodeErr != nil {
			fmt.Fprintf(out, "❌ Could not serialize the inspection: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	fmt.Fprintf(out, "🔎 Run %s\n   job %s\n   state %s (sequence %d, revision %d)\n   events %d, outcomes %d, responses %d\n",
		summary.RunID, summary.JobID, summary.State, summary.Sequence, summary.Revision,
		summary.EventCount, len(summary.Outcomes), len(summary.Responses))
	if orphanedCanceled {
		fmt.Fprintln(out, "   ⚠️ owner died during cancellation: reconciled as canceled-orphaned on restart")
	}
	for _, outcome := range summary.Outcomes {
		fmt.Fprintf(out, "   • attempt %s → %s%s\n", outcome.InvocationID, outcome.Class, outcomeDetailSuffix(outcome.Error))
	}
	return runExitSuccess
}

func outcomeDetailSuffix(errorText string) string {
	if errorText == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", errorText)
}

// listRecoveries renders the ticket 10 slice 1 recovery scan: one entry per
// non-terminal run with its evidence-based class. The scan itself is
// read-only; this surface only formats its entries. Exit codes follow the
// runs contract: 0 when nothing requires attention, 4 when at least one run
// needs an explicit operator decision (class operator_required). Corruption
// is reported in the table but stays informational here: repairing bytes is
// a deliberate operator action, and `runs verify` remains the integrity
// verdict that exits 5.
func listRecoveries(out io.Writer, backing *store.Store, asJSON bool) int {
	entries, err := store.ScanRecoveries(backing)
	if err != nil {
		fmt.Fprintf(out, "❌ Could not scan recoveries: %v\n", err)
		return runExitCode(err)
	}
	rows := make([]runsRecoveryRow, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, runsRecoveryRow{
			RunID: entry.RunID, Class: string(entry.Class), Reason: entry.Reason,
			HeadSequence: entry.HeadSequence, Reconciled: entry.Reconciled,
		})
	}
	if asJSON {
		output := runsRecoveryOutput{Recoveries: rows}
		if encodeErr := encodeStableJSON(out, output); encodeErr != nil {
			fmt.Fprintf(out, "❌ Could not serialize the recovery scan: %v\n", encodeErr)
			return runExitInfrastructure
		}
	} else if len(rows) == 0 {
		fmt.Fprintln(out, "📭 No non-terminal durable runs require recovery attention.")
	} else {
		for _, row := range rows {
			line := fmt.Sprintf("• %s  class=%s head=#%d  %s", row.RunID, row.Class, row.HeadSequence, row.Reason)
			if row.Reconciled {
				line += " (reconciled)"
			}
			fmt.Fprintln(out, line)
		}
	}
	for _, row := range rows {
		if row.Class == string(store.RecoveryOperatorRequired) {
			return runExitInvalidState
		}
	}
	return runExitSuccess
}

func executeRunsLogs(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("logs", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(out, "❌ "+usoRunsLogs)
		return runExitUsage
	}
	_, controller, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	principal, err := resolveRunsPrincipal()
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	host, closeRemote := runsHostWithDaemonPreference(worktree, controller)
	defer closeRemote()
	page, err := host.Subscribe(context.Background(), execution.SubscribeRequest{
		RunID:       agentrun.Identity(options.runID),
		AfterCursor: options.afterCursor,
		Limit:       options.limit,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if err != nil {
		fmt.Fprintf(out, "❌ Could not read the event log of %s: %v\n", options.runID, err)
		return runExitCode(err)
	}
	if page.Events == nil {
		page.Events = []store.EventFrame{}
	}
	output := runsLogsOutput{
		RunID: options.runID, AfterCursor: options.afterCursor, Limit: options.limit,
		Events: page.Events, HasMore: page.HasMore,
	}
	if page.HasMore {
		cursor := page.NextRevision
		output.NextCursor = &cursor
	}
	if options.jsonOut {
		if encodeErr := encodeStableJSON(out, output); encodeErr != nil {
			fmt.Fprintf(out, "❌ Could not serialize the event log: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	for _, frame := range output.Events {
		fmt.Fprintf(out, "• #%d rev=%d %s→%s [%s] invocation=%s\n",
			frame.Sequence, frame.Revision, frame.From, frame.To, frame.Decision, frame.InvocationID)
	}
	if output.HasMore {
		fmt.Fprintf(out, "➡️  next-cursor: %d (resume with --after %d)\n", *output.NextCursor, *output.NextCursor)
	} else {
		fmt.Fprintln(out, "🏁 End of the event log.")
	}
	return runExitSuccess
}
