package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// ErrNoReviewerFactory signals that no reviewer factory is configured for the
// overview. It is a configuration error, not a runtime one: callers can match
// it with errors.Is.
var ErrNoReviewerFactory = errors.New("no reviewer factory configured")

// DecisionChainLimit is the volume threshold (added+deleted lines) beyond
// which a branch proposes a chain of PRs unless coherence is demonstrated.
//
// It derives from git.ReviewableLinesLimit BY DECISION, not by accident: the
// decision to split a PR follows the same volume rule as the guardian,
// because it measures the same thing — how much change one person can review
// in one sitting. Before T0.4 it was a separate 400 that could silently
// diverge from the guardian's threshold (B4).
const DecisionChainLimit = git.ReviewableLinesLimit

// StoreBlobs is the minimum AnalyzeBranch needs from the T2.5/T2.6 store to
// reuse revisions by blob content instead of by SHA (T2.7): a rebase changes
// a commit's SHA without touching its files' content, so looking up by blob
// is what survives a rebase.
//
// It is deliberately defined here, in internal/review, and NOT in
// internal/store: internal/store already imports internal/review
// (review.Finding, review.Ledger in the v1 migration), so if this package
// imported internal/store a review→store→review cycle would appear.
// *store.Store implements this interface structurally, so neither package
// needs to know the other by name.
type StoreBlobs interface {
	// AlreadyReviewed reports whether blob was already seen in some
	// previously audited commit (under any SHA) and returns the associated
	// v2 findings, if any (see store.Store.AlreadyReviewed).
	AlreadyReviewed(blob string) (bool, []Finding, error)
	// RegisterCommitBlobs saves the blobs of a freshly audited commit so a
	// future rebase can recognize them (see store.Store.RegisterCommitBlobs).
	RegisterCommitBlobs(sha string, blobs map[string]string) error
	// BlobSHAs returns the commit SHAs that registered blob, or nil if it
	// was never registered (see store.Store.BlobSHAs). commitCoveredByBlobs
	// uses it to compute the exact intersection of SHAs across all of a
	// commit's blobs, not just whether "some" SHA covers each blob
	// separately.
	BlobSHAs(blob string) ([]string, error)
}

// BranchOptions defines the analysis of a whole branch against its base.
type BranchOptions struct {
	Base            string // comparison branch; empty = "main"
	OnlyPending     bool   // --only-unaudited: do not audit, just list records
	Overview        bool   // --overview: 1 branch-level Spec call for coherence
	ProfileOverride string
	Answers         string // clarifications for the extra questions round
	// OnCommit reports right before each pending commit starts being
	// audited (idx from 0, total = len(pending)): without it, OnDimension
	// progress cannot tell which commit each starting dimension belongs to,
	// because AnalyzeBranch audits several commits in the same pass and the
	// engine itself knows nothing about the "branch" context (documented
	// debt when F1 closed).
	OnCommit       func(idx, total int, sha string)
	OnDimension    func(dim string)
	Factory        ReviewerFactory
	RefuterFactory RefuterFactory
	Parallel       int
	// ReviewTransportFactory, when set, builds a per-commit durable transport
	// so every audited commit routes its dimension calls through the run
	// controller bound to its own SHA and paths. Nil keeps the legacy path.
	ReviewTransportFactory func(sha string, rutas []string) ReviewTransport
	// Store is optional (nil-safe): when not nil, AnalyzeBranch consults by
	// blob before by SHA to decide pending commits (T2.7, F2 exit criterion:
	// a rebase that does not alter content keeps 100% of the findings) and
	// registers the blobs of every commit it audits. Without Store the
	// behavior is the pre-T2.7 one: only the v1 ledger.
	Store StoreBlobs
	// DeterministicFindings (T6.2) carries the result of validating one
	// concrete snapshot, explicitly identified by DeterministicFindingsSHA
	// (never inferred by position in shas): AnalyzeBranch only forwards them
	// as AuditOptions.DeterministicFindings to the commit whose SHA matches
	// exactly, never to the others — a deterministic failure of one snapshot
	// does not prove it existed in another commit.
	DeterministicFindings []Finding
	// DeterministicFindingsSHA is the exact SHA of the commit validated to
	// produce DeterministicFindings. Empty (or with no match in the branch)
	// means they apply to no commit: an explicit fail-safe instead of
	// assuming it is always the last element of shas.
	DeterministicFindingsSHA string
	// DeterministicFindingsFactory, when set, produces per-commit
	// deterministic findings from that commit's files and diff. Unlike
	// DeterministicFindings, which carries one validated commit's gate
	// result, this runs for every audited commit in the range. Results are
	// appended to the findings already scheduled, never replacing them.
	DeterministicFindingsFactory func(sha string, files []string, diff string) []Finding
	// OwnDiff (T8.2, internal input) activates stacked own-diff semantics:
	// only merge_base(parent, HEAD)..HEAD is reviewed and findings from
	// already-audited context commits come back as read-only inherited
	// results. Nil keeps the legacy whole-range analysis against Base.
	OwnDiff *OwnDiffOptions
	// NetReview (T8.3, internal opt-in; CLI: T8.4) audits the net range via the engine seams.
	NetReview *NetReviewOptions
	// ModelVerifier answers whether a profile's model was verified by its
	// own agent. auditBranchCommit and the net audit forward it to the
	// engine, which stamps findings without importing a concrete prober.
	// Nil keeps the honest default: nothing verified.
	ModelVerifier ModelVerifier
}

