package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/gate"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// durableRunPolicyID identifies every review-side durable run admitted by
// this wiring.
const durableRunPolicyID = "policy:review"

// reviewRunAnnouncer surfaces each durable review run identity the moment the
// shared transport admits it, so an operator can immediately run
// `vcsentinel runs attach --run <id> --follow` while the review is still
// executing. It belongs to the `vcsentinel review` command boundary only: the
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
// os.Stderr: the JSON-safe channel, so `vcsentinel review --json` consumers
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
	fmt.Fprintf(a.out, "vas-sentinel: durable review run admitted %s; follow it live with `vcsentinel runs attach --run %s --follow`\n", runID, runID)
}

// announcedReviewTransport builds the engine-side review transport
// `vcsentinel review` uses, wiring the admission-time announcer at this command
// boundary through the shared construction path's extraOptions seam. A
// missing git common dir degrades to the same always-failing honest transport
// as durableReviewTransport: each dimension surfaces it as unavailable
// evidence instead of a silent direct-call fallback.
func announcedReviewTransport(cfg config.Config, worktree, sha string, paths []string, out io.Writer) review.ReviewTransport {
	rich := announcedReviewTransportWithEvidence(cfg, worktree, sha, paths, out)
	return func(bundleName, dimension, prompt string, agent review.AgentReviewer) (string, string, error) {
		output, evidence, err := rich(bundleName, dimension, prompt, agent)
		return output, evidence.InvocationID, err
	}
}

func announcedReviewTransportWithEvidence(cfg config.Config, worktree, sha string, paths []string, out io.Writer) review.ReviewTransportWithEvidence {
	rich, _ := announcedReviewTransportWithMetrics(cfg, worktree, sha, paths, out)
	return rich
}

