package review

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// errOwnDiffWithoutParent signals that stacked own-diff semantics were requested
// without an explicit parent or without parent resolution enabled.
var errOwnDiffWithoutParent = errors.New("stacked own-diff analysis requested without an explicit parent or parent resolution")

// ParentResolver resolves the stacked parent branch through the T8.1 seam.
type ParentResolver func(git.ParentResolutionOptions) (git.ParentResolution, error)

// defaultParentResolver is the production resolver. Tests swap it so stacked
// analysis stays deterministic without gh, remotes, or network.
var defaultParentResolver = git.ResolveParentBranch

// OwnDiffOptions activates stacked own-diff semantics in AnalizarRama (T8.2).
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
	// No Worktree option on purpose (semantic review of T8.2): AnalizarRama
	// is ambient-cwd-based end to end. Directing only the parent resolution
	// to another repository would split the boundary between one repo's
	// parent and another repo's ranges.
}

// RangoPropio is the explainable range evidence of a stacked analysis:
//
//	own_diff(C) = merge_base(parent(C), C)..C   (reviewed)
//	context(C)  = merge_base(base, parent(C))..parent(C)   (read-only)
type RangoPropio struct {
	Parent        string   // resolved parent reference; never a silent "main"
	ParentSource  string   // T8.1 source: explicit | pull_request | tracking_upstream | local_merge_base
	Base          string   // context base reference ("main" unless overridden)
	PropioDesde   string   // merge_base(parent, HEAD): start of the reviewed range
	ContextoDesde string   // merge_base(base, parent): start of the read-only range
	Evidencia     []string // ordered resolution evidence from the T8.1 resolver
}

// HallazgoHeredado is one finding introduced outside the current branch's own
// diff. Its correction belongs to the PR that introduced it: it travels as
// inherited context and never blocks the current PR.
type HallazgoHeredado struct {
	SHA      string // context-range commit whose ficha carries the finding
	Hallazgo Hallazgo
}

// resolverRangoPropio turns OwnDiffOptions into the explainable own/context
// range pair. Without a usable parent signal it fails explicitly instead of
// guessing a base: misreporting foreign changes as own is the worst review
// outcome (T8.1 hard rule).
func resolverRangoPropio(opciones *OwnDiffOptions, base string) (*RangoPropio, error) {
	rango := &RangoPropio{Base: base}
	var resolucion git.ParentResolution
	switch {
	case strings.TrimSpace(opciones.Parent) != "":
		resuelto, err := defaultParentResolver(git.ParentResolutionOptions{ExplicitParent: opciones.Parent})
		if err != nil {
			return nil, err
		}
		resolucion = resuelto
	case opciones.ResolveParent:
		resuelto, err := defaultParentResolver(git.ParentResolutionOptions{})
		if err != nil {
			return nil, fmt.Errorf("stacked own-diff analysis needs a parent branch: %w", err)
		}
		resolucion = resuelto
	default:
		return nil, errOwnDiffWithoutParent
	}
	rango.Parent = resolucion.Reference
	rango.ParentSource = string(resolucion.Source)
	rango.Evidencia = resolucion.Evidence

	propioDesde, err := git.MergeBase(rango.Parent, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("own diff against parent %s: %w", rango.Parent, err)
	}
	contextoDesde, err := git.MergeBase(rango.Base, rango.Parent)
	if err != nil {
		return nil, fmt.Errorf("inherited context against base %s: %w", rango.Base, err)
	}
	rango.PropioDesde = propioDesde
	rango.ContextoDesde = contextoDesde
	return rango, nil
}

// hallazgosHeredados collects the effective findings that already-audited
// context commits recorded in the ledger. It is strictly read-only: missing
// fichas are skipped, nothing is audited, adopted, or persisted, so reviewing
// a stacked PR never audits its parent's work.
func hallazgosHeredados(ledger *Ledger, rango *RangoPropio) ([]HallazgoHeredado, error) {
	shas, err := git.SHAsRango(rango.ContextoDesde, rango.Parent)
	if err != nil {
		return nil, fmt.Errorf("inherited context %s..%s: %w", rango.ContextoDesde, rango.Parent, err)
	}
	var heredados []HallazgoHeredado
	for _, sha := range shas {
		ficha, err := ledger.LeerFicha(sha)
		if err != nil {
			return nil, err
		}
		if ficha == nil || !estaPendiente(*ficha) {
			continue
		}
		ultima, ok := ultimaRevision(*ficha)
		if !ok {
			continue
		}
		for _, hallazgo := range ultima.HallazgosEfectivos() {
			heredados = append(heredados, HallazgoHeredado{SHA: sha, Hallazgo: hallazgo})
		}
	}
	return heredados, nil
}
