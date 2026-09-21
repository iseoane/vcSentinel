package pr

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vcSentinel/internal/ops"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vcSentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// FlagsPrReview carries the parsed `pr review` options across the dispatch
// boundary. cmd/sentinel owns the flag parsing (the package-main tests drive
// it); this struct is its counterpart here, so the fields are exported.
type FlagsPrReview struct {
	Base     string
	Overview bool // --overview
	JsonOut  bool // --json
	Parent   string
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
		// the unaudited-commits decision in docs/issues/decisions.md: distinct from "nuevas" (commits
		// discovered this pass) — this is how many still carry no review
		// record once the pass is done, so an operator reading events can
		// tell an audited pass apart from a skipped one.
		"unaudited": len(res.Unaudited),
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
		latest, _, ok := review.LastAuthoritativeRevision(card)
		if !ok {
			continue
		}
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
//
// progress is the injected human-motion channel for the ⏳ spinner callbacks:
// the caller owns the JSON-safe routing (the cmd caller passes its payload
// writer normally and stderr in --json mode), so this assembler never reads
// the process streams itself.
func BranchPrReviewOptions(cfg config.Config, verifier *modelprobe.Verifier, worktree string, flags FlagsPrReview, factory review.ReviewerFactory, wiring Wiring, progress io.Writer) (options review.BranchOptions, storeWarning error) {
	base := flags.Base
	if base == "" {
		base = "main"
	}
	blobStore, storeWarning := ResolveBlobStore(worktree)
	return wiring.BranchOptionsWithRefuter(cfg, verifier, review.BranchOptions{
		Base: base,
		// pr review reports record gaps but never audits commits on the
		// operator's behalf; sentinel review owns per-commit verdicts.
		OnlyPending:            true,
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
		NetReview: &review.NetReviewOptions{Validation: "pr review performs no deterministic validation"},
	}), storeWarning
}

// ApplyPrReviewDispositions loads the standing human answers and sets them
// on the branch options, where the branch reporting surfaces and the net
// review's cross-SHA carry-over both read them. A corrupt log is an error:
// pr review must fail closed before spending review tokens rather than audit
// as if no human answered. The loader is a seam so tests drive this without
// git. It never invents a net review input and still reports a loader
// failure when none was requested.
func ApplyPrReviewDispositions(options review.BranchOptions, worktree string, load func(string) ([]review.FindingDisposition, error)) (review.BranchOptions, error) {
	dispositions, err := load(worktree)
	if err != nil {
		return options, err
	}
	options.Dispositions = dispositions
	return options, nil
}

// DepsPrReview carries the seams RunPrReviewWith needs to drive the whole
// flow without real git or agents: the branch analysis, the event recorder
// and the event detail builder. Production resolves all three in
// realPrReviewDeps; AnalyzeBranch receives the ledger RunPrReviewWith
// resolved BEFORE the options so the ledger-before-options failure order
// (see the comment below) is preserved. EventDetail exists because
// PrReviewEventDetail never fails on a real branch result: the seam is the
// only deterministic way to drive the "could not build the event detail"
// warning from a test.
type DepsPrReview struct {
	AnalyzeBranch func(ledger *review.Ledger, options review.BranchOptions) (*review.BranchResult, error)
	RecordEvent   func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error
	EventDetail   func(base string, res *review.BranchResult, ci bool) (ops.EventDetail, error)
	WriteEvidence func(worktree, branch string, logs []review.EvidenceLog) ([]string, error)
	SavePRReview  func(worktree string, entry *store.PRReviewEntry) error
	ReadIntents   func(shas []string) ([]review.IntentLine, error)
	CommitMessage func(sha string) (string, error)
}

func realPrReviewDeps() DepsPrReview {
	return DepsPrReview{
		AnalyzeBranch: review.AnalyzeBranch,
		RecordEvent:   ops.RecordEvent,
		EventDetail:   PrReviewEventDetail,
		CommitMessage: git.CommitMessage,
		ReadIntents: func(shas []string) ([]review.IntentLine, error) {
			lines := make([]review.IntentLine, 0, len(shas))
			for _, sha := range shas {
				value, err := git.CommitIntent(sha)
				if err != nil {
					return nil, err
				}
				if value.Text != "" {
					lines = append(lines, review.IntentLine{SHA: sha, Text: value.Text, Source: value.Source})
				}
			}
			return lines, nil
		},
	}
}

// RunPrReview analyzes the branch against the base, then authors and persists
// its local judgement and evidence. It does not publish a pull request. It
// records the pr-review event when done. cmd/sentinel parses the flags and exits
// on a parse error before dispatching here, and it owns the JSON-safe routing:
// it passes the payload writer for both channels normally, and stderr for the
// human motion in --json mode.
func RunPrReview(w, progress io.Writer, worktree string, flags FlagsPrReview, wiring Wiring) {
	os.Exit(RunPrReviewWith(w, progress, worktree, flags, wiring, realPrReviewDeps()))
}

// RunPrReviewWith is the injectable core of RunPrReview (same pattern as
// RunPrCreateWith): it returns the exit code without ending the process, so
// the whole --json flow can be driven from a test with stubbed seams.
//
// w carries the payload (the terminal report or the --json document);
// progress carries the human motion (⏳ spinners, blob-store, event-detail
// and event-record warnings). The caller owns the JSON-safe routing — cmd
// passes w itself normally and stderr in --json mode — so this function
// never reads the process streams.
func RunPrReviewWith(w, progress io.Writer, worktree string, flags FlagsPrReview, wiring Wiring, deps DepsPrReview) int {
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
	// The ⏳ progress lines and the human warnings (blob-store reuse, event
	// detail and event recording) are human motion, not payload: they ride
	// the progress channel the caller injected, so byte 0 of --json stdout
	// stays '{' without this function consulting flags.JsonOut or the
	// process streams.
	options, err := BranchPrReviewOptions(cfg, modelVerifier, worktree, flags, factory, wiring, progress)
	if err != nil {
		fmt.Fprintf(progress, "⚠️  Warning: could not resolve the git-common-dir; revisions will not be reused by content after a rebase (%v).\n", err)
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
	var intents []review.IntentLine
	if options.NetReview != nil {
		netReview := options.NetReview
		options.PrepareNetReview = func(shas []string) error {
			if deps.ReadIntents != nil {
				var err error
				intents, err = deps.ReadIntents(shas)
				if err != nil {
					return err
				}
			}
			netReview.Intention = review.IntentText(intents)
			if netReview.Intention == "" {
				netReview.Intention = review.NoRecordedIntentForPRRange
			}
			return nil
		}
	}
	res, err := deps.AnalyzeBranch(ledger, options)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if deps.WriteEvidence == nil {
		deps.WriteEvidence = review.WriteEvidence
	}
	if deps.SavePRReview == nil {
		deps.SavePRReview = func(root string, entry *store.PRReviewEntry) error {
			common, err := git.GetGitCommonDir(root)
			if err != nil {
				return err
			}
			return store.NewStore(common).SavePRReview(entry)
		}
	}
	if len(res.SHAs) == 0 {
		fmt.Fprintln(w, "? review produced no branch head")
		return 1
	}
	head := res.SHAs[len(res.SHAs)-1]
	if !store.IsValidGitObjectID(head) {
		fmt.Fprintln(w, "? review produced invalid branch head")
		return 1
	}
	title := ""
	if deps.CommitMessage == nil {
		fmt.Fprintln(w, "? pr review title reader is unavailable")
		return 1
	}
	title, err = deps.CommitMessage(res.SHAs[0])
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if strings.TrimSpace(title) == "" {
		fmt.Fprintln(w, "? review produced no PR title")
		return 1
	}
	verdict := review.VerdictDeBranch(res.Records, options.Dispositions)
	if res.Net != nil {
		verdict = res.Net.Audit.Verdict
	}
	// pr review runs neither validation nor verification by contract; saying
	// so is not the same claim as "nothing is configured", which is what an
	// unmarked empty verification rendered.
	unattempted := review.TemplateVerification{NotAttempted: true}
	attestation := review.BuildPRReviewAttestation(res, intents, unattempted, options.Dispositions)
	body, err := review.RenderPRReviewBody(res, intents, unattempted, attestation, options.Dispositions)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	evidence, err := deps.WriteEvidence(worktree, res.Branch, []review.EvidenceLog{{Step: "pr-review", Content: body}})
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	attestationJSON, err := json.Marshal(attestation)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if err := deps.SavePRReview(worktree, &store.PRReviewEntry{Branch: res.Branch, HeadSHA: head, Title: title, Verdict: verdict, Body: body, Attestation: attestationJSON, Evidence: evidence, UnavailableCauses: NetUnavailableCauses(res), At: time.Now().UTC()}); err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	detail, err := deps.EventDetail(base, res, git.DetectCI(worktree))
	if err != nil {
		fmt.Fprintf(progress, "? Warning: could not build the event detail: %v\n", err)
	}
	if err := deps.RecordEvent(gitDir, "pr-review", 0, res.SHAs, detail, worktree); err != nil {
		fmt.Fprintf(progress, "? Warning: could not record the event: %v\n", err)
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
		fmt.Fprintln(w, review.VerdictLine(res, options.Dispositions))
		// Item 17: an unavailable net verdict alone says nothing about why.
		// The per-dimension reasons live in memory but never reached this
		// console path, so name each unavailable dimension and its cause
		// here. This is console-only diagnostics: nothing here feeds the
		// persisted published body.
		for _, line := range RenderNetUnavailableLines(res) {
			fmt.Fprintln(w, line)
		}
	}
	if len(res.Records) > 0 {
		fmt.Fprintln(w, "OWN (per-commit audit)")
		fmt.Fprintln(w, review.RenderMatrix(res.Records))
		if res.Net == nil { // historical summary only without a net authority
			fmt.Fprintln(w, review.RenderSummary(res.Records, options.Dispositions))
		}
	}
	// Informational only, never a gate (the unaudited-commits decision in docs/issues/decisions.md):
	// this must render even with zero Records, the default now that pr
	// review does not audit pending commits.
	fmt.Fprint(w, review.RenderUnauditedNotice(res.Unaudited))
	// The single/chain decision is volume-driven, independent of whether any
	// per-commit record exists: it must not disappear when Records is empty.
	fmt.Fprintln(w, DecisionText(res.Decision, res.Volume))
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
		output["net_unavailable_causes"] = NetUnavailableCauses(res)
	}
	// the unaudited-commits decision in docs/issues/decisions.md: "pendientes" alone leaves a machine
	// consumer unable to tell "no review record" apart from "audited, no
	// findings" without cross-referencing "fichas" by SHA. "unaudited"
	// carries the same commits explicitly, paired with their subject, and is
	// only present when the branch actually has a gap to report.
	if len(res.Unaudited) > 0 {
		output["unaudited"] = res.Unaudited
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

// NetUnavailableCauses extracts one observational cause per unavailable net
// dimension. It mirrors the classification rule in AuditResult.String
// (admission when the reason carries the admission prefix, infrastructure
// otherwise) through the same IsAdmissionReason source of truth, so no second
// rule exists. A DimensionOutcome with a nil Result but a non-nil Error is
// treated as unavailable, exactly as AuditResult.String treats it. The result
// never changes the verdict: it only records why a dimension never audited.
func NetUnavailableCauses(res *review.BranchResult) []store.UnavailableDimensionCause {
	causes := []store.UnavailableDimensionCause{}
	if res == nil || res.Net == nil {
		return causes
	}
	for _, rd := range res.Net.Audit.Dims {
		reason := ""
		invocation := ""
		name := rd.Dim
		if rd.Result != nil {
			reason = rd.Result.Reason
			invocation = rd.Result.InvocationID
			if name == "" {
				name = rd.Result.Dim
			}
		}
		if reason == "" && rd.Error != nil {
			reason = rd.Error.Error()
		}
		unavailable := rd.Result != nil && rd.Result.Verdict == review.VerdictUnavailable
		if rd.Result == nil && rd.Error != nil {
			unavailable = true
		}
		if !unavailable {
			continue
		}
		if name == "" {
			name = "?"
		}
		cause := store.UnavailableDimensionCause{Dimension: name}
		if strings.TrimSpace(reason) == "" {
			cause.Class = "unrecorded"
			cause.InvocationID = invocation
			causes = append(causes, cause)
			continue
		}
		cause.Class = "infrastructure"
		if reviewexec.IsAdmissionReason(reason) {
			cause.Class = "admission"
		}
		cause.Reason = reason
		cause.InvocationID = invocation
		causes = append(causes, cause)
	}
	return causes
}

// RenderNetUnavailableLines renders the console diagnostics for every
// unavailable net dimension: the dimension name with its cause and class when
// a reason was recorded, or the unrecorded-cause fallback naming the
// invocation id and the manual command that reads it. Only these lines reach
// the terminal; the published body is untouched.
func RenderNetUnavailableLines(res *review.BranchResult) []string {
	var lines []string
	for _, cause := range NetUnavailableCauses(res) {
		if cause.Class != "unrecorded" {
			lines = append(lines, fmt.Sprintf("? net dimension %q unavailable (%s): %s", cause.Dimension, cause.Class, cause.Reason))
			continue
		}
		if cause.InvocationID != "" {
			lines = append(lines, fmt.Sprintf("? net dimension %q unavailable: cause unrecorded (invocation %s); inspect the owning run with `sentinel runs logs --run <run-id>` (locate it with `sentinel runs status`).", cause.Dimension, cause.InvocationID))
			continue
		}
		lines = append(lines, fmt.Sprintf("? net dimension %q unavailable: cause unrecorded (no invocation recorded); inspect with `sentinel runs status`.", cause.Dimension))
	}
	return lines
}
