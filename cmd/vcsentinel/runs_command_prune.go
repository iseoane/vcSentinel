package main

import (
	"errors"
	"fmt"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runsPruneOutput is the stable JSON shape of `runs prune`. Decisions reuses
// the store's PruneDecision contract; reasons are the stable retention
// strings documented in docs/design/runs-cli.md.
type runsPruneOutput struct {
	Cutoff    time.Time             `json:"cutoff"`
	Examined  int                   `json:"examined"`
	Pruned    int                   `json:"pruned"`
	Kept      int                   `json:"kept"`
	Decisions []store.PruneDecision `json:"decisions"`
}

// executeRunsPrune implements `vcsentinel runs prune --older-than <duration>`.
// Pruning is an explicit operator maintenance action: nothing in vcsentinel
// ever purges execution records automatically. Every examined run appears
// in the report — removals and refusals alike — so the operator sees exactly
// what was deleted and why anything was kept.
func executeRunsPrune(out io.Writer, worktree string, args []string) int {
	options, err := parseRunOptions("prune", args)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitUsage
	}
	if !options.olderThanSet || strings.TrimSpace(options.olderThan) == "" {
		fmt.Fprintln(out, "❌ "+runsPruneUsage)
		return runExitUsage
	}
	maxAge, parseErr := time.ParseDuration(strings.TrimSpace(options.olderThan))
	if parseErr != nil || maxAge <= 0 {
		fmt.Fprintf(out, "❌ flag --older-than requires a positive duration (for example 720h), received %q\n", options.olderThan)
		return runExitUsage
	}
	backing, err := buildRunsStore(worktree)
	if err != nil {
		fmt.Fprintf(out, "❌ %v\n", err)
		return runExitInfrastructure
	}
	references, refsErr := collectProvenanceReferences(worktree, backing)
	if refsErr != nil {
		fmt.Fprintf(out, "❌ Could not collect review provenance references: %v\n", refsErr)
		return runExitInfrastructure
	}
	report, pruneErr := backing.PruneExecutions(time.Now().Add(-maxAge), references)
	if pruneErr != nil {
		fmt.Fprintf(out, "❌ Could not prune executions: %v\n", pruneErr)
		return runExitInfrastructure
	}
	output := runsPruneOutput{
		Cutoff: report.Cutoff, Examined: report.Examined,
		Pruned: report.Pruned, Kept: report.Kept, Decisions: report.Decisions,
	}
	if options.jsonOut {
		if encodeErr := encodeStableJSON(out, output); encodeErr != nil {
			fmt.Fprintf(out, "❌ Could not serialize the prune report: %v\n", encodeErr)
			return runExitInfrastructure
		}
		return runExitSuccess
	}
	fmt.Fprintf(out, "🧹 examined %d execution record(s) with cutoff %s: pruned %d, kept %d\n",
		output.Examined, output.Cutoff.Format(time.RFC3339), output.Pruned, output.Kept)
	for _, decision := range output.Decisions {
		fmt.Fprintf(out, "   • %s %s (%s)\n", decision.RunID, decision.Action, decision.Reason)
	}
	return runExitSuccess
}

// collectProvenanceReferences merges every invocation identity that review
// evidence still cites: persisted finding blobs and the append-only review
// ledger records (per-dimension results, their raw v2 Hallazgos — including
// refutation-downgraded ones, whose admitted refuter invocation travels in
// Hallazgo.InvocationID — and aggregated findings). Aggregation drops
// refuted findings, so scanning AggregatedFindings alone would miss the
// refuter stream; the raw Dims hallazgos close that gap. Any read failure
// fails closed — a prune must never run while provenance is unreadable,
// because that is exactly how referenced streams get destroyed.
func collectProvenanceReferences(worktree string, backing *store.Store) (map[string]bool, error) {
	references, directories, err := provenanceLedgerSource(worktree, backing)
	if err != nil {
		return nil, err
	}
	for _, gitDir := range directories {
		if err := annotateLedgerReferences(review.NewLedger(gitDir), references); err != nil {
			return nil, err
		}
	}
	return references, nil
}

// ledgerV1Directories enumerates every legacy per-checkout review ledger that
// belongs to this repository: the common directory, plus one per linked worktree.
// Current review, status, and pr records use the common directory; the linked
// worktree paths remain only for legacy pruning. Reading only the common
// directory made a legacy record written from a worktree absent rather than
// unreadable, so the fail-closed guard above never fired and a prune could
// destroy the very streams it protects.
//
// Enumerated with os.ReadDir and NOT with filepath.Glob. filepath.Glob
// reports only ErrBadPattern and silently swallows the I/O errors it hits
// while reading directories, so a static pattern over an unreadable
// `worktrees` directory returns an empty list and a nil error. That is
// indistinguishable from a repository with no linked worktrees, and it would
// fail open in the one place whose whole contract is to fail closed. A glob
// that misses a directory only defers work a later run can repeat; here it
// destroys.
//
// An absent `worktrees` directory is the ordinary case for a repository with no
// linked worktrees and is not a failure. Anything else is.
func ledgerV1Directories(gitCommonDir string) ([]string, error) {
	directories := []string{gitCommonDir}
	root := filepath.Join(gitCommonDir, "worktrees")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return directories, nil
	}
	if err != nil {
		return nil, fmt.Errorf("enumerating linked worktree ledgers in %s: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // not a worktree administrative directory
		}
		gitDir := filepath.Join(root, entry.Name())
		path := filepath.Join(gitDir, "vcsentinel")

		// Absence and malformation are separated with Lstat before Stat. Stat
		// alone follows symlinks, so a dangling ledger symlink reports
		// ErrNotExist and would be skipped as "never wrote a record", and a
		// regular file in its place would fail an IsDir check silently. Either
		// would hide provenance behind the same silent-absence hole this
		// function was just rewritten to close.
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue // that worktree never wrote a record
		} else if err != nil {
			return nil, fmt.Errorf("checking the ledger path of linked worktree %s: %w", entry.Name(), err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("the ledger path of linked worktree %s exists but cannot be resolved: %w", entry.Name(), err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("the ledger path of linked worktree %s is not a directory", entry.Name())
		}
		directories = append(directories, gitDir)
	}
	return directories, nil
}

// annotateLedgerReferences accumulates into references every invocation the
// records of one ledger cite. Any unreadable record fails closed: a prune must
// never run while provenance is only partly known.
func annotateLedgerReferences(ledger *review.Ledger, references map[string]bool) error {
	shas, err := ledger.ListRecords()
	if err != nil {
		return err
	}
	for _, sha := range shas {
		record, err := ledger.ReadRecord(sha)
		if err != nil {
			return fmt.Errorf("review ledger record %s is unreadable: %v", sha, err)
		}
		annotateRecordReferences(record, references)
	}
	return nil
}

// annotateRecordReferences accumulates into references every invocation one
// record cites: per-dimension producer invocations, raw v2 hallazgo
// invocations (including refutation-downgraded ones), and aggregated
// findings. A nil record cites nothing. Shared by the full collector and by
// retention, which skips published records before reaching it, so both count
// the same identities for the records they keep.
func annotateRecordReferences(record *review.Record, references map[string]bool) {
	if record == nil {
		return
	}
	for _, rev := range record.Revisions {
		for _, dim := range rev.Dims {
			if dim.InvocationID != "" {
				references[dim.InvocationID] = true
			}
		}
		for _, finding := range rev.AggregatedFindings {
			if finding.InvocationID != "" {
				references[finding.InvocationID] = true
			}
		}
	}
}