func announcedReviewTransportWithMetrics(cfg config.Config, worktree, sha string, paths []string, out io.Writer) (review.ReviewTransportWithEvidence, review.MetricsFinalizer) {
	announcer := newReviewRunAnnouncer(out)
	transport := newDurableReviewTransport(cfg, worktree, sha, paths,
		store.RunPolicy{ID: durableRunPolicyID, Operation: "review", Commit: shortCommit(sha), Worktree: worktree},
		[]reviewexec.DurableTransportOption{reviewexec.WithRunObserver(announcer.observe)})
	if transport == nil {
		return func(string, string, string, review.AgentReviewer) (string, review.ReviewEvidence, error) {
			return "", review.ReviewEvidence{}, fmt.Errorf("durable review transport unavailable for %s: no git common dir", worktree)
		}, nil
	}
	rich := reviewTransportClosureWithEvidence(transport)
	finalize := func(runID, invocationID, failureClass, detail string) error {
		var failures []store.ExecutionFailure
		if failureClass != "" {
			failures = []store.ExecutionFailure{{
				InvocationID: invocationID,
				Class:        store.FailureClass(failureClass),
				Detail:       detail,
			}}
		}
		_, err := transport.FinalizeMetricsForDisposition(context.Background(), runID, failures)
		return err
	}
	return rich, finalize
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

// reviewTransportClosure wraps a durable transport into the engine-side
// review.ReviewTransport closure both production wirings share (`vcsentinel
// review` and the gate cutover): restricted-capability enforcement plus
// verified-evidence identity threading. One construction site keeps the two
// paths from drifting.
// reviewTransportClosure wraps a durable transport into the historical
// engine-side callback while preserving the same rich evidence seam used by
// production finalization.
func reviewTransportClosure(transport *reviewexec.DurableTransport) review.ReviewTransport {
	rich := reviewTransportClosureWithEvidence(transport)
	return func(bundleName, dimension, prompt string, agent review.AgentReviewer) (string, string, error) {
		output, evidence, err := rich(bundleName, dimension, prompt, agent)
		return output, evidence.InvocationID, err
	}
}

func reviewTransportClosureWithEvidence(transport *reviewexec.DurableTransport) review.ReviewTransportWithEvidence {
	return func(bundleName, dimension, prompt string, agent review.AgentReviewer) (string, review.ReviewEvidence, error) {
		restricted, ok := agent.(reviewexec.PolicyRestrictedReviewer)
		if !ok {
			return "", review.ReviewEvidence{}, review.ErrRestrictedRequired
		}
		policyProvider, ok := agent.(reviewexec.PolicyProvider)
		if !ok {
			return "", review.ReviewEvidence{}, review.ErrRestrictedRequired
		}
		output, evidence, err := transport.RunWithPolicy(restricted, bundleName+"/"+dimension, prompt, policyProvider.ReviewToolPolicy())
		return output, review.ReviewEvidence{RunID: evidence.RunID, InvocationID: evidence.InvocationID}, err
	}
}

// durableReviewTransport builds the engine-side transport closure every
// production review wiring uses (ticket 13, R11: the review.durable_runs
// switch was removed, so the transport is unconditional and never nil). A
// missing git common dir cannot silently degrade to a direct-call path that
// no longer exists: it returns an always-failing transport whose error each
// dimension surfaces as honest unavailable evidence.
func durableReviewTransport(cfg config.Config, worktree, sha string, paths []string) review.ReviewTransport {
	rich, _ := durableReviewTransportWithMetrics(cfg, worktree, sha, paths)
	return func(bundleName, dimension, prompt string, agent review.AgentReviewer) (string, string, error) {
		output, evidence, err := rich(bundleName, dimension, prompt, agent)
		return output, evidence.InvocationID, err
	}
}

func durableReviewTransportWithMetrics(cfg config.Config, worktree, sha string, paths []string) (review.ReviewTransportWithEvidence, review.MetricsFinalizer) {
	transport := newDurableReviewTransport(cfg, worktree, sha, paths,
		store.RunPolicy{ID: durableRunPolicyID, Operation: "review", Commit: shortCommit(sha), Worktree: worktree}, nil)
	if transport == nil {
		return func(string, string, string, review.AgentReviewer) (string, review.ReviewEvidence, error) {
			return "", review.ReviewEvidence{}, fmt.Errorf("durable review transport unavailable for %s: no git common dir", worktree)
		}, nil
	}
	rich := reviewTransportClosureWithEvidence(transport)
	finalize := func(runID, invocationID, failureClass, detail string) error {
		var failures []store.ExecutionFailure
		if failureClass != "" {
			failures = []store.ExecutionFailure{{
				InvocationID: invocationID,
				Class:        store.FailureClass(failureClass),
				Detail:       detail,
			}}
		}
		_, err := transport.FinalizeMetricsForDisposition(context.Background(), runID, failures)
		return err
	}
	return rich, finalize
}

// newDurableReviewTransport constructs the shared durable review transport
// over the repository common-dir store with the ticket 07/08 construction-time
// seams threaded from configuration. extraOptions lets callers attach
// additional admission-time observers (the gate cutover's child sink) without
// forking the construction path. It returns nil only when the git common dir
// cannot be resolved; callers translate that into an honest failure instead
// of a silent execution-path fallback.
func newDurableReviewTransport(cfg config.Config, worktree, sha string, paths []string, policy store.RunPolicy, extraOptions []reviewexec.DurableTransportOption) *reviewexec.DurableTransport {
	// JD-A1 fix: the engine sanitizes its own copy per audit, so a transport
	// bound before dispatch must apply the identical safe-list rule or the
	// durable path would forward raw caller paths to reviewers.
	safePaths := review.SafeReviewPaths(paths)
	gitCommonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vas-sentinel: durable review transport unavailable, no git common dir: %v\n", err)
		return nil
	}
	st := store.NewStore(gitCommonDir)
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
// onto an already-assembled gate.Options value. It routes the whole gate
// execution through ONE root durable run backed by the SAME repository
// common-dir store the review transport uses, and installs a REAL
// DurableReviewTransportFactory that threads the root run ID into the shared
// transport's RunPolicy as the persisted ParentRunID. The factory's observer
// sink learns every actually-admitted review run so the orchestrator can
// enumerate them in the root settlement. A missing git common dir leaves the
// durable store unwired, so EjecutarGate fails honestly as infrastructure —
// there is no legacy path to degrade to anymore. It returns the sink (nil
// when wiring failed) for observability and tests.
func applyDurableCutover(options *gate.Options, worktree, stage, sha string) {
	gitCommonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vas-sentinel: gate durable runs unavailable for this execution, no git common dir: %v\n", err)
		return
	}
	options.Stage = stage
	options.CandidateSHA = sha
	// One store directory for the root run and the validation-job
	// settlements: root+children linkage stays inside one directory so
	// reconstruction from store contents alone is possible.
	//
	// Piece 3 removed the review transport this function also used to wire.
	// The durable store itself is NOT review machinery — RunGate refuses to
	// execute without it — so it stays, and only the review-side factories,
	// the child sink and the parent-linked review policy are gone.
	options.DurableStore = store.NewStore(gitCommonDir)
}

// shortCommit keeps the activity label compact without assuming callers pass a
// full Git SHA; tests and error paths legitimately use short candidates.
func shortCommit(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}
