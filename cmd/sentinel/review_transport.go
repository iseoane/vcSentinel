package main

import (
	"fmt"
	"io"
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

// reviewRunAnnouncer surfaces each durable review run identity the moment the
// shared transport admits it, so an operator can immediately run
// `sentinel runs attach --run <id> --follow` while the review is still
// executing. It belongs to the `sentinel review` command boundary only: the
// gate cutover keeps its silent child sink and PR branch analysis keeps its
// unwired factory, so neither inherits this output. Parallel dimensions admit
// runs from different goroutines, so every access is mutex-guarded, and a
// repeated observation of the same identity prints exactly once.
type reviewRunAnnouncer struct {
	mu   sync.Mutex
	seen map[string]bool
	out  io.Writer
}

// newReviewRunAnnouncer builds an announcer over out. Production passes
// os.Stderr: the JSON-safe channel, so `sentinel review --json` consumers
// reading stdout keep parsing valid payloads while live lines stream by.
func newReviewRunAnnouncer(out io.Writer) *reviewRunAnnouncer {
	return &reviewRunAnnouncer{seen: make(map[string]bool), out: out}
}

// observe is the WithRunObserver callback: invoked synchronously immediately
// after durable admission, on the auditing goroutine before any wait. Start
// launches the provider worker independently of this callback, so the
// announcement can never prevent or delay provider execution. The line
// carries the canonical attach command so the ID is immediately actionable.
func (a *reviewRunAnnouncer) observe(runID string) {
	if runID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seen[runID] {
		return
	}
	a.seen[runID] = true
	fmt.Fprintf(a.out, "vas-sentinel: durable review run admitted %s; follow it live with `sentinel runs attach --run %s --follow`\n", runID, runID)
}

// announcedReviewTransport builds the engine-side review transport
// `sentinel review` uses, wiring the admission-time announcer at this command
// boundary through the shared construction path's extraOptions seam. A
// missing git common dir degrades to the same always-failing honest transport
// as durableReviewTransport: each dimension surfaces it as unavailable
// evidence instead of a silent direct-call fallback.
func announcedReviewTransport(cfg config.Config, worktree, sha string, paths []string, out io.Writer) review.ReviewTransport {
	announcer := newReviewRunAnnouncer(out)
	transport := nuevoDurableReviewTransport(cfg, worktree, sha, paths,
		store.RunPolicy{ID: durableRunPolicyID},
		[]reviewexec.DurableTransportOption{reviewexec.WithRunObserver(announcer.observe)})
	if transport == nil {
		return func(string, string, string, review.AuditorAgente) (string, string, error) {
			return "", "", fmt.Errorf("durable review transport unavailable for %s: no git common dir", worktree)
		}
	}
	return cerrarTransporteRevision(transport)
}

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
		restricted, ok := agente.(reviewexec.PolicyRestrictedReviewer)
		if !ok {
			return "", "", review.ErrRestrictedRequired
		}
		policyProvider, ok := agente.(reviewexec.PolicyProvider)
		if !ok {
			return "", "", review.ErrRestrictedRequired
		}
		// Ticket 07 slice 2b: the verified durable evidence travels with the
		// output so the engine can bind findings to their producing invocation.
		output, evidence, err := transport.RunWithPolicy(restricted, bundleName+"/"+dimension, prompt, policyProvider.ReviewToolPolicy())
		if err != nil {
			return "", "", err
		}
		return output, evidence.InvocationID, nil
	}
}

// durableReviewTransport builds the engine-side transport closure every
// production review wiring uses (ticket 13, R11: the review.durable_runs
// switch was removed, so the transport is unconditional and never nil). A
// missing git common dir cannot silently degrade to a direct-call path that
// no longer exists: it returns an always-failing transport whose error each
// dimension surfaces as honest unavailable evidence.
func durableReviewTransport(cfg config.Config, worktree, sha string, paths []string) review.ReviewTransport {
	transport := nuevoDurableReviewTransport(cfg, worktree, sha, paths, store.RunPolicy{ID: durableRunPolicyID}, nil)
	if transport == nil {
		return func(string, string, string, review.AuditorAgente) (string, string, error) {
			return "", "", fmt.Errorf("durable review transport unavailable for %s: no git common dir", worktree)
		}
	}
	return cerrarTransporteRevision(transport)
}

// nuevoDurableReviewTransport constructs the shared durable review transport
// over the repository common-dir store with the ticket 07/08 construction-time
// seams threaded from configuration. extraOptions lets callers attach
// additional admission-time observers (the gate cutover's child sink) without
// forking the construction path. It returns nil only when the git common dir
// cannot be resolved; callers translate that into an honest failure instead
// of a silent execution-path fallback.
func nuevoDurableReviewTransport(cfg config.Config, worktree, sha string, paths []string, policy store.RunPolicy, extraOptions []reviewexec.DurableTransportOption) *reviewexec.DurableTransport {
	// JD-A1 fix: the engine sanitizes its own copy per audit, so a transport
	// bound before dispatch must apply the identical safe-list rule or the
	// durable path would forward raw caller paths to reviewers.
	safePaths := review.RutasRevisionSeguras(paths)
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vas-sentinel: durable review transport unavailable, no git common dir: %v\n", err)
		return nil
	}
	st := store.NuevoStore(gitCommonDir)
	// Ticket 07 slice 3: evidence admission is threaded construction-time from
	// review.evidence_admission (default true). Ticket 08 slice 3: bounded
	// cancellation escalation threads from review.cancellation_escalation the
	// same way (default true). Both flags are construction-time rollback
	// seams over the admitted transport, which every reviewer call takes.
	options := append([]reviewexec.DurableTransportOption{
		reviewexec.WithEvidenceAdmission(cfg.Review.EvidenceAdmission),
		reviewexec.WithCancellationEscalation(execution.EscalationPolicy{Disabled: !cfg.Review.CancellationEscalation}),
	}, extraOptions...)
	return reviewexec.NewDurableTransport(st, policy, sha, safePaths, options...)
}

// applyDurableCutover wires the durable gate orchestration (ticket 11 slice
// 3, made unconditional by ticket 13 R11 when gate.durable_runs was removed)
// onto an already-assembled gate.Opciones value. It routes the whole gate
// execution through ONE root durable run backed by the SAME repository
// common-dir store the review transport uses, and installs a REAL
// DurableReviewTransportFactory that threads the root run ID into the shared
// transport's RunPolicy as the persisted ParentRunID. The factory's observer
// sink learns every actually-admitted review run so the orchestrator can
// enumerate them in the root settlement. A missing git common dir leaves the
// durable store unwired, so EjecutarGate fails honestly as infrastructure —
// there is no legacy path to degrade to anymore. It returns the sink (nil
// when wiring failed) for observability and tests.
func applyDurableCutover(opciones *gate.Opciones, cfg config.Config, worktree, stage, sha string, archivos []string) *reviewChildSink {
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vas-sentinel: gate durable runs unavailable for this execution, no git common dir: %v\n", err)
		return nil
	}
	sink := &reviewChildSink{}
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
