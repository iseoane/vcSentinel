package main

import (
	"context"
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func executeRunsStatus(salida io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("status", args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	backing, controller, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitInfrastructure
	}
	if options.runID == "" {
		return listExecutions(salida, backing, options.jsonOut)
	}
	return inspectExecution(salida, controller, agentrun.Identity(options.runID), options.jsonOut)
}

func listExecutions(salida io.Writer, backing *store.Store, comoJSON bool) int {
	ids, err := backing.ListExecutionIDs()
	if err != nil {
		fmt.Fprintf(salida, "❌ Could not list executions: %v\n", err)
		return runExitCode(err)
	}
	entries := make([]runsListEntry, 0, len(ids))
	for _, id := range ids {
		projection, projectionErr := backing.ReadDerivedProjection(id)
		if projectionErr != nil {
			fmt.Fprintf(salida, "❌ Could not read the projection of %s: %v\n", id, projectionErr)
			return runExitCode(projectionErr)
		}
		entries = append(entries, runsListEntry{
			RunID: projection.RunID, State: projection.State,
			OutcomeClass: projection.Terminal, Revision: projection.Revision,
		})
	}
	if comoJSON {
		if encodeErr := encodeStableJSON(salida, struct {
			Runs []runsListEntry `json:"runs"`
		}{Runs: entries}); encodeErr != nil {
			fmt.Fprintf(salida, "❌ Could not serialize the listing: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	if len(entries) == 0 {
		fmt.Fprintln(salida, "📭 No durable runs are recorded.")
		return runExitSuccess
	}
	for _, entry := range entries {
		outcome := string(entry.OutcomeClass)
		if outcome == "" {
			outcome = "-"
		}
		fmt.Fprintf(salida, "• %s  state=%s outcome=%s revision=%d\n", entry.RunID, entry.State, outcome, entry.Revision)
	}
	return runExitSuccess
}

func inspectExecution(salida io.Writer, controller *execution.Controller, runID agentrun.Identity, comoJSON bool) int {
	inspection, err := controller.Inspect(context.Background(), runID)
	if err != nil {
		fmt.Fprintf(salida, "❌ Could not inspect %s: %v\n", runID, err)
		return runExitCode(err)
	}
	summary := runsStatusSummary{
		RunID: string(runID), State: inspection.Projection.State,
		Sequence: inspection.Projection.Sequence, Revision: inspection.Projection.Revision,
		EventCount: len(inspection.Events), Outcomes: inspection.Outcomes, Responses: inspection.Responses,
	}
	if len(inspection.Events) > 0 {
		summary.JobID = inspection.Events[0].JobID
	}
	if comoJSON {
		if encodeErr := encodeStableJSON(salida, summary); encodeErr != nil {
			fmt.Fprintf(salida, "❌ Could not serialize the inspection: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	fmt.Fprintf(salida, "🔎 Run %s\n   job %s\n   state %s (sequence %d, revision %d)\n   events %d, outcomes %d, responses %d\n",
		summary.RunID, summary.JobID, summary.State, summary.Sequence, summary.Revision,
		summary.EventCount, len(summary.Outcomes), len(summary.Responses))
	for _, outcome := range summary.Outcomes {
		fmt.Fprintf(salida, "   • attempt %s → %s%s\n", outcome.InvocationID, outcome.Class, outcomeDetailSuffix(outcome.Error))
	}
	return runExitSuccess
}

func outcomeDetailSuffix(errorText string) string {
	if errorText == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", errorText)
}

func executeRunsLogs(salida io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("logs", args)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitUsage
	}
	if options.runID == "" {
		fmt.Fprintln(salida, "❌ Usage: sentinel runs logs --run <id> [--after <cursor>] [--limit N]")
		return runExitUsage
	}
	backing, _, err := buildReadonlyController(worktree)
	if err != nil {
		fmt.Fprintf(salida, "❌ %v\n", err)
		return runExitInfrastructure
	}
	page, err := backing.ReadEvents(options.runID, options.afterCursor, options.limit)
	if err != nil {
		fmt.Fprintf(salida, "❌ Could not read the event log of %s: %v\n", options.runID, err)
		return runExitCode(err)
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
		if encodeErr := encodeStableJSON(salida, output); encodeErr != nil {
			fmt.Fprintf(salida, "❌ Could not serialize the event log: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	for _, frame := range output.Events {
		fmt.Fprintf(salida, "• #%d rev=%d %s→%s [%s] invocation=%s\n",
			frame.Sequence, frame.Revision, frame.From, frame.To, frame.Decision, frame.InvocationID)
	}
	if output.HasMore {
		fmt.Fprintf(salida, "➡️  next-cursor: %d (resume with --after %d)\n", *output.NextCursor, *output.NextCursor)
	} else {
		fmt.Fprintln(salida, "🏁 End of the event log.")
	}
	return runExitSuccess
}
