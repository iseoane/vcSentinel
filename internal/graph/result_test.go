package graph

import (
	"reflect"
	"testing"
)

func TestNativeResultBindsInputEvidenceAndScope(t *testing.T) {
	paths := []string{"internal/git/diff.go"}
	result := newAnalysisResult("tree-1", paths, affectedScope{
		packages:    []string{"internal/git"},
		tests:       []string{"./internal/git"},
		explanation: []string{"diff.go belongs to internal/git"},
	}, completenessEvidence{complete: true})
	paths[0] = "altered.go"

	scope, authorized := AuthorizePartialScope(result)
	if !authorized {
		t.Fatal("a coherent native analysis was not authorized")
	}
	if !reflect.DeepEqual(result.AnalyzedPaths(), []string{"internal/git/diff.go"}) {
		t.Fatalf("analyzed paths = %v", result.AnalyzedPaths())
	}
	if !reflect.DeepEqual(result.Scope().Explanation(), []string{"diff.go belongs to internal/git"}) {
		t.Fatalf("inspectable explanation = %v", result.Scope().Explanation())
	}
	packages := scope.Packages()
	packages[0] = "altered"
	if !reflect.DeepEqual(scope.Packages(), []string{"internal/git"}) ||
		!reflect.DeepEqual(scope.Tests(), []string{"./internal/git"}) ||
		!reflect.DeepEqual(scope.Explanation(), []string{"diff.go belongs to internal/git"}) {
		t.Fatalf("authorized scope incomplete: %+v", scope)
	}
}

func TestNativeResultFailsClosed(t *testing.T) {
	scope := affectedScope{
		packages:    []string{"internal/git"},
		tests:       []string{"./internal/git"},
		explanation: []string{"affected path"},
	}
	cases := []struct {
		name      string
		paths     []string
		scope     affectedScope
		evidence  completenessEvidence
		uncovered string
	}{
		{name: "incomplete", paths: []string{"a.go"}, scope: scope, evidence: completenessEvidence{reason: "unsupported language"}},
		{name: "contradicts uncovered", paths: []string{"a.go"}, scope: scope, evidence: completenessEvidence{complete: true, uncovered: []string{"a.go"}}, uncovered: "a.go"},
		{name: "contradicts reason", paths: []string{"a.go"}, scope: scope, evidence: completenessEvidence{complete: true, reason: "partial"}},
		{name: "missing input", scope: scope, evidence: completenessEvidence{complete: true}},
		{name: "invalid input", paths: []string{""}, scope: scope, evidence: completenessEvidence{complete: true}},
		{name: "missing scope", paths: []string{"a.go"}, evidence: completenessEvidence{complete: true}},
		{name: "invalid scope", paths: []string{"a.go"}, scope: affectedScope{packages: []string{""}, explanation: []string{"affected path"}}, evidence: completenessEvidence{complete: true}},
		{name: "missing explanation", paths: []string{"a.go"}, scope: affectedScope{packages: []string{"p"}}, evidence: completenessEvidence{complete: true}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := newAnalysisResult("tree-1", tc.paths, tc.scope, tc.evidence)
			if _, authorized := AuthorizePartialScope(result); authorized {
				t.Fatal("an incomplete or contradictory state was authorized")
			}
			if tc.uncovered != "" && !reflect.DeepEqual(result.Uncovered(), []string{tc.uncovered}) {
				t.Fatalf("uncovered = %v", result.Uncovered())
			}
		})
	}
}
