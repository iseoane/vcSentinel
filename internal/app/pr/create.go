package pr

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// FlagsPrCreate carries the parsed `pr create` options across the dispatch
// boundary. cmd/sentinel owns the flag parsing (the package-main tests drive
// it); this struct is its counterpart here, so the fields are exported.
type FlagsPrCreate struct {
	Base    string
	ChainPR bool   // --chain-pr: publish the whole branch even if it is oversized
	Force   bool   // --force: override a red validation (T1.8: the only gate that blocks)
	Reason  string // --reason: explicit and mandatory motive next to --force
	Parent  string
	// AuditPending (--audit-pending) restores the old default: audit every
	// commit on the branch that carries no review record instead of only
	// reporting the gap (the unaudited-commits decision in docs/issues/decisions.md). The net audit
	// below is unconditional either way and is what actually gates.
	AuditPending bool
}

// PrCreateEventDetail builds the detail of the pr-create event (guide §13):
// the publication record with pr_url, fallback and chain_pr. Extends T1.8:
// force records whether the red validation was overridden, and reason (only
// with force) leaves an explicit trace of why — the exception is never
// silent. unaudited (the unaudited-commits decision in docs/issues/decisions.md) records how many
// branch commits carried no review record in this pass, so an operator
// reconstructing what happened from the event stream can tell an audited
// pass apart from a skipped one, not just the --json report.
func PrCreateEventDetail(prURL string, fallback, chain, force bool, reason string, unaudited int) (ops.EventDetail, error) {
	detail := ops.EventDetail{
		"pr_url":    prURL,
		"fallback":  fallback,
		"chain_pr":  chain,
		"force":     force,
		"unaudited": unaudited,
	}
	if force {
		detail["motivo"] = reason
	}
	return detail, nil
}

// DepsPrCreate groups the injectable seams of the pr create pipeline (T1.8):
// it lets tests exercise the ORDER (validation before auditing, zero tokens
// if it fails) without real git, agents or gh. In production the package-main
// wiring in cmd/sentinel resolves them, building the twin struct with the
// unexported fields the package-main tests construct; this is its exported
// equivalent inside the flow.
type DepsPrCreate struct {
	LoadConfig    func(worktree string) (config.Config, error)
	GetGitDir     func() (string, error)
	GetHeadSHA    func() (string, error)
	RunValidation func(profile string, scope []string, opts validation.RunOptions) ([]validation.ValidationRun, error)
	// AnalyzeBranch receives the WORKTREE, not a gitDir: where the review
	// ledger is anchored is a production decision that lives in
	// sharedReviewLedger, not something the caller picks per invocation.
	AnalyzeBranch func(worktree string, opts review.BranchOptions) (*review.BranchResult, error)
	Verify        func(worktree, gitDir string, cfg config.Config, modelVerifier *modelprobe.Verifier) review.TemplateVerification
	Publish       func(worktree, templatePath, base string) (string, bool, error)
	RecordEvent   func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error
	// GetGitCommonDir and RecordDecision cover T7.5 (M3 report): the
	// --force that overrides a red validation stops being an untraceable
	// exception. store.NewStore requires the git common dir (shared across
	// linked worktrees), NEVER the per-worktree gitDir that RecordEvent
	// above already uses: they are two directories with two distinct
	// contracts (see the doc comment of store.NewStore).
	GetGitCommonDir func(worktree string) (string, error)
	RecordDecision  func(commonDir string, d *store.Decision) error
	// ResolveActor is one more seam of this same effort: without it,
	// RunPrCreateWith would call resolveActor(worktree) directly, which
	// shells out to a real `git config user.name`, breaking DepsPrCreate's
	// promise of testing "without real git, agents or gh" (comment above).
	ResolveActor func(worktree string) string
	// WriteTemplate allows tests to observe whether the PR template was
	// created. When nil, RunPrCreateWith uses WritePRTemplate.
	WriteTemplate func(string) (string, error)
	// BlobStore builds the content-addressed store that lets AnalyzeBranch
	// reuse reviews after a rebase (F8 criterion 2). It is a seam because
	// ResolveBlobStore shells out to git, which DepsPrCreate exists to avoid;
	// nil means no reuse, the behaviour before this wiring.
	BlobStore func(worktree string) (review.StoreBlobs, error)
	// ReadDispositions reads the standing human answers for the advisory
	// overlay. It is a seam because loadDispositionsForWorktree resolves
	// the real git common dir, which DepsPrCreate exists to avoid; nil
	// means no standing answers, the fixture every older test builds.
	ReadDispositions func(worktree string) ([]review.FindingDisposition, error)
}

