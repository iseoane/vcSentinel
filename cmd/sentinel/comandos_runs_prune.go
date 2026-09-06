package main

import (
	"errors"
	"fmt"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
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

// executeRunsPrune implements `sentinel runs prune --older-than <duration>`.
// Pruning is an explicit operator maintenance action: nothing in sentinel
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
		fmt.Fprintln(out, "❌ "+usoRunsPrune)
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
// ledger fichas (per-dimension results, their raw v2 hallazgos — including
// refutation-downgraded ones, whose admitted refuter invocation travels in
// Hallazgo.InvocationID — and aggregated findings). Aggregation drops
// refuted findings, so scanning AggregatedFindings alone would miss the
// refuter stream; the raw Dims hallazgos close that gap. Any read failure
// fails closed — a prune must never run while provenance is unreadable,
// because that is exactly how referenced streams get destroyed.
func collectProvenanceReferences(worktree string, backing *store.Store) (map[string]bool, error) {
	references, directorios, err := provenanceLedgerSource(worktree, backing)
	if err != nil {
		return nil, err
	}
	for _, gitDir := range directorios {
		if err := anotarReferenciasDeLedger(review.NuevoLedger(gitDir), references); err != nil {
			return nil, err
		}
	}
	return references, nil
}

// directoriosLedgerV1 enumerates every gitDir whose v1 review ledger belongs to
// this repository: the common directory, plus one per linked worktree.
//
// review.NuevoLedger anchors on the checkout's gitDir, so the main checkout
// writes to <gitCommonDir>/vas-sentinel only because its two paths coincide,
// while a linked worktree writes to <gitCommonDir>/worktrees/<name>/vas-sentinel.
// Reading the common directory alone made a ficha written from a worktree
// absent rather than unreadable, so the fail-closed guard above never fired and
// a prune could destroy the very streams it protects. Delegating to a writer in
// a dedicated worktree is the mandated workflow here, so that was the normal
// path (FU-12).
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
func directoriosLedgerV1(gitCommonDir string) ([]string, error) {
	directorios := []string{gitCommonDir}
	raiz := filepath.Join(gitCommonDir, "worktrees")
	entradas, err := os.ReadDir(raiz)
	if errors.Is(err, fs.ErrNotExist) {
		return directorios, nil
	}
	if err != nil {
		return nil, fmt.Errorf("enumerating linked worktree ledgers in %s: %w", raiz, err)
	}
	for _, entrada := range entradas {
		if !entrada.IsDir() {
			continue // not a worktree administrative directory
		}
		gitDir := filepath.Join(raiz, entrada.Name())
		ruta := filepath.Join(gitDir, "vas-sentinel")

		// Absence and malformation are separated with Lstat before Stat. Stat
		// alone follows symlinks, so a dangling ledger symlink reports
		// ErrNotExist and would be skipped as "never wrote a ficha", and a
		// regular file in its place would fail an IsDir check silently. Either
		// would hide provenance behind the same silent-absence hole this
		// function was just rewritten to close.
		if _, err := os.Lstat(ruta); errors.Is(err, fs.ErrNotExist) {
			continue // that worktree never wrote a ficha
		} else if err != nil {
			return nil, fmt.Errorf("checking the ledger path of linked worktree %s: %w", entrada.Name(), err)
		}
		info, err := os.Stat(ruta)
		if err != nil {
			return nil, fmt.Errorf("the ledger path of linked worktree %s exists but cannot be resolved: %w", entrada.Name(), err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("the ledger path of linked worktree %s is not a directory", entrada.Name())
		}
		directorios = append(directorios, gitDir)
	}
	return directorios, nil
}

// anotarReferenciasDeLedger accumulates into references every invocation the
// fichas of one ledger cite. Any unreadable ficha fails closed: a prune must
// never run while provenance is only partly known.
func anotarReferenciasDeLedger(ledger *review.Ledger, references map[string]bool) error {
	shas, err := ledger.ListarFichas()
	if err != nil {
		return err
	}
	for _, sha := range shas {
		ficha, err := ledger.LeerFicha(sha)
		if err != nil {
			return fmt.Errorf("review ledger ficha %s is unreadable: %v", sha, err)
		}
		anotarReferenciasDeFicha(ficha, references)
	}
	return nil
}

// anotarReferenciasDeFicha accumulates into references every invocation one
// ficha cites: per-dimension producer invocations, raw v2 hallazgo
// invocations (including refutation-downgraded ones), and aggregated
// findings. A nil ficha cites nothing. Shared by the full collector and by
// retention, which skips published fichas before reaching it, so both count
// the same identities for the fichas they keep.
func anotarReferenciasDeFicha(ficha *review.Ficha, references map[string]bool) {
	if ficha == nil {
		return
	}
	for _, rev := range ficha.Revisions {
		for _, dim := range rev.Dims {
			if dim.InvocationID != "" {
				references[dim.InvocationID] = true
			}
			for _, hallazgo := range dim.Hallazgos {
				if hallazgo.InvocationID != "" {
					references[hallazgo.InvocationID] = true
				}
			}
		}
		for _, hallazgo := range rev.AggregatedFindings {
			if hallazgo.InvocationID != "" {
				references[hallazgo.InvocationID] = true
			}
		}
	}
}
