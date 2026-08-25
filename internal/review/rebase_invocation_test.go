package review_test

// Ticket 07 slice 2b acceptance criterion: reviewed content re-encountered
// under a DIFFERENT commit SHA is admitted through the existing blob identity
// rules without any reviewer/provider call, and every surfaced finding still
// reports its ORIGINAL producing invocation identity from the first review.
//
// Same black-box shape as rebase_test.go (package review_test over a real git
// repository and a real store), so it reuses that file's git helpers instead
// of duplicating them a third time.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

const (
	invocationFindingDescription = "v2 finding with durable provenance"
	originalInvocation           = "inv-first-review-0001"
	secondPassInvocation         = "inv-second-review-should-not-happen"
)

// rebaseV2Response emits one v2 finding (the "id" marker makes esV2 true) so
// the persisted ficha carries Hallazgos whose InvocationID binding can be
// checked after adoption.
var rebaseV2Response = "BEGIN_REVIEW\n" +
	`{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"b.txt","line":1,"severity":"WARNING","description":"` + invocationFindingDescription + `","id":"rebase-inv-1","title":"durable provenance","evidence":"content-b","confidence":"high","location":{"file":"b.txt","line_start":1,"line_end":1},"status":"open"}]}` +
	"\nEND_REVIEW"

// rebaseV2StubAgent implements review.AuditorAgente but must NEVER be
// called: both passes route through a transport, so any direct legacy call is
// exactly the provider invocation the reuse rule forbids.
type rebaseV2StubAgent struct{ calls int }

func (a *rebaseV2StubAgent) EjecutarPrompt(string) (string, error) {
	a.calls++
	return "", errors.New("direct reviewer call forbidden while a transport is configured")
}

func (a *rebaseV2StubAgent) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func (a *rebaseV2StubAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

func rebaseV2StubFactory(a *rebaseV2StubAgent) review.FabricaAuditor {
	return func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return a, "stub", nil
	}
}

// countedTransportFactory builds a per-commit ReviewTransport factory whose
// transports count their calls and answer under the given invocation id,
// mimicking what the durable transport reports through its verified evidence.
func countedTransportFactory(calls *int, invocacion string) func(string, []string) review.ReviewTransport {
	return func(sha string, paths []string) review.ReviewTransport {
		return func(_, _, _ string, _ review.AuditorAgente) (string, string, error) {
			*calls++
			if sha == "" || strings.Contains(sha, " ") {
				return "", "", errors.New("transport bound without an audited sha")
			}
			return rebaseV2Response, invocacion, nil
		}
	}
}

// findV2FindingInFicha finds one v2 Hallazgo by exact description across every
// revision/dimension of a review record.
func findV2FindingInFicha(ficha review.Ficha, descripcion string) *review.Hallazgo {
	for _, rev := range ficha.Revisions {
		for _, dim := range rev.Dims {
			for i := range dim.Hallazgos {
				if dim.Hallazgos[i].Description == descripcion {
					return &dim.Hallazgos[i]
				}
			}
		}
	}
	return nil
}

