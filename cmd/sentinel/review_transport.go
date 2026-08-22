package main

import (
	"fmt"
	"os"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// durableRunPolicyID identifies every review-side durable run admitted by
// this wiring.
const durableRunPolicyID = "policy:review"

// reviewTransportFactory returns the per-commit factory wired into branch
// analysis: every audited commit gets its own transport bound to its own SHA
// and touched paths.
func reviewTransportFactory(cfg config.Config, worktree string) func(sha string, paths []string) review.ReviewTransport {
	return func(sha string, paths []string) review.ReviewTransport {
		return durableReviewTransport(cfg, worktree, sha, paths)
	}
}

// durableReviewTransport builds the engine-side transport closure when the
// configuration enables durable runs (review.durable_runs); nil keeps the
// legacy scheduler, which is the construction-time rollback seam of ticket
// 05. Store failures surface later as honest unavailable evidence instead of
// being swallowed here.
func durableReviewTransport(cfg config.Config, worktree, sha string, paths []string) review.ReviewTransport {
	if !cfg.Review.DurableRuns {
		return nil
	}
	// JD-A1 fix: the engine sanitizes its own copy per audit, so a transport
	// bound before dispatch must apply the identical safe-list rule or the
	// durable path would forward raw caller paths to reviewers.
	safePaths := review.RutasRevisionSeguras(paths)
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vas-sentinel: durable runs disabled for this audit, no git common dir: %v\n", err)
		return nil
	}
	st := store.NuevoStore(gitCommonDir)
	// Ticket 07 slice 3: evidence admission is threaded construction-time from
	// review.evidence_admission (default true). The flag is the only rollback
	// seam; when DurableRuns is false the legacy path runs untouched no matter
	// what this flag says.
	transport := reviewexec.NewDurableTransport(st, store.RunPolicy{ID: durableRunPolicyID}, sha, safePaths,
		reviewexec.WithEvidenceAdmission(cfg.Review.EvidenceAdmission))
	return func(bundleName, dimension, prompt string, agent review.AuditorAgente) (string, string, error) {
		restricted, ok := agent.(reviewexec.RestrictedReviewer)
		if !ok {
			return "", "", review.ErrRestrictedRequired
		}
		// Ticket 07 slice 2b: the verified durable evidence travels with the
		// output so the engine can bind findings to their producing invocation.
		output, evidence, err := transport.Run(restricted, bundleName+"/"+dimension, prompt)
		if err != nil {
			return "", "", err
		}
		return output, evidence.InvocationID, nil
	}
}
