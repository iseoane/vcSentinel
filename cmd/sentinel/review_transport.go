package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// durableRunPolicyID identifies every review-side durable run admitted by
// this wiring.
const durableRunPolicyID = "policy:review"

// reviewChildSink records the durable run identity of every review-side run
// actually admitted during one gate execution (ticket 11 slice 3). Review
// candidate identities are process-salted inside the shared durable
// transport, so the gate orchestrator learns its real children here instead
// of deriving them from the plan. Parallel dimensions admit runs from
// different goroutines, so every access is mutex-guarded.
type reviewChildSink struct {
	mu  sync.Mutex
	ids []string
}

// observe is the WithRunObserver callback: invoked synchronously after each
// successful Start, before any completion can exist.
func (s *reviewChildSink) observe(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, runID)
}

// learned drains the sink as gate child identities in admission order.
func (s *reviewChildSink) learned() []agentrun.Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ids) == 0 {
		return nil
	}
	ids := make([]agentrun.Identity, 0, len(s.ids))
	for _, id := range s.ids {
		ids = append(ids, agentrun.Identity(id))
	}
	return ids
}

// reviewTransportFactory returns the per-commit factory wired into branch
// analysis: every audited commit gets its own transport bound to its own SHA
// and touched paths.
func reviewTransportFactory(cfg config.Config, worktree string) func(sha string, paths []string) review.ReviewTransport {
	return func(sha string, paths []string) review.ReviewTransport {
		return durableReviewTransport(cfg, worktree, sha, paths)
	}
}

// cerrarTransporteRevision wraps a durable transport into the engine-side
// review.ReviewTransport closure both production wirings share (`sentinel
// review` and the gate cutover): restricted-capability enforcement plus
// verified-evidence identity threading. One construction site keeps the two
// paths from drifting.
func cerrarTransporteRevision(transport *reviewexec.DurableTransport) review.ReviewTransport {
	return func(bundleName, dimension, prompt string, agente review.AuditorAgente) (string, string, error) {
		restricted, ok := agente.(reviewexec.RestrictedReviewer)
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

// durableReviewTransport builds the engine-side transport closure when the
// configuration enables durable runs (review.durable_runs); nil keeps the
// legacy scheduler, which is the construction-time rollback seam of ticket
// 05. Store failures surface later as honest unavailable evidence instead of
// being swallowed here.
func durableReviewTransport(cfg config.Config, worktree, sha string, paths []string) review.ReviewTransport {
	if !cfg.Review.DurableRuns {
		return nil
	}
	transport := nuevoDurableReviewTransport(cfg, worktree, sha, paths, store.RunPolicy{ID: durableRunPolicyID}, nil)
	if transport == nil {
		return nil
	}
	return cerrarTransporteRevision(transport)
}

// nuevoDurableReviewTransport constructs the shared durable review transport
// over the repository common-dir store with the ticket 07/08 construction-time
// seams threaded from configuration. extraOptions lets callers attach
// additional admission-time observers (the gate cutover's child sink) without
// forking the construction path.
func nuevoDurableReviewTransport(cfg config.Config, worktree, sha string, paths []string, policy store.RunPolicy, extraOptions []reviewexec.DurableTransportOption) *reviewexec.DurableTransport {
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
	// review.evidence_admission (default true). Ticket 08 slice 3: bounded
	// cancellation escalation threads from review.cancellation_escalation the
	// same way (default true). Both flags are construction-time rollback
	// seams; when DurableRuns is false the legacy path runs untouched no
	// matter what they say.
	options := append([]reviewexec.DurableTransportOption{
		reviewexec.WithEvidenceAdmission(cfg.Review.EvidenceAdmission),
		reviewexec.WithCancellationEscalation(execution.EscalationPolicy{Disabled: !cfg.Review.CancellationEscalation}),
	}, extraOptions...)
	return reviewexec.NewDurableTransport(st, policy, sha, safePaths, options...)
}

// applyDurableCutover wires the gate.durable_runs cutover (ticket 11 slice 3)
// onto an already-assembled gate.Opciones value. TRUE (the configuration
// default — this unit IS the switch-over) routes the whole gate execution
// through ONE root durable run backed by the SAME repository common-dir store
// the review transport uses, and installs a REAL DurableReviewTransportFactory
// that threads the root run ID into the shared transport's RunPolicy as the
// persisted ParentRunID. The factory's observer sink learns every
// actually-admitted review run so the orchestrator can enumerate them in the
// root settlement. FALSE restores the legacy orchestration byte-for-byte:
// nothing is touched and previously written durable history stays inspectable
// via `sentinel runs`. A missing git common dir degrades to the legacy path
// exactly like `sentinel review` does today. It returns the sink (nil when
// the cutover stayed off) for observability and tests.
func applyDurableCutover(opciones *gate.Opciones, cfg config.Config, worktree, stage, sha string, archivos []string) *reviewChildSink {
	if !cfg.Gate.DurableRuns {
		return nil
	}
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vas-sentinel: gate durable runs disabled for this execution, no git common dir: %v\n", err)
		return nil
	}
	sink := &reviewChildSink{}
	opciones.DurableRuns = true
	opciones.Stage = stage
	opciones.CandidateSHA = sha
	// One store directory for the root run, the validation-job settlements,
	// and every routed review invocation (each transport holds its own
	// stateless handle over it): root+children linkage stays inside one
	// directory so reconstruction from store contents alone is possible.
	durableStore := store.NuevoStore(gitCommonDir)
	opciones.DurableStore = durableStore
	opciones.DurableReviewChildren = sink.learned
	opciones.DurableReviewTransportFactory = func(rootRunID agentrun.Identity) review.ReviewTransport {
		policy := store.RunPolicy{ID: durableRunPolicyID, ParentRunID: string(rootRunID)}
		transport := nuevoDurableReviewTransport(cfg, worktree, sha, archivos, policy,
			[]reviewexec.DurableTransportOption{reviewexec.WithRunObserver(sink.observe)})
		if transport == nil {
			return nil
		}
		return cerrarTransporteRevision(transport)
	}
	return sink
}