func TestAnalizarRamaRebaseReusePreservesInvocationIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real-git-repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		gitEjecutarRebase(t, args...)
	}
	if err := os.WriteFile("base.txt", []byte(strings.Repeat("b\n", 5)), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarRebase(t, "add", "base.txt")
	gitEjecutarRebase(t, "commit", "-m", "feat(base): branch base")
	gitEjecutarRebase(t, "checkout", "-b", "feature")

	commitEnRamaRebase(t, "a.txt", "content-a\n")
	commitEnRamaRebase(t, "b.txt", "content-b\n")
	commitEnRamaRebase(t, "c.txt", "content-c\n")

	gitDir := filepath.Join(repo, ".git")
	ledger := review.NuevoLedger(gitDir)
	st := store.NuevoStore(gitDir)
	stub := &rebaseV2StubAgent{}

	callsPrimera := 0
	res, err := review.AnalizarRama(ledger, review.OpcionesRama{
		Fabrica:                rebaseV2StubFactory(stub),
		Parallel:               1,
		Store:                  st,
		ReviewTransportFactory: countedTransportFactory(&callsPrimera, originalInvocation),
	})
	if err != nil {
		t.Fatalf("first pass failed: %v", err)
	}
	if len(res.Pendientes) != 3 {
		t.Fatalf("Pendientes = %v, expected 3 new commits", res.Pendientes)
	}
	if callsPrimera == 0 {
		t.Fatal("the transport should have been invoked during the first pass")
	}
	if stub.calls != 0 {
		t.Fatalf("the agent was invoked directly %d times with a transport configured", stub.calls)
	}
	if len(res.SHAs) != 3 {
		t.Fatalf("pre-rebase SHAs = %v, expected 3 commits", res.SHAs)
	}
	shaBeforeRebase := res.SHAs[1]

	var fichaBeforeRebase *review.Ficha
	for i := range res.Fichas {
		if res.Fichas[i].SHA == shaBeforeRebase {
			fichaBeforeRebase = &res.Fichas[i]
		}
	}
	if fichaBeforeRebase == nil {
		t.Fatalf("no review record for %s after the first pass", shaBeforeRebase)
	}
	findingBeforeRebase := findV2FindingInFicha(*fichaBeforeRebase, invocationFindingDescription)
	if findingBeforeRebase == nil {
		t.Fatalf("the record for %s lost the v2 finding: %+v", shaBeforeRebase, fichaBeforeRebase)
	}
	if findingBeforeRebase.InvocationID != originalInvocation {
		t.Fatalf("initial InvocationID = %q, expected %q", findingBeforeRebase.InvocationID, originalInvocation)
	}

	// Real rebase: rewrites the 3 commit SHAs without touching content.
	gitEjecutarRebase(t, "checkout", "main")
	if err := os.WriteFile("docs.txt", []byte("docs\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarRebase(t, "add", "docs.txt")
	gitEjecutarRebase(t, "commit", "-m", "docs: advance main")
	gitEjecutarRebase(t, "checkout", "feature")
	gitEjecutarRebase(t, "rebase", "main")

	callsSegunda := 0
	res2, err := review.AnalizarRama(ledger, review.OpcionesRama{
		Fabrica:                rebaseV2StubFactory(stub),
		Parallel:               1,
		Store:                  st,
		ReviewTransportFactory: countedTransportFactory(&callsSegunda, secondPassInvocation),
	})
	if err != nil {
		t.Fatalf("second pass (post-rebase) failed: %v", err)
	}

	if len(res2.SHAs) != 3 || res2.SHAs[1] == shaBeforeRebase {
		t.Fatalf("post-rebase SHAs = %v, expected 3 REWRITTEN commits (before: %s)", res2.SHAs, shaBeforeRebase)
	}
	if len(res2.Pendientes) != 0 {
		t.Errorf("post-rebase Pendientes = %v, expected 0 (content already reviewed by blob)", res2.Pendientes)
	}
	if callsSegunda != 0 {
		t.Errorf("el transporte se invocó %d veces tras el rebase, esperado 0 calls nuevas", callsSegunda)
	}
	if stub.calls != 0 {
		t.Errorf("el revisor se invocó %d veces tras el rebase, esperado 0 calls de proveedor", stub.calls)
	}

	var fichaAfterRebase *review.Ficha
	for i := range res2.Fichas {
		if res2.Fichas[i].SHA == res2.SHAs[1] {
			fichaAfterRebase = &res2.Fichas[i]
		}
	}
	if fichaAfterRebase == nil {
		t.Fatalf("no adopted review record under post-rebase SHA %s", res2.SHAs[1])
	}
	findingAfterRebase := findV2FindingInFicha(*fichaAfterRebase, invocationFindingDescription)
	if findingAfterRebase == nil {
		t.Fatalf("the adopted record under %s lost the v2 finding: %+v", res2.SHAs[1], fichaAfterRebase)
	}
	if findingAfterRebase.InvocationID != originalInvocation {
		t.Fatalf("post-rebase InvocationID = %q, expected the original identity %q",
			findingAfterRebase.InvocationID, originalInvocation)
	}
}
