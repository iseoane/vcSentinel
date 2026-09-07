package pr

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
)

const HonestNetIntention = "No PR title/description exists before publication: claims cover branch commits and the net diff only."

// FlagsPrReview carries the parsed `pr review` options across the dispatch
// boundary. cmd/sentinel owns the flag parsing (the package-main tests drive
// it); this struct is its counterpart here, so the fields are exported.
type FlagsPrReview struct {
	Base        string
	OnlyPending bool // --only-unaudited
	Overview    bool // --overview
	JsonOut     bool // --json
	Parent      string
}

// PrReviewEventDetail constructs the structured detail for the pr-review event
// (guide schema §13).
func PrReviewEventDetail(base string, res *review.BranchResult, ci bool) (ops.EventDetail, error) {
	detail := ops.EventDetail{
		"base":      base,
		"rama":      res.Branch,
		"auditadas": len(res.Records),
		"nuevas":    len(res.Pending),
		"volumen":   res.Volume,
		"ci":        ci,
		"overview":  res.Overview != nil,
		"chain_pr":  res.Decision == "chain",
	}
	// An overview failure must not be silent in the event: if it was requested
	// and failed, the chain decision carries its cause.
	if res.OverviewError != "" {
		detail["overview_error"] = res.OverviewError
	}
	if failures := reviewerFailureDetails(res.Records); len(failures) > 0 {
		detail["reviewer_failures"] = failures
	}
	return detail, nil
}

func reviewerFailureDetails(cards []review.Record) []ops.EventDetail {
	var failures []ops.EventDetail
	for _, card := range cards {
		if len(card.Revisions) == 0 {
			continue
		}
		latest := card.Revisions[len(card.Revisions)-1]
		for _, dimension := range latest.Dims {
			if dimension.Verdict != review.VerdictUnavailable || strings.TrimSpace(dimension.Reason) == "" {
				continue
			}
			failures = append(failures, ops.EventDetail{
				"sha":       card.SHA,
				"dimension": dimension.Dim,
				"reason":    review.CompactProviderCause(dimension.Reason),
			})
		}
	}
	return failures
}

// DecisionText explains the single/chain decision on the terminal output.
func DecisionText(decision string, volume int) string {
	switch decision {
	case "chain":
		return "PR decision: chain (--chain-pr, phase 6). The branch exceeds " +
			"the volume threshold and no coherence was demonstrated: it should be split into chained PRs."
	case "single":
		if volume > review.DecisionChainLimit {
			return "PR decision: a single PR. The branch exceeds the volume threshold but " +
				"the overview confirmed it is a coherent change."
		}
		return "PR decision: a single PR (volume within the threshold)."
	}
	return "PR decision: " + decision
}

// BranchPrReviewOptions assembles the branch-analysis options pr review hands to
// AnalyzeBranch. It is a separate function, not an inline literal, because
// the wiring it carries — notably the blob store that keeps a base rebase
// cheap (F8 criterion 2) — would otherwise be deletable without failing
// anything.
//
// The named results say what the signature alone would get wrong: storeWarning is
// ONLY the blob-store resolution failure, never a reason to abort. Reuse is
// optional, so options comes back fully usable with a nil Store and the caller
// warns through its own stream instead of returning.
func BranchPrReviewOptions(cfg config.Config, verifier *modelprobe.Verifier, worktree string, flags FlagsPrReview, factory review.ReviewerFactory, wiring Wiring) (options review.BranchOptions, storeWarning error) {
	base := flags.Base
	if base == "" {
		base = "main"
	}
	blobStore, storeWarning := ResolveBlobStore(worktree)
	// The ⏳ progress lines are human motion, not payload: when this
	// invocation will emit --json to stdout they ride stderr (the JSON-safe
	// channel, the same discipline as the durable-run announcer), so byte 0
	// of --json stdout stays '{'.
	progress := io.Writer(os.Stdout)
	if flags.JsonOut {
		progress = os.Stderr
	}
	return wiring.BranchOptionsWithRefuter(cfg, verifier, review.BranchOptions{
		Base:                   base,
		OnlyPending:            flags.OnlyPending,
		Overview:               flags.Overview,
		Factory:                factory,
		Parallel:               cfg.Review.Parallel,
		Store:                  blobStore,
		ReviewTransportFactory: wiring.TransportFactory(cfg, worktree),
		// FU-11 residual: exposed-credential incidents ride every audited
		// commit through the per-commit deterministic channel.
		DeterministicFindingsFactory: SecretFindingsFactory(),
		ModelVerifier:                verifier,
		OnCommit: func(idx, total int, sha string) {
			fmt.Fprintf(progress, "⏳ [%d/%d] Auditing %s\n", idx+1, total, wiring.ShortSHA(sha))
		},
		OnDimension: func(dim string) {
			fmt.Fprintf(progress, "  ⏳ %s …\n", dim)
		},
		OwnDiff:   stackOwnDiff(flags.Parent, false),
		NetReview: &review.NetReviewOptions{Intention: HonestNetIntention, Validation: "pr review performs no deterministic validation"},
	}), storeWarning
}