// OverviewResult is the response of the branch-level Spec call: coherence of
// the whole set and the rationale for the PR template.
type OverviewResult struct {
	Coherent  bool   `json:"coherent"`
	Rationale string `json:"rationale"`
}

// UnauditedCommit pairs a commit that carries no review record, at the end
// of this AnalyzeBranch call, with its subject line — so a report can point
// a human at it (`sentinel review <sha>`) without a second git lookup.
type UnauditedCommit struct {
	SHA     string
	Subject string
}

// BranchResult aggregates the complete branch analysis: the SHAs of the
// range, the records, the real volume and the single/chain decision.
type BranchResult struct {
	Branch string
	SHAs   []string
	// Pending is the SHAs that had no review record when this call STARTED
	// (before the audit loop below runs): it answers "how many new commits
	// did this pass discover", which is what the pr-review/pr-create event
	// detail's "nuevas" counter reports. It is NOT recomputed after auditing,
	// so with OnlyPending == false a commit audited within this very call
	// still appears here — by design, for that telemetry meaning.
	Pending []string
	// Unaudited is the SHAs (with subject) that STILL carry no review
	// record once this call is done — recomputed AFTER the audit loop, so it
	// is what a report or a machine consumer should read to mean "this
	// commit was never audited" (the unaudited-commits decision in docs/issues/decisions.md). With
	// the default OnlyPending == true it equals Pending; with the
	// --audit-pending opt-in it is empty once auditing succeeds.
	Unaudited     []UnauditedCommit
	Records       []Record
	Volume        int
	Overview      *OverviewResult // nil when not requested or not obtained
	OverviewError string          // why there is no overview, when requested and failed
	Decision      string          // "single" | "chain"
	// Own carries the explainable stacked-range resolution (T8.2). Nil
	// keeps the legacy whole-range analysis against Base.
	Own *OwnRange
	// Inherited lists effective findings recorded by already-audited context
	// commits (read-only): their correction belongs to the parent PR, so they
	// never enter Records and can never block the current branch.
	Inherited []InheritedFinding
	// Net, non-nil after a NetReview opt-in: independent audit of the net range.
	Net *NetReview
}

