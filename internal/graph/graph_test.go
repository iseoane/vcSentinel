package graph_test

import (
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/graph"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

type contextProviderStub struct{}

func (contextProviderStub) Name() string { return "context-test" }
func (contextProviderStub) Context(string, []string) ([]review.Reference, error) {
	return []review.Reference{{Path: "internal/git/diff_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}}, nil
}

func TestZeroValueCannotAuthorizeScope(t *testing.T) {
	result := graph.AnalysisResult{}

	if _, authorized := graph.AuthorizePartialScope(result); authorized {
		t.Fatal("the zero value built outside graph authorized partial scope")
	}
	if result.Complete() || len(result.AnalyzedPaths()) != 0 || len(result.Scope().Packages()) != 0 {
		t.Fatal("the zero value exposed trusted evidence")
	}
}

func TestContextProviderOnlyProvidesContext(t *testing.T) {
	var provider review.ContextProvider = contextProviderStub{}

	refs, err := provider.Context("head", []string{"internal/git/diff.go"})
	if err != nil || len(refs) != 1 {
		t.Fatalf("context = %+v, error = %v", refs, err)
	}
	if _, authorized := graph.AuthorizePartialScope(graph.AnalysisResult{}); authorized {
		t.Fatal("the standalone context authorized partial scope")
	}
}