// ApplyPrReviewDispositions loads the standing human answers and sets
// them on the net review input for the cross-SHA carry-over. A corrupt log
// is an error: pr review must fail closed before spending review tokens
// rather than audit as if no human answered. The loader is a seam so tests
// drive this without git. A nil net review (no net audit requested) leaves
// the options untouched and still reports a loader failure.
func ApplyPrReviewDispositions(options review.BranchOptions, worktree string, load func(string) ([]review.FindingDisposition, error)) (review.BranchOptions, error) {
	dispositions, err := load(worktree)
	if err != nil {
		return options, err
	}
	if options.NetReview != nil {
		options.NetReview.Dispositions = dispositions
	}
	return options, nil
}

// DepsPrReview carries the seams RunPrReviewWith needs to drive the whole
// flow without real git or agents: the branch analysis and the event
// recorder. Production resolves both in realPrReviewDeps; AnalyzeBranch
// receives the ledger RunPrReviewWith resolved BEFORE the options so the
// ledger-before-options failure order (see the comment below) is preserved.
type DepsPrReview struct {
	AnalyzeBranch func(ledger *review.Ledger, options review.BranchOptions) (*review.BranchResult, error)
	RecordEvent   func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error
}

func realPrReviewDeps() DepsPrReview {
	return DepsPrReview{
		AnalyzeBranch: review.AnalyzeBranch,
		RecordEvent:   ops.RecordEvent,
	}
}

// RunPrReview analyzes the branch against the base and shows the audit
// matrix, the summary and the single/chain decision. It is dry-run: nothing
// is published. It records the pr-review event when done. cmd/sentinel parses
// the flags and exits on a parse error before dispatching here.
func RunPrReview(worktree string, flags FlagsPrReview, wiring Wiring) {
	os.Exit(RunPrReviewWith(os.Stdout, worktree, flags, wiring, realPrReviewDeps()))
}

