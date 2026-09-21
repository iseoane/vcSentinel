package daemon

import (
	"errors"
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// ReconcileOnBoot runs the R8 recovery machinery exactly once before a
// starting daemon begins serving, so a restart mid-run always leaves the
// repository in classified, honestly settled state instead of silently
// rewritten bytes.
//
// Action policy mirrors the existing `runs recover --repair` CLI path without
// duplicating its decisions: the automatic settlement vocabulary lives in
// internal/store, and the only class it settles automatically is
// terminal_unprojected, through store.RepairTerminalUnprojected (the very
// primitive repairRunProjection invokes). Every other class keeps its CLI
// semantics at boot:
//
//   - operator_required rows are surfaced untouched, carrying their exact
//     missing-evidence reason; repairing them stays an explicit operator
//     command after the daemon is up.
//   - recoverable, orphaned_canceled, and corrupt streams stay untouched too:
//     resuming launches adapter calls and rewriting corrupt bytes are both
//     deliberate operator actions, so boot only logs them informationally.
//
// One line per entry is written to out, either describing the applied action
// or the reason nothing was applied. The returned error aggregates ONLY
// infrastructure failures (a failed scan, a failed repair); untouched
// operator-required evidence is informational at boot and never fails
// startup.
//
// The controller parameter is the very controller the daemon will serve;
// today's actions derive purely from durable evidence, and the parameter is
// kept so boot reconciliation stays anchored to one serving lifecycle and
// later slices can grow controller-aware checks without breaking callers.
func ReconcileOnBoot(controller *execution.Controller, backing *store.Store, out io.Writer) error {
	entries, err := store.ScanRecoveries(backing)
	if err != nil {
		return fmt.Errorf("daemon: boot reconciliation scan failed: %w", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "🧭 boot reconciliation: no interrupted durable runs require attention")
		return nil
	}
	var infrastructure []error
	for _, entry := range entries {
		switch entry.Class {
		case store.RecoveryTerminalUnprojected:
			result, repairErr := store.RepairTerminalUnprojected(backing, entry.RunID)
			if repairErr != nil {
				infrastructure = append(infrastructure,
					fmt.Errorf("daemon: boot repair of run %s failed: %w", entry.RunID, repairErr))
				fmt.Fprintf(out, "❌ boot repair failed for run %s (class %s): %v\n",
					entry.RunID, entry.Class, repairErr)
				continue
			}
			fmt.Fprintf(out, "🔧 settled run %s: repaired projection (%s → %s)\n",
				result.RunID, result.ClassBefore, result.ClassAfter)
		case store.RecoveryOperatorRequired:
			fmt.Fprintf(out, "⏸ run %s needs an operator decision (class %s): %s\n",
				entry.RunID, entry.Class, entry.Reason)
		default:
			// recoverable, orphaned_canceled, corrupt: the shared repair
			// vocabulary has no automatic action for these classes, so boot
			// surfaces them informationally exactly as the scan would.
			fmt.Fprintf(out, "ℹ️ run %s left untouched (class %s): %s\n",
				entry.RunID, entry.Class, entry.Reason)
		}
	}
	return errors.Join(infrastructure...)
}
