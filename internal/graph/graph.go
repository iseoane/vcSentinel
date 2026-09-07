// Package graph defines the contracts of the verifiable graph and the
// optional context. Only the package's native provider can authorize partial
// scope.
package graph

// GraphProvider represents the native graph that produces an atomic analysis.
type GraphProvider interface {
	Name() string
	Analyze(paths []string) (AnalysisResult, error)
}

type completenessEvidence struct {
	complete  bool
	reason    string
	uncovered []string
}

type affectedScope struct {
	packages    []string
	tests       []string
	explanation []string
}

// AnalysisResult binds the analyzed input, the scope, and its evidence.
// Its zero value is not trusted; only graph can produce an authorizable value.
type AnalysisResult struct {
	identity string
	paths    []string
	scope    affectedScope
	evidence completenessEvidence
	trusted  bool
}

func newAnalysisResult(identity string, paths []string, scope affectedScope, evidence completenessEvidence) AnalysisResult {
	return AnalysisResult{
		identity: identity,
		paths:    clone(paths),
		scope: affectedScope{
			packages:    clone(scope.packages),
			tests:       clone(scope.tests),
			explanation: clone(scope.explanation),
		},
		evidence: completenessEvidence{
			complete:  evidence.complete,
			reason:    evidence.reason,
			uncovered: clone(evidence.uncovered),
		},
		trusted: true,
	}
}

func (r AnalysisResult) SnapshotIdentity() string { return r.identity }
func (r AnalysisResult) AnalyzedPaths() []string  { return clone(r.paths) }
func (r AnalysisResult) Complete() bool           { return r.evidence.complete }
func (r AnalysisResult) ReasonIncomplete() string { return r.evidence.reason }
func (r AnalysisResult) Uncovered() []string      { return clone(r.evidence.uncovered) }
func (r AnalysisResult) Scope() AffectedScope     { return copyScope(r.scope) }

// AffectedScope is a read-only view of the authorized scope.
type AffectedScope struct {
	packages    []string
	tests       []string
	explanation []string
}

func (a AffectedScope) Packages() []string    { return clone(a.packages) }
func (a AffectedScope) Tests() []string       { return clone(a.tests) }
func (a AffectedScope) Explanation() []string { return clone(a.explanation) }

// ScopeAuthorization is the only valid input for partial execution.
// Its zero value authorizes nothing; only AuthorizePartialScope can create a valid one.
type ScopeAuthorization struct {
	scope      affectedScope
	authorized bool
}

func (a ScopeAuthorization) Authorized() bool      { return a.authorized }
func (a ScopeAuthorization) Packages() []string    { return clone(a.scope.packages) }
func (a ScopeAuthorization) Tests() []string       { return clone(a.scope.tests) }
func (a ScopeAuthorization) Explanation() []string { return clone(a.scope.explanation) }

// AuthorizePartialScope fails closed against foreign, incomplete, or
// contradictory results, and against ones lacking a valid input and an
// explainable scope.
func AuthorizePartialScope(result AnalysisResult) (ScopeAuthorization, bool) {
	a := result.scope
	e := result.evidence
	if !result.trusted || result.identity == "" || !e.complete || e.reason != "" || len(e.uncovered) != 0 ||
		!allValid(result.paths) || len(a.packages) == 0 ||
		!optionalValid(a.packages) || !optionalValid(a.tests) || !allValid(a.explanation) {
		return ScopeAuthorization{}, false
	}
	return ScopeAuthorization{scope: affectedScope{
		packages: clone(a.packages), tests: clone(a.tests), explanation: clone(a.explanation),
	}, authorized: true}, true
}

func copyScope(a affectedScope) AffectedScope {
	return AffectedScope{
		packages:    clone(a.packages),
		tests:       clone(a.tests),
		explanation: clone(a.explanation),
	}
}

func allValid(values []string) bool {
	if len(values) == 0 {
		return false
	}
	return optionalValid(values)
}

func optionalValid(values []string) bool {
	for _, value := range values {
		if value == "" {
			return false
		}
	}
	return true
}

func clone(values []string) []string {
	return append([]string(nil), values...)
}