// RunPrCreateWith is the injectable version of pr create (test seam): it
// returns the exit code without ending the process, same pattern as the
// gate flow. cmd/sentinel parses the flags (error → "? %v" and exit 1)
// before dispatching here; wiring carries the package-main collaborators this
// flow shares with the review and gate commands (see Wiring).
func RunPrCreateWith(w io.Writer, worktree string, flags FlagsPrCreate, deps DepsPrCreate, wiring Wiring) int {
	cfg, err := deps.LoadConfig(worktree)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	gitDir, err := deps.GetGitDir()
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	// Validation FIRST (T1.8): it reuses internal/validation (the same piece
	// internal/gate uses, see the gate commands), not the full gate
	// orchestrator, because AnalyzeBranch audits the WHOLE branch, not a
	// single commit as AuditCommit does. If it fails without --force,
	// AnalyzeBranch is NEVER invoked: zero tokens spent.
	runs, err := deps.RunValidation(wiring.DefaultGateProfile, nil, validation.RunOptions{
		Worktree: worktree,
		Cfg:      cfg,
	})
	if err != nil {
		fmt.Fprintf(w, "? Could not run the validation: %v\n", err)
		return 1
	}
	findings := validation.Findings(runs, cfg.Validation.Capabilities)
	// forcedRedValidation distinguishes the PRESENCE of the --force flag from
	// its real EFFECT (an orchestrator finding, Fix 2): it is only true when
	// there actually were red findings that --force had to override. If
	// --force was passed but the validation was already green, the flag
	// exercised no effect and the event must not record an exception that
	// never happened.
	var forcedRedValidation bool
	if len(findings) > 0 {
		if !flags.Force {
			fmt.Fprintln(w, "🚨 Red validation: the PR is not published. Commands:")
			for _, h := range findings {
				fmt.Fprintf(w, "  - ✖ %s (%s):\n%s\n", h.Capability, h.Command, strings.TrimSpace(h.Evidence))
			}
			fmt.Fprintln(w, "Fix the red commands or repeat with --force --reason \"reason\" to publish anyway.")
			return 1
		}
		forcedRedValidation = true
		fmt.Fprintf(w, "⚠️  Red validation overridden with --force (reason: %s).\n", flags.Reason)
		// T7.5 (M3 report): --force stops being an untraceable exception.
		// It does not abort on failure (--force already decided to continue
		// despite the red validation, like the GetHeadSHA warning below):
		// it warns and continues.
		if commonDir, err := deps.GetGitCommonDir(worktree); err != nil {
			fmt.Fprintf(w, "⚠️  Warning: could not resolve the git-common-dir, the --force decision is left unrecorded (%v).\n", err)
		} else if err := deps.RecordDecision(commonDir, &store.Decision{
			Decision: store.DecisionForceBypass,
			Actor:    deps.ResolveActor(worktree),
			At:       time.Now().UTC(),
			Reason:   flags.Reason,
			Scope:    store.ScopePrCreate,
		}); err != nil {
			fmt.Fprintf(w, "⚠️  Warning: could not write the --force decision to decisions.jsonl (%v).\n", err)
		}
	}
	// With --force, the semantic review DOES run despite the red validation
	// (unlike sentinel gate, which short-circuits to save tokens): both
	// sources coexist in the same report, so the deterministic finding must
	// be able to supersede the equivalent semantic one (T6.2) instead of
	// duplicating the same signal twice. The validated SHA is resolved
	// explicitly (never inferred by branch position): without it,
	// AnalyzeBranch cannot attach the findings to any commit (fail-safe).
	var deterministicFindings []review.Finding
	var validatedSHA string
	if forcedRedValidation {
		deterministicFindings = wiring.ProjectFindings(findings)
		sha, err := deps.GetHeadSHA()
		if err != nil {
			// It does not abort the publication (--force already decided to
			// continue despite the red validation): but without the SHA there
			// is no commit to attach the deterministic findings to, so the
			// T6.2 supersede does not apply in this run. It warns explicitly
			// instead of discarding it silently.
			fmt.Fprintf(w, "⚠️  Warning: could not resolve the validated commit (%v); the deterministic findings will not supersede the equivalent semantic finding in this report.\n", err)
		} else {
			validatedSHA = sha
		}
	}

	modelVerifier := wiring.NewModelVerifier(worktree)
	factory := func(_ review.ReviewBundle, dimension string) (review.AgentReviewer, string, error) {
		profile := config.ResolveProfile(cfg, reviewcontract.DefaultProfile(dimension), "")
		adapter, err := agentadapter.NewAdapterWithProfile(cfg, profile)
		if err != nil {
			return nil, profile.Name, err
		}
		modelVerifier.Verify(profile.Name, profile.Model, adapter)
		return adapter, profile.Name, nil
	}

	base := flags.Base
	if base == "" {
		base = "main"
	}
	var blobStore review.StoreBlobs
	if deps.BlobStore != nil {
		var err error
		if blobStore, err = deps.BlobStore(worktree); err != nil {
			fmt.Fprintf(w, "⚠️  Warning: could not resolve the git-common-dir; revisions will not be reused by content after a rebase (%v).\n", err)
		}
	}
	// Standing human answers drive every rendered and advisory finding, and
	// carry into the net audit (FU-6 unit A). A corrupt log fails closed
	// before spending review tokens rather than auditing as if no human
	// answered. A nil seam means no standing answers, the fixture older
	// tests build; production always wires the real loader.
	var branchDispositions []review.FindingDisposition
	if deps.ReadDispositions != nil {
		var err error
		branchDispositions, err = deps.ReadDispositions(worktree)
		if err != nil {
			fmt.Fprintf(w, "? %v\n", err)
			return 1
		}
	}
	res, err := deps.AnalyzeBranch(worktree, wiring.BranchOptionsWithRefuter(cfg, modelVerifier, review.BranchOptions{
		Base: base,
		// the unaudited-commits decision in docs/issues/decisions.md: pr create no longer audits every
		// unaudited commit by default — the net audit below already plans
		// from the net diff's own aggregate risk, so paying for both was the
		// largest single cost in a review. --audit-pending restores the old
		// behavior explicitly.
		OnlyPending:              !flags.AuditPending,
		Overview:                 true,
		DeterministicFindings:    deterministicFindings,
		DeterministicFindingsSHA: validatedSHA,
		Factory:                  factory,
		Parallel:                 cfg.Review.Parallel,
		Store:                    blobStore,
		ReviewTransportFactory:   wiring.TransportFactory(cfg, worktree),
		// FU-11 residual: exposed-credential incidents ride every audited
		// commit through the per-commit deterministic channel.
		DeterministicFindingsFactory: SecretFindingsFactory(),
		ModelVerifier:                modelVerifier,
		NetReview:                    &review.NetReviewOptions{Intention: HonestNetIntention, Validation: fmt.Sprint(validationCommands(runs)), Dispositions: branchDispositions},
		OnCommit: func(idx, total int, sha string) {
			fmt.Fprintf(w, "⏳ [%d/%d] Auditing %s\n", idx+1, total, wiring.ShortSHA(sha))
		},
		OnDimension: func(dim string) {
			fmt.Fprintf(w, "  ⏳ %s …\n", dim)
		},
		OwnDiff: stackOwnDiff(flags.Parent, flags.ChainPR),
	}))
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	// Records can legitimately be empty now that pr create no longer audits
	// pending commits by default (the unaudited-commits decision in docs/issues/decisions.md): the net
	// audit below already plans from the net diff's own aggregate risk, so an
	// empty per-commit history is only a real dead end when there is no net
	// verdict either.
	if len(res.Records) == 0 && res.Net == nil {
		fmt.Fprintln(w, "_No audited commits on the branch._")
		return 1
	}

	// The net audit is the advisory authority when present.
	if res.Net != nil {
		fmt.Fprintln(w, review.VerdictLine(res, branchDispositions))
	} else if warn, blockers := SemanticNoticeWithDispositions(res.Records, branchDispositions); warn {
		fmt.Fprintln(w, "⚠️  NOTICE: semantic audit verdict = block (does not block publication, advisory).")
		for _, h := range blockers {
			fmt.Fprintf(w, "  - [%s] %s (%s:%d)\n", h.Severity, h.Description, h.File, h.Line)
		}
	}
	// Informational only, never a gate (the unaudited-commits decision in docs/issues/decisions.md):
	// the net verdict above is what decides, this only points at the gap.
	fmt.Fprint(w, review.RenderUnauditedNotice(res.Unaudited))

	// Oversized branch without --chain-pr: the chain is proposed, no giant PR
	// is published (guide §12.4).
	if res.Decision == "chain" && !flags.ChainPR {
		fmt.Fprintln(w, "🚨 Oversized branch: it exceeds the volume threshold without demonstrated coherence.")
		fmt.Fprintln(w, "It is proposed to split it into chained PRs (--chain-pr) instead of one giant PR.")
		return 1
	}

	verification := deps.Verify(worktree, gitDir, cfg, modelVerifier)
	verification.Validation = validationCommands(runs)

	publishBase := base
	if res.Own != nil {
		if res.Own.PublicationBranch == "" {
			fmt.Fprintln(w, "? Refusing to publish: the stacked parent has no verified publication branch.")
			return 1
		}
		publishBase = res.Own.PublicationBranch
	}

	body := review.RenderBranchPRTemplateWithDispositions(res, verification, wiring.Version, branchDispositions)
	writeTemplate := deps.WriteTemplate
	if writeTemplate == nil {
		writeTemplate = WritePRTemplate
	}
	templatePath, err := writeTemplate(body)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	prURL, fallback, err := deps.Publish(worktree, templatePath, publishBase)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if fallback {
		// The file is the deliverable artifact of the fallback: it is kept.
		fmt.Fprintln(w, "? Template on the clipboard: create the PR manually with that content.")
	} else {
		fmt.Fprintf(w, "? PR created: %s\n", prURL)
		// The body already lives in the PR: the ephemeral temp file is cleaned.
		if err := os.Remove(templatePath); err != nil {
			fmt.Fprintf(w, "? Warning: could not clean up the temporary file (%v).\n", err)
		}
	}

	detail, err := PrCreateEventDetail(prURL, fallback, flags.ChainPR, forcedRedValidation, flags.Reason, len(res.Unaudited))
	if err != nil {
		fmt.Fprintf(w, "? Warning: could not build the event detail: %v\n", err)
	}
	if err := deps.RecordEvent(gitDir, "pr-create", 0, res.SHAs, detail, worktree); err != nil {
		fmt.Fprintf(w, "? Warning: could not record the event: %v\n", err)
	}
	return 0
}