// AnalyzeBranch analyzes the current branch against its base (guide §12.1):
// it resolves the merge-base, audits the pending SHAs with the engine (the
// ledger is a cache, not an authority), measures the real volume with
// numstat, runs the overview when requested and decides single PR vs chain
// by volume + coherence.
func AnalyzeBranch(ledger *Ledger, opts BranchOptions) (*BranchResult, error) {
	base := opts.Base
	if base == "" {
		base = "main"
	}
	// Stacked mode (T8.2): the reviewed range starts at the resolved parent
	// (never a silent "main"); without a reliable signal it fails explicitly.
	var own *OwnRange
	if opts.OwnDiff != nil {
		var err error
		own, err = resolveOwnRange(opts.OwnDiff, base)
		if err != nil {
			return nil, err
		}
		base = own.Parent
	}
	branch, err := git.CurrentBranch()
	if err != nil {
		return nil, err
	}
	mergeBase, err := git.MergeBase(base, "HEAD")
	if err != nil {
		return nil, err
	}
	shas, err := git.RangeSHAs(mergeBase, "HEAD")
	if err != nil {
		return nil, err
	}

	var pending []string
	reauditSpec := make(map[string]bool)
	for _, sha := range shas {
		if opts.Store != nil {
			message, err := git.CommitMessage(sha)
			if err != nil {
				return nil, err
			}
			decision, err := DecideBlobReuse(ledger, opts.Store, sha, message)
			if err != nil {
				return nil, err
			}
			if decision.Reused() {
				if err := ledger.AdoptRecordWithMessage(decision.OriginSHA, sha, message); err != nil {
					return nil, err
				}
				if decision.ReauditSpec {
					pending = append(pending, sha)
					reauditSpec[sha] = true
				}
				continue
			}
		}
		record, err := ledger.ReadRecord(sha)
		if err != nil {
			return nil, err
		}
		if record == nil {
			pending = append(pending, sha)
		}
	}

	if !opts.OnlyPending {
		for idx, sha := range pending {
			if opts.OnCommit != nil {
				opts.OnCommit(idx, len(pending), sha)
			}
			commitOptions := opts
			commitOptions.DeterministicFindings = deterministicFindingsForValidatedSHA(sha, opts.DeterministicFindingsSHA, opts.DeterministicFindings)
			if err := auditBranchCommit(ledger, sha, commitOptions, reauditSpec[sha]); err != nil {
				return nil, fmt.Errorf("could not audit %s: %v", sha, err)
			}
		}
	}

	records := make([]Record, 0, len(shas))
	var stillUnaudited []string
	for _, sha := range shas {
		record, err := ledger.ReadRecord(sha)
		if err != nil {
			return nil, err
		}
		if record != nil {
			records = append(records, *record)
		} else {
			// Recomputed HERE, not reused from the pre-audit `pending` slice
			// above: when OnlyPending is false the loop just above audited
			// these commits and gave them a record, so re-reading the ledger
			// is what tells "still has no record" from "just got one" —
			// exactly the ambiguity BranchResult.Unaudited exists to remove.
			stillUnaudited = append(stillUnaudited, sha)
		}
	}
	unaudited := unauditedCommitSubjects(stillUnaudited)

	volume, err := git.RangeNumstat(mergeBase, "HEAD")
	if err != nil {
		return nil, err
	}

	res := &BranchResult{
		Branch: branch, SHAs: shas, Pending: pending, Unaudited: unaudited,
		Records: records, Volume: volume,
	}
	if opts.NetReview != nil { // T8.3: mergeBase already is merge_base(base_or_resolved_parent, HEAD)
		head := mergeBase
		if len(shas) > 0 {
			head = shas[len(shas)-1]
		}
		if res.Net, err = runNetReview(opts.NetReview, opts, mergeBase, head, records); err != nil {
			return nil, err
		}
	}
	res.Own = own
	if own != nil {
		inherited, err := inheritedFindings(ledger, own)
		if err != nil {
			return nil, err
		}
		res.Inherited = inherited
	}
	if opts.Overview {
		overview, err := branchOverview(opts, branch, records)
		res.Overview = overview
		if err != nil {
			res.OverviewError = err.Error()
		}
	}

	// Decision on two axes: real volume + coherence (guide §12.1).
	switch {
	case volume <= DecisionChainLimit:
		res.Decision = "single"
	case res.Overview == nil || !res.Overview.Coherent:
		res.Decision = "chain"
	default:
		res.Decision = "single"
	}
	return res, nil
}

