package git

import "github.com/ISeoane-Quental/vas.sentinel/internal/intent"

const mechanicalSemanticFallback = "Mechanical fallback: production files stay with focused tests, direct compile dependencies stay in the same unit, structural cohesion is considered before file-class and line-count ordering, and no source or test content is compressed."

type SemanticSliceBoundary struct {
	ID        string           `json:"id"`
	Paths     []string         `json:"paths,omitempty"`
	Selectors []ChangeSelector `json:"selectors,omitempty"`
}

type SemanticSliceUnit struct {
	ID        string           `json:"id"`
	Paths     []string         `json:"paths,omitempty"`
	Selectors []ChangeSelector `json:"selectors,omitempty"`
}

type SemanticSliceProposal struct {
	State string              `json:"state"`
	Units []SemanticSliceUnit `json:"units"`
}

type SemanticOversizedUnit struct {
	ID         string
	Paths      []string
	AddedLines int
	Reason     string
}

type SemanticSliceOptions struct {
	Boundaries       []SemanticSliceBoundary
	Proposal         *SemanticSliceProposal
	Consent          bool
	Intent           string
	IntentSource     intent.Source
	ExcludedPaths    []string
	ConfirmOversized func(SemanticOversizedUnit) (bool, error)
}

type semanticFile struct {
	change PlannedChange
	path   string
	pkg    string
	class  string
	layer  string
	test   bool
	opaque bool
	defs   map[string]bool
	refs   []semanticRef
}

type semanticRef struct{ alias, name string }

type semanticGraph struct {
	files []semanticFile
	deps  map[int]map[int]bool
	sets  semanticSets
}

type semanticSets struct{ parent []int }

type semanticComponent struct {
	ids            []int
	ordered        []int
	selectors      []ChangeSelector
	boundary, rank int
	class, first   string
	lines          int
}