// SemanticNotice decides whether the branch's semantic verdict deserves a
// prominent advisory in the publication (T1.8): the verdict-blocking gate
// became advisory, like internal/gate since T1.7 — validation (below) is now
// the only gate that can prevent publishing. SemanticNotice NEVER decides
// whether to publish, only whether to warn. It returns the structured
// CRITICAL findings: formatting remains the CLI's responsibility.
//
// It used to be called gateBlock and returned "allowed"; it is renamed
// because a function that no longer blocks cannot keep being called
// "gate...Block" without lying about what it does.
func SemanticNotice(records []review.Record) (warn bool, blockers []review.ReviewFinding) {
	blockers = review.BranchBlockers(records)
	return len(blockers) > 0, blockers
}

// SemanticNoticeWithDispositions is SemanticNotice overlaid with the
// standing human answers (FU-6): a valid human refutation clears its
// finding from the branch blockers shown here exactly as in the engine and
// the gate.
func SemanticNoticeWithDispositions(records []review.Record, dispositions []review.FindingDisposition) (warn bool, blockers []review.ReviewFinding) {
	blockers = review.BranchBlockersWithDispositions(records, dispositions)
	return len(blockers) > 0, blockers
}

// validationCommands translates the ValidationRun values of
// internal/validation into VerifiedCommand for the template: same evidence
// shape (command + real exit code), which is why the type is reused instead
// of duplicated — what changes is the origin (pre-validation, not the
// post-hoc verification of ops.Verify), hence its own field/section.
func validationCommands(runs []validation.ValidationRun) []review.VerifiedCommand {
	cmds := make([]review.VerifiedCommand, 0, len(runs))
	for _, r := range runs {
		cmds = append(cmds, review.VerifiedCommand{Comando: r.Command, Exit: r.Exit})
	}
	return cmds
}