// deterministicFindingsForValidatedSHA returns the findings as-is only when
// sha matches validatedSHA exactly (the commit whose snapshot produced
// them); for any other commit in the branch, including validatedSHA == ""
// (no explicit SHA), it returns nil — the validated commit is never inferred
// by its position in the branch, so a deterministic failure of one snapshot
// is not attributed to a historical commit where it may never have existed
// (T6.2).
func deterministicFindingsForValidatedSHA(sha, validatedSHA string, findings []Finding) []Finding {
	if validatedSHA == "" || sha != validatedSHA {
		return nil
	}
	return findings
}

// deterministicFindingsForCommit gathers the deterministic findings for one
// audited commit: the gate findings bound to exactly this SHA plus, when
// set, the per-commit factory output for this commit's files and diff. A
// fresh slice every time: appending must never grow the shared options
// backing array.
func deterministicFindingsForCommit(opts BranchOptions, sha string, files []string, diff string) []Finding {
	out := append([]Finding(nil), deterministicFindingsForValidatedSHA(sha, opts.DeterministicFindingsSHA, opts.DeterministicFindings)...)
	if opts.DeterministicFindingsFactory != nil {
		out = append(out, opts.DeterministicFindingsFactory(sha, files, diff)...)
	}
	return out
}

// auditBranchCommit audits one pending commit with the engine and persists
// the revision in the ledger with bucket "pr" (origin: branch analysis).
func auditBranchCommit(ledger *Ledger, sha string, opts BranchOptions, specOnly bool) error {
	message, err := git.CommitMessage(sha)
	if err != nil {
		return err
	}
	diff, err := git.DiffCommit(sha)
	if err != nil {
		return err
	}
	files, err := git.FilesOfCommit(sha)
	if err != nil {
		return err
	}

	profile, err := change.ComputeCommitProfile(sha)
	if err != nil {
		return err
	}
	attributes, err := git.Attributes(sha)
	if err != nil {
		return err
	}
	var transport ReviewTransport
	if opts.ReviewTransportFactory != nil {
		transport = opts.ReviewTransportFactory(sha, files)
	}
	bundles := PlanForProfile(profile, files, diff, attributes).Bundles
	if specOnly {
		bundles = []ReviewBundle{{Name: "reused_spec", Dimensions: []string{DimSpec}, Priority: PriorityRequired, Cost: 1}}
	}
	result := AuditCommit(opts.Factory, opts.Parallel, AuditOptions{
		SHA:                   sha,
		Message:               message,
		Diff:                  diff,
		Bundles:               bundles,
		Answers:               opts.Answers,
		ProfileOverride:       opts.ProfileOverride,
		ContextPaths:          files,
		OnDimension:           opts.OnDimension,
		RefuterFactory:        opts.RefuterFactory,
		DeterministicFindings: deterministicFindingsForCommit(opts, sha, files, diff),
		ModelVerifier:         opts.ModelVerifier,
		ReviewTransport:       transport,
	})

	model := opts.ProfileOverride
	if model == "" {
		model = "default"
	}
	revision := Revision{
		At:                 time.Now(),
		Result:             result.Verdict,
		Fixed:              RevisionFixesPriorBlock(ledger, sha, result.Verdict, CoverageAuthoritative),
		Coverage:           CoverageAuthoritative, // pr branch audits derive their plan from the change
		Dims:               DimensionResultsForRecord(result.Dims),
		AggregatedFindings: result.Findings,
	}
	if specOnly {
		if err := ledger.SaveReusedSpecRevision(sha, revision); err != nil {
			return err
		}
	} else if err := ledger.SaveRevision(sha, message, "pr", model, revision); err != nil {
		return err
	}

	if opts.Store != nil {
		// Registers this commit's blobs so a future rebase can recognize
		// them via BlobSHAs/AlreadyReviewed (T2.7). No v2 findings are
		// invented from the v1 verdict (golden rule: never fabricate
		// evidence): CommitIndex keeps empty Fingerprints and only populated
		// Blobs, which is already enough for AlreadyReviewed to work
		// ("reviewed with no findings" is expected while the agent keeps
		// emitting v1, until F5).
		//
		// A failure here is NOT fatal for this audit: the record was already
		// saved above with SaveRevision, which is the only thing that
		// matters right now. Recording blobs is just a future-reuse
		// optimization (T2.7 fix); aborting AnalyzeBranch over it would
		// throw away a real, already-persisted result. It is reported on
		// stderr instead of propagating the error, so the signal is neither
		// lost silently nor made fatal.
		if err := RegisterCommitBlobs(opts.Store, sha, files); err != nil {
			fmt.Fprintf(os.Stderr, "vas-sentinel: could not register the blobs of %s in the store, continuing without registering (future-reuse optimization, does not affect this audit): %v\n", sha, err)
		}
	}
	return nil
}

