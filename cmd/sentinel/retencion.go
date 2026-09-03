// Event-driven execution retention (T9.5).
//
// Once a commit is published — an ancestor of origin/main — or has vanished
// from every ref, its execution streams are in-flight detail with no
// operational reader left: review dimensions were corrected, the work
// shipped with its guarantees. Retention collects those streams so the
// store stops growing with every review.
//
// What it collects, and what it deliberately does not:
//
//   - Execution streams whose invocations no unpublished review record
//     cites, through the same provenance collector `runs prune` uses, minus
//     the published fichas. The store's own guards decide each run:
//     terminal, old enough, measured (its metrics snapshot exists),
//     single-attempt. Anything else stays with its stable reason.
//   - Review fichas stay. Findings, dispositions, and remediation evidence
//     live in the ficha and feed `sentinel metrics`; deleting them would
//     move the aggregates retention promises to leave untouched. Fichas are
//     kilobytes — the 134 MB that motivated this work is execution streams.
//   - The ops event log stays. T9.2 forbids rewriting historical
//     events.jsonl, and stage/remediation aggregates read it. Retention is
//     the admitted compaction only for execution directories, never for
//     the append-only log.
//
// The one guarantee that matters: retention is invisible to measurement.
// `sentinel metrics` over the surviving store is byte-identical before and
// after a retention pass.
//
// Retention is best-effort and never blocks the operation that triggered
// it (rebase, gate --stage pre-push): any failure skips the pass with a
// one-line note on stderr.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// retenerDetallePublicado collects the execution streams of published
// commits: terminal runs no unpublished review record cites, provided their
// metrics snapshot survives them. It returns the prune report and the SHAs
// whose records were treated as published. Any failure fails closed —
// nothing is deleted on an unanswerable query.
func retenerDetallePublicado(worktree string) (store.PruneReport, []string, error) {
	backing, err := buildRunsStore(worktree)
	if err != nil {
		return store.PruneReport{}, nil, err
	}
	// Classify nothing against a repository that cannot answer: without
	// this anchor every publication query could read as "unpublished" and
	// keep everything, or worse, a redirected repository could answer for
	// commits it does not own.
	if err := git.RepositorioUsable(worktree); err != nil {
		return store.PruneReport{}, nil, err
	}
	references, published, err := collectUnpublishedProvenanceReferences(worktree, backing)
	if err != nil {
		return store.PruneReport{}, nil, err
	}
	report, err := backing.PruneExecutions(time.Now(), references)
	if err != nil {
		return store.PruneReport{}, published, err
	}
	return report, published, nil
}

// collectUnpublishedProvenanceReferences merges every invocation identity
// that UNPUBLISHED review evidence still cites: persisted finding blobs and
// the fichas of every ledger whose commit is not an ancestor of
// origin/main. Invocations cited only by published fichas lose their
// protection, which is what makes their streams collectible; everything
// else keeps the exact fail-closed semantics of
// collectProvenanceReferences. Fichas whose publication cannot be decided
// abort the collection: an unanswerable query never silently keeps, and
// never silently releases, a stream.
func collectUnpublishedProvenanceReferences(worktree string, backing *store.Store) (map[string]bool, []string, error) {
	references, err := backing.ReferencedInvocationIDs()
	if err != nil {
		return nil, nil, err
	}
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return nil, nil, err
	}
	directorios, err := directoriosLedgerV1(gitCommonDir)
	if err != nil {
		return nil, nil, err
	}
	var published []string
	for _, gitDir := range directorios {
		ledger := review.NuevoLedger(gitDir)
		shas, err := ledger.ListarFichas()
		if err != nil {
			return nil, nil, err
		}
		for _, sha := range shas {
			esPublicado, err := git.PublicadoEnRemoto(worktree, sha)
			if err != nil {
				return nil, nil, fmt.Errorf("cannot decide publication of review record %s: %w", sha, err)
			}
			if esPublicado {
				published = append(published, sha)
				continue
			}
			ficha, err := ledger.LeerFicha(sha)
			if err != nil {
				return nil, nil, fmt.Errorf("review ledger ficha %s is unreadable: %v", sha, err)
			}
			anotarReferenciasDeFicha(ficha, references)
		}
	}
	return references, published, nil
}

// intentarRetencionTrasPublicacion runs one retention pass without ever
// failing the operation that triggered it. A skipped pass is a note on
// stderr; a pass that collected nothing is silent; a pass that collected
func intentarRetencionTrasPublicacion(out io.Writer, worktree string) {
	report, published, err := retenerDetallePublicado(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "retention skipped: %v\n", err)
		return
	}
	if report.Pruned == 0 {
		return
	}
	fmt.Fprintf(out, "retention: collected %d execution stream(s) for %d published commit(s); review records and metrics snapshots kept.\n",
		report.Pruned, len(published))
}