// RunPrReviewWith is the injectable core of RunPrReview (same pattern as
// RunPrCreateWith): it returns the exit code without ending the process, so
// the whole --json flow can be driven from a test with stubbed seams.
func RunPrReviewWith(w io.Writer, worktree string, flags FlagsPrReview, wiring Wiring, deps DepsPrReview) int {
	// STRICT config (orchestrator finding, outside the record's original
	// text): an unknown key in the yml must cut here with an explicit error,
	// not silently continue with the default config.
	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	gitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
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

	// Anchored on the common directory, not on gitDir: see sharedReviewLedger.
	// AnalyzeBranch also WRITES here, through GuardarRevision and AdoptarFicha,
	// so this is where a rebase-adopted copy lands too.
	//
	// Resolved BEFORE the branch options, which warn-and-continue when the same
	// common directory cannot be resolved: reporting that content reuse is
	// degraded and then dying on the identical lookup told the operator the run
	// would continue when it could not.
	ledger, err := wiring.SharedReviewLedger(worktree)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	options, err := BranchPrReviewOptions(cfg, modelVerifier, worktree, flags, factory, wiring)
	if err != nil {
		fmt.Fprintf(w, "⚠️  Warning: could not resolve the git-common-dir; revisions will not be reused by content after a rebase (%v).\n", err)
	}
	// Standing human answers carry into the net audit (FU-6 unit A). A
	// corrupt log fails closed rather than auditing as if no human answered.
	var loadErr error
	options, loadErr = ApplyPrReviewDispositions(options, worktree, wiring.LoadDispositions)
	if loadErr != nil {
		fmt.Fprintf(w, "? %v\n", loadErr)
		return 1
	}
	base := options.Base
	res, err := deps.AnalyzeBranch(ledger, options)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	detail, err := PrReviewEventDetail(base, res, git.DetectCI(worktree))
	if err != nil {
		fmt.Fprintf(w, "? Warning: could not build the event detail: %v\n", err)
	}
	if err := deps.RecordEvent(gitDir, "pr-review", 0, res.SHAs, detail, worktree); err != nil {
		fmt.Fprintf(w, "? Warning: could not record the event: %v\n", err)
	}

	if flags.JsonOut {
		output := PrReviewJSONOutput(base, res)
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			fmt.Fprintf(w, "? %v\n", err)
			return 1
		}
		fmt.Fprintln(w, string(data))
		return 0
	}

	if res.Net != nil { // T8.4/A: the authoritative verdict leads the report
		fmt.Fprintln(w, review.VerdictLine(res))
	}
	if len(res.Records) > 0 {
		fmt.Fprintln(w, "OWN (per-commit audit)")
		fmt.Fprintln(w, review.RenderMatrix(res.Records))
		if res.Net == nil { // historical summary only without a net authority
			fmt.Fprintln(w, review.RenderSummary(res.Records))
		}
		fmt.Fprintln(w, DecisionText(res.Decision, res.Volume))
	}
	fmt.Fprint(w, review.InheritedSection(res.Inherited))
	// Ticket 07: admission failures are first-class evidence, so the terminal
	// report never lets them pass as generic infrastructure unavailability.
	if admission, _ := unavailableCount(res.Records); admission > 0 {
		fmt.Fprintf(w, "? %d unavailable dimension record(s) are ADMISSION failures (evidence rejected before verdicts), not infrastructure outages.\n", admission)
	}
	if res.OverviewError != "" {
		fmt.Fprintf(w, "? Warning: the overview could not be obtained (%s); the decision was made by volume.\n", res.OverviewError)
	}
	return 0
}

// PrReviewJSONOutput builds the public --json result shape of pr review,
// including the ticket-07 classification of unavailable dimension records:
// review_admission_failures and review_infrastructure_failures are counted
// separately so consumers can distinguish rejected evidence from outages.
func PrReviewJSONOutput(base string, res *review.BranchResult) map[string]any {
	admission, infrastructure := unavailableCount(res.Records)
	output := map[string]any{
		"rama":       res.Branch,
		"base":       base,
		"shas":       res.SHAs,
		"pendientes": res.Pending,
		"fichas":     res.Records,
		"volumen":    res.Volume,
		"decision":   res.Decision,
		"overview":   res.Overview,
		// Ticket 07: admission vs infrastructure split over the append-only
		// revision history of every audited commit on the branch.
		"review_admission_failures":      admission,
		"review_infrastructure_failures": infrastructure,
	}
	if res.OverviewError != "" {
		output["overview_error"] = res.OverviewError
	}
	if res.Own != nil { // T8.4/E
		output["own"] = res.Own
	}
	if len(res.Inherited) > 0 {
		output["inherited"] = res.Inherited
	}
	if res.Net != nil {
		output["net"] = res.Net
	}
	return output
}

// unavailableCount classifies every unavailable DimensionResult recorded
// in the branch's revisions: reasons carrying the literal admission prefix
// are admission failures; everything else stays infrastructure. It reads the
// persisted ledger shape, where the typed transport error no longer exists.
func unavailableCount(records []review.Record) (admission, infrastructure int) {
	for _, record := range records {
		for _, revision := range record.Revisions {
			for _, dim := range revision.Dims {
				if dim.Verdict != review.VerdictUnavailable {
					continue
				}
				if reviewexec.IsAdmissionReason(dim.Reason) {
					admission++
				} else {
					infrastructure++
				}
			}
		}
	}
	return admission, infrastructure
}