// unauditedCommitSubjects resolves the subject line of every commit that
// still has no review record, so a report can name them instead of a bare
// SHA (the unaudited-commits decision in docs/issues/decisions.md). A nil slice in, nil slice out: a
// fully audited branch must not carry an empty-but-allocated slice into the
// rendered report or the JSON output.
//
// A subject-lookup failure is NOT fatal, same rationale as commitBlobs
// below: this is reporting sugar, not the audit itself, and the net verdict
// (the only thing that gates, per the unaudited-commits decision in docs/issues/decisions.md) must not
// be thrown away over a cosmetic git failure. It warns on stderr and falls
// back to the bare SHA as the subject.
func unauditedCommitSubjects(shas []string) []UnauditedCommit {
	if len(shas) == 0 {
		return nil
	}
	out := make([]UnauditedCommit, 0, len(shas))
	for _, sha := range shas {
		subject, err := git.CommitMessage(sha)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vas-sentinel: could not resolve the subject of %s, reporting it unaudited without one: %v\n", sha, err)
			subject = ""
		}
		out = append(out, UnauditedCommit{SHA: sha, Subject: subject})
	}
	return out
}

// RegisterCommitBlobs records a completed audit's immutable file blobs so a
// later SHA can be considered by DecideBlobReuse. Both review entry points use
// this helper; callers decide whether a registration failure is fatal.
func RegisterCommitBlobs(store StoreBlobs, sha string, files []string) error {
	blobs, err := commitBlobs(sha, files)
	if err != nil || len(blobs) == 0 {
		return err
	}
	return store.RegisterCommitBlobs(sha, blobs)
}

// commitBlobs resolves the blob of each file of a commit (file → blob), to
// register it in the store or consult it before auditing (T2.7). A commit
// with no files (degenerate case) returns a nil map, not an empty one: the
// caller then distinguishes "nothing to register" without needing a separate
// length check.
func commitBlobs(sha string, files []string) (map[string]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	blobs := make(map[string]string, len(files))
	for _, file := range files {
		blob, err := git.BlobFileAtCommit(sha, file)
		if err != nil {
			return nil, err
		}
		blobs[file] = blob
	}
	return blobs, nil
}

// BlobReuseDecision is the shared reuse decision for every review entry point.
// ReauditSpec is true only when identical content has a different commit message.
type BlobReuseDecision struct {
	OriginSHA   string
	ReauditSpec bool
}

func (d BlobReuseDecision) Reused() bool {
	return d.OriginSHA != ""
}

