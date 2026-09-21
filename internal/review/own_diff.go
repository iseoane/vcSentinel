package review

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/git"
)

// errOwnDiffWithoutParent signals that stacked own-diff semantics were requested
// without an explicit parent or without parent resolution enabled.
var errOwnDiffWithoutParent = errors.New("stacked own-diff analysis requested without an explicit parent or parent resolution")

// ParentResolver resolves the stacked parent branch through the T8.1 seam.
type ParentResolver func(git.ParentResolutionOptions) (git.ParentResolution, error)

// defaultParentResolver is the production resolver. Tests swap it so stacked
// analysis stays deterministic without gh, remotes, or network.
var defaultParentResolver = git.ResolveParentBranch

// OwnDiffOptions activates stacked own-diff semantics in AnalyzeBranch (T8.2).
// It is internal input only: no CLI flag exposes it until F8 integration.
type OwnDiffOptions struct {
	// Parent is an explicit parent reference supplied by an internal caller.
	// When set it short-circuits inference and is verified as a real commit
	// through git.ResolveParentBranch (ExplicitParent).
	Parent string
	// ResolveParent resolves the parent with the strict T8.1 precedence
	// (pull request base, tracking upstream, local merge base). It never
	// falls back to "main": an unreliable signal fails the analysis.
	ResolveParent bool
	// No Worktree option on purpose (semantic review of T8.2): AnalyzeBranch
	// is ambient-cwd-based end to end. Directing only the parent resolution
	// to another repository would split the boundary between one repo's
	// parent and another repo's ranges.
}

// OwnRange is the explainable range evidence of a stacked analysis:
//
//	own_diff(C) = merge_base(parent(C), C)..C   (reviewed)
//	context(C)  = merge_base(base, parent(C))..parent(C)   (read-only)
type OwnRange struct {
	Parent       string   // resolved parent reference; never a silent "main"
	ParentSource string   // T8.1 source: explicit | pull_request | tracking_upstream | local_merge_base
	Base         string   // context base reference ("main" unless overridden)
	OwnFrom      string   // merge_base(parent, HEAD): start of the reviewed range
	ContextFrom  string   // merge_base(base, parent): start of the read-only range
	Evidence     []string // ordered resolution evidence from the T8.1 resolver
	// PublicationBranch is the bare branch accepted by gh --base.
	PublicationBranch string
}

// InheritedFinding is one finding introduced outside the current branch's own
// diff. Its correction belongs to the PR that introduced it: it travels as
// inherited context and never blocks the current PR.
type InheritedFinding struct {
	SHA     string // context-range commit whose record carries the finding
	Finding Finding
}

// resolveOwnRange turns OwnDiffOptions into the explainable own/context
// range pair. Without a usable parent signal it fails explicitly instead of
// guessing a base: misreporting foreign changes as own is the worst review
// outcome (T8.1 hard rule).
func resolveOwnRange(opts *OwnDiffOptions, base string) (*OwnRange, error) {
	rng := &OwnRange{Base: base}
	var resolution git.ParentResolution
	switch {
	case strings.TrimSpace(opts.Parent) != "":
		resolved, err := defaultParentResolver(git.ParentResolutionOptions{ExplicitParent: opts.Parent})
		if err != nil {
			return nil, err
		}
		resolution = resolved
	case opts.ResolveParent:
		resolved, err := defaultParentResolver(git.ParentResolutionOptions{})
		if err != nil {
			return nil, fmt.Errorf("stacked own-diff analysis needs a parent branch: %w", err)
		}
		resolution = resolved
	default:
		return nil, errOwnDiffWithoutParent
	}
	rng.Parent = resolution.Reference
	rng.PublicationBranch = resolution.PublicationBranch
	rng.ParentSource = string(resolution.Source)
	rng.Evidence = resolution.Evidence

	ownFrom, err := git.MergeBase(rng.Parent, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("own diff against parent %s: %w", rng.Parent, err)
	}
	contextFrom, err := git.MergeBase(rng.Base, rng.Parent)
	if err != nil {
		return nil, fmt.Errorf("inherited context against base %s: %w", rng.Base, err)
	}
	rng.OwnFrom = ownFrom
	rng.ContextFrom = contextFrom
	return rng, nil
}

// inheritedFindings collects the effective findings that already-audited
// context commits recorded in the ledger. It is strictly read-only: missing
// records are skipped, nothing is audited, adopted, or persisted, so
// reviewing a stacked PR never audits its parent's work. It decides over the
// same effective view as the branch blockers and the summary table: the
// record's current findings overlaid with the standing human answers, so a
// refuted finding is neither inherited nor keeps its record inherited.
func inheritedFindings(ledger *Ledger, rng *OwnRange, dispositions []FindingDisposition) ([]InheritedFinding, error) {
	shas, err := git.RangeSHAs(rng.ContextFrom, rng.Parent)
	if err != nil {
		return nil, fmt.Errorf("inherited context %s..%s: %w", rng.ContextFrom, rng.Parent, err)
	}
	var inherited []InheritedFinding
	for _, sha := range shas {
		record, err := ledger.ReadRecord(sha)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		for _, finding := range effectiveRecordFindings(*record, dispositions) {
			switch NormalizeStatus(finding.Status) {
			case StatusRefuted, StatusFixed:
				continue
			}
			inherited = append(inherited, InheritedFinding{SHA: sha, Finding: finding})
		}
	}
	return inherited, nil
}
