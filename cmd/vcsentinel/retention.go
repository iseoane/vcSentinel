// Event-driven execution retention (T9.5).
//
// Once a commit is published — an ancestor of origin/main — its execution
// streams are in-flight detail with no operational reader left: review
// dimensions were corrected, the work shipped with its guarantees.
// Retention collects those streams so the store stops growing with every
// review.
//
// Commits that vanished from every ref (rebase/amend/squash, then gc) flow
// through the existing two-step path, not through this pass: `status
// --prune` already deletes their records and events, and the next retention
// pass collects their now-uncited runs. Treating an unresolvable object as
// "vanished" here would reopen FU-15 (a corrupt object reads exactly like a
// collected one while HEAD stays readable), so an undecidable publication
// keeps that record's protection instead.
//
// What it collects, and what it deliberately does not:
//
//   - Execution streams whose invocations no unpublished review record
//     cites, through the same provenance collector `runs prune` uses, minus
//     the published records. The store's own guards decide each run:
//     terminal, measured (its metrics snapshot exists and agrees with
//     its terminal outcomes), single-attempt. Anything else stays with
//     its stable reason.
//   - Review records stay. Findings, dispositions, and remediation evidence
//     live in the record and feed `vcsentinel metrics`; deleting them would
//     move the aggregates retention promises to leave untouched. Records are
//     kilobytes — the 134 MB that motivated this work is execution streams.
//   - The ops event log stays. T9.2 forbids rewriting historical
//     events.jsonl, and stage/remediation aggregates read it. Retention is
//     the admitted compaction only for execution directories, never for
//     the append-only log.
//
// The one guarantee that matters: retention is invisible to measurement.
// `vcsentinel metrics` over the surviving store is byte-identical before and
// after a retention pass.
//
// Retention is best-effort and never blocks the operation that triggered
// it (rebase, gate --stage pre-push): any failure skips the pass with a
// one-line note on the caller's writer.
package main

import (
	"fmt"
	"io"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// retainPublishedDetail collects the execution streams of published
// commits: terminal runs no unpublished review record cites, provided their
// metrics snapshot survives them. It returns the prune report, the SHAs
// treated as published, and the SHAs kept as undecidable because their
// publication could not be answered. Any hard failure fails closed —
// nothing is deleted on an unanswerable query.
func retainPublishedDetail(worktree string) (store.PruneReport, []string, []string, error) {
	// Classify nothing against a repository that cannot answer: without
	// this anchor every publication query could read as "unpublished" and
	// keep everything, or worse, a redirected repository could answer for
	// commits it does not own. The anchor comes before the store is even
	// opened, so no decision input exists yet when it fails.
	if err := git.RequireUsableRepository(worktree); err != nil {
		return store.PruneReport{}, nil, nil, err
	}
	backing, err := buildRunsStore(worktree)
	if err != nil {
		return store.PruneReport{}, nil, nil, err
	}
	references, published, undecidable, err := collectUnpublishedProvenanceReferences(worktree, backing)
	if err != nil {
		return store.PruneReport{}, nil, nil, err
	}
	report, err := backing.PruneExecutions(time.Now(), references)
	if err != nil {
		return store.PruneReport{}, published, undecidable, err
	}
	return report, published, undecidable, nil
}

// provenanceLedgerSource opens the shared provenance inputs every collector
// needs: invocation identities cited by persisted finding blobs, plus every
// v1 ledger directory in the repository. One bootstrap for both collectors
// so their universes cannot drift: deciding what to keep from one ledger
// set while another collector reads a different one is the FU-12 failure.
func provenanceLedgerSource(worktree string, backing *store.Store) (map[string]bool, []string, error) {
	references, err := backing.ReferencedInvocationIDs()
	if err != nil {
		return nil, nil, err
	}
	gitCommonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		return nil, nil, err
	}
	directories, err := ledgerV1Directories(gitCommonDir)
	if err != nil {
		return nil, nil, err
	}
	return references, directories, nil
}

// collectUnpublishedProvenanceReferences merges every invocation identity
// that UNPUBLISHED review evidence still cites: persisted finding blobs and
// the records of every ledger whose commit is not an ancestor of
// origin/main. Invocations cited only by published records lose their
// protection, which is what makes their streams collectible; everything
// else keeps the exact fail-closed semantics of
// collectProvenanceReferences.
//
// One undecidable record never vetoes the pass: its references stay
// annotated (nothing it cites is released on a guess) and collection
// continues with the rest. Aborting everything on one unresolvable object
// would let a single damaged record disable retention on every push.
//
// Linked worktrees resolve through the same shared refs and object store:
// publication is a property of the commit graph, not of the checkout that
// asks, so one worktree parameter classifies every ledger's records
// identically. That sharing is what makes per-ledger enumeration safe here
// instead of per-checkout classification (the FU-12 trap in reverse).
func collectUnpublishedProvenanceReferences(worktree string, backing *store.Store) (map[string]bool, []string, []string, error) {
	references, directories, err := provenanceLedgerSource(worktree, backing)
	if err != nil {
		return nil, nil, nil, err
	}
	var published, undecidable []string
	for _, gitDir := range directories {
		ledger := review.NewLedger(gitDir)
		shas, err := ledger.ListRecords()
		if err != nil {
			return nil, nil, nil, err
		}
		for _, sha := range shas {
			// Every record is read before any branch: readability is
			// decided uniformly, so a corrupt published record aborts
			// exactly like a corrupt unpublished one instead of
			// slipping through the branch that never cites it.
			record, err := ledger.ReadRecord(sha)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("review ledger record %s is unreadable: %v", sha, err)
			}
			isPublished, err := git.PublishedToRemote(worktree, sha)
			if err != nil {
				// Undecidable publication keeps this record's
				// protection: annotate it as unpublished and move on,
				// but record it. A systemic cause (no origin/main
				// anywhere) would otherwise protect every record with
				// no visible trace — the silent total skip. The
				// reporter turns this list into a note the operator
				// can see. See the FU-15 note on PublishedToRemote
				// for why an error is never read as "vanished".
				undecidable = append(undecidable, sha)
				annotateRecordReferences(record, references)
				continue
			}
			if isPublished {
				published = append(published, sha)
				continue
			}
			annotateRecordReferences(record, references)
		}
	}
	return references, published, undecidable, nil
}

// tryRetentionAfterPublish runs one retention pass without ever
// failing the operation that triggered it. A skipped pass reports one line
// on out; a pass that collected nothing is silent unless records were kept
// as undecidable, which always reports; a pass that collected streams
// reports one line on out. Every line goes to the caller's writer, never
// around it.
func tryRetentionAfterPublish(out io.Writer, worktree string) {
	report, published, undecidable, err := retainPublishedDetail(worktree)
	if err != nil {
		fmt.Fprintf(out, "retention skipped: %v\n", err)
		return
	}
	if report.Pruned == 0 && len(undecidable) == 0 {
		return
	}
	if report.Pruned > 0 {
		fmt.Fprintf(out, "retention: collected %d execution stream(s) for %d published commit(s); review records and metrics snapshots kept.\n",
			report.Pruned, len(published))
	}
	if len(undecidable) > 0 {
		fmt.Fprintf(out, "retention: %d review record(s) kept as undecidable (publication unknown).\n", len(undecidable))
	}
}