// DecideBlobReuse applies the exact blob-SHA intersection rule and then compares
// the stored review message with the destination message. A self match is not
// reuse: an explicit review of an already-indexed SHA must still audit it.
func DecideBlobReuse(ledger *Ledger, store StoreBlobs, sha, message string) (BlobReuseDecision, error) {
	covered, originSHA, err := commitCoveredByBlobs(store, sha)
	if err != nil || !covered || originSHA == sha {
		return BlobReuseDecision{}, err
	}
	source, err := ledger.ReadRecord(originSHA)
	if err != nil {
		return BlobReuseDecision{}, err
	}
	if source == nil {
		// Blob coverage can outlive a pruned ledger record. It is an optimization,
		// not an audit precondition: discard this stale candidate and audit normally.
		return BlobReuseDecision{}, nil
	}
	return BlobReuseDecision{OriginSHA: originSHA, ReauditSpec: source.Message != message}, nil
}

// commitCoveredByBlobs reports whether sha's content matches EXACTLY the
// content of ONE single previous commit: it computes the intersection of the
// SHAs that registered each of sha's blobs (BlobSHAs, not AlreadyReviewed —
// AlreadyReviewed only says "some SHA covers this blob", losing sight of
// whether it is the SAME SHA for all of them) and, if that intersection is
// not empty, covered=true and originSHA is any one of those candidates:
// all of them registered the complete set of sha's blobs, so adopting the
// record of any one is equally safe (an arbitrary choice among valid
// candidates, there is no "better" one). This is the typical rebase case:
// the commit is rewritten without touching its content.
//
// If any blob has NO registered SHA, or the intersection becomes empty at
// any point, sha is NOT considered covered: its files would come from a
// MIX of different previous commits (e.g. a squash), and there is no single
// previous record to adopt without inventing content. Losing the reuse is
// preferable to losing real findings. A commit with no files is never
// considered covered (nothing to reuse).
func commitCoveredByBlobs(s StoreBlobs, sha string) (covered bool, originSHA string, err error) {
	files, err := git.FilesOfCommit(sha)
	if err != nil {
		return false, "", err
	}
	blobs, err := commitBlobs(sha, files)
	if err != nil {
		return false, "", err
	}
	if len(blobs) == 0 {
		return false, "", nil
	}

	var candidates map[string]bool
	for _, blob := range blobs {
		shas, err := s.BlobSHAs(blob)
		if err != nil {
			return false, "", err
		}
		if len(shas) == 0 {
			return false, "", nil
		}
		seen := make(map[string]bool, len(shas))
		for _, blobSHA := range shas {
			seen[blobSHA] = true
		}
		if candidates == nil {
			candidates = seen
			continue
		}
		intersection := make(map[string]bool, len(candidates))
		for blobSHA := range candidates {
			if seen[blobSHA] {
				intersection[blobSHA] = true
			}
		}
		candidates = intersection
		if len(candidates) == 0 {
			return false, "", nil
		}
	}
	for blobSHA := range candidates {
		return true, blobSHA, nil
	}
	return false, "", nil
}

// branchOverview runs the branch-level Spec call (one single call, not one
// per commit). The error propagates so an overview failure never decides
// silently: the analysis continues (with the chain decision as the safe
// fallback) but the caller knows the exact cause.
func branchOverview(opts BranchOptions, branch string, records []Record) (*OverviewResult, error) {
	if opts.Factory == nil {
		return nil, ErrNoReviewerFactory
	}
	agent, _, err := opts.Factory(ReviewBundle{Name: "overview", Dimensions: []string{DimSpec}}, DimSpec)
	if err != nil {
		return nil, fmt.Errorf("could not create the branch reviewer: %w", err)
	}
	output, err := agent.RunPrompt(BuildOverviewPrompt(branch, records))
	if err != nil {
		return nil, fmt.Errorf("the branch reviewer did not respond: %w", err)
	}
	overview, err := ParseOverview(output)
	if err != nil {
		return nil, fmt.Errorf("invalid coherence response: %w", err)
	}
	return overview, nil
}

// BuildOverviewPrompt asks the agent for the coherence of the branch's
// commit set in one single call (branch-level Spec dimension).
func BuildOverviewPrompt(branch string, records []Record) string {
	var b strings.Builder
	b.WriteString("You are the coherence reviewer of a development branch.\n")
	b.WriteString("Branch: " + branch + "\n\nBranch commits:\n")
	for _, record := range records {
		b.WriteString(fmt.Sprintf("- %s %s\n", shortBranchSHA(record.SHA), record.Message))
	}
	b.WriteString("\nDo these commits form a single coherent change for the branch (one PR) or independent units with seams that deserve separate chained PRs?\n")
	b.WriteString("Return ONLY one JSON line with this exact shape:\n")
	b.WriteString(`{"coherent": true|false, "rationale": "<3 to 5 line explanation in English>"}` + "\n")
	return b.String()
}

// ParseOverview extracts the coherence response from the agent's text:
// it walks the balanced JSON objects and takes the first one containing the
// "coherent" field (tolerates surrounding text, multiline JSON and a JSON
// preamble).
func ParseOverview(output string) (*OverviewResult, error) {
	from := 0
	for {
		start := strings.Index(output[from:], "{")
		if start < 0 {
			return nil, errors.New("the agent did not return a coherence JSON")
		}
		start += from
		end, ok := closingJSON(output, start)
		if !ok {
			return nil, errors.New("the agent did not return a coherence JSON")
		}
		candidate := output[start : end+1]
		if strings.Contains(candidate, "coherent") {
			var overview OverviewResult
			if err := json.Unmarshal([]byte(candidate), &overview); err != nil {
				return nil, fmt.Errorf("invalid coherence JSON: %v", err)
			}
			return &overview, nil
		}
		from = end + 1
	}
}

// closingJSON returns the index of the '}' that closes the JSON object
// starting at start, without being confused by braces inside strings.
func closingJSON(output string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(output); i++ {
		c := output[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// shortBranchSHA truncates a SHA to 8 characters without panicking when it
// is shorter.
func shortBranchSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// RevisionFixesPriorBlock reports whether this audit (without block) fixes a
// previous revision of the same SHA that was in block. It follows RULE 1 in
// coverage.go on both sides.
//
// Reading: only the record's current AUTHORITATIVE verdict counts, so a
// supplementary alarm on an otherwise OK record is not "a previous revision in
// block" and a fresh OK audit is not flagged as correcting one.
//
// Writing: claiming to have cleared a block IS a verdict claim, so only an
// authoritative audit may make it. A narrowed run coming out ok on the one
// dimension it looked at has cleared nothing. The coverage of the audit being
// recorded is therefore a parameter and not the caller's business to apply:
// keeping it here is what stops each writer from re-deriving the rule, and
// getting it wrong, on its own.
func RevisionFixesPriorBlock(ledger *Ledger, sha, verdict string, coverage RevisionCoverage) bool {
	if verdict == VerdictBlock {
		return false
	}
	if coverage != CoverageAuthoritative {
		return false
	}
	record, err := ledger.ReadRecord(sha)
	if err != nil || record == nil || len(record.Revisions) == 0 {
		return false
	}
	current, _, ok := LastAuthoritativeRevision(*record)
	return ok && current.Result == VerdictBlock
}

// DimensionResultsForRecord copies the dimension outcomes into the shape
// persisted in the record (dropping the error and the profile, which already
// live in other fields).
func DimensionResultsForRecord(dims []DimensionOutcome) []DimensionResult {
	results := make([]DimensionResult, 0, len(dims))
	for _, dim := range dims {
		if dim.Result != nil {
			result := *dim.Result
			result.Bundle = dim.Bundle
			results = append(results, result)
		}
	}
	return results
}
