package review

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// auditOutputCritical is a valid audit JSONL that reports one CRITICAL finding
// for every commit audited with it.
const auditOutputCritical = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"x.go\",\"line\":1,\"severity\":\"CRITICAL\",\"description\":\"injected defect\",\"evidence\":\"defect\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"

// stackRepo is a temporary repository holding the stack main -> feature-a ->
// feature-b (current branch: feature-b), plus the SHAs needed by assertions.
type stackRepo struct {
	gitDir  string
	baseSHA string // tip of main
	shaA    string // feature-a's own commit
	shaB    string // feature-b's own commit (current HEAD)
}

// prepareStackRepo builds the three-branch stack required by the T8.2
// acceptance criteria on a real, temporary Git repository.
func prepareStackRepo(t *testing.T) stackRepo {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		gitEjecutar(t, args...)
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("b\nb\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutar(t, "add", "base.txt")
	gitEjecutar(t, "commit", "-m", "feat(base): trunk")
	pila := stackRepo{gitDir: filepath.Join(repo, ".git")}
	pila.baseSHA = strings.TrimSpace(gitSalida(t, "rev-parse", "HEAD"))
	gitEjecutar(t, "checkout", "-b", "feature-a")
	pila.shaA = commitEnRama(t, "a.txt", "1\n2\n3\n4\n5\n")
	gitEjecutar(t, "checkout", "-b", "feature-b")
	pila.shaB = commitEnRama(t, "b.txt", "x\ny\nz\n")
	return pila
}

// swapParentResolver swaps the parent resolver with a fake for the test.
func swapParentResolver(t *testing.T, reemplazo ParentResolver) {
	t.Helper()
	anterior := defaultParentResolver
	defaultParentResolver = reemplazo
	t.Cleanup(func() { defaultParentResolver = anterior })
}

func fixedResolver(ref string) ParentResolver {
	return func(git.ParentResolutionOptions) (git.ParentResolution, error) {
		return git.ParentResolution{
			Reference: ref,
			Source:    git.ParentSourceLocalMergeBase,
			Evidence:  []string{"local merge-base winner=" + ref},
		}, nil
	}
}

// TestStackedBranchExplainsRanges: with the parent resolved, the
// reviewed range is only the own diff (merge_base(parent, HEAD)..HEAD) and
// parent/base/range evidence is explained on the result.
func TestStackedBranchExplainsRanges(t *testing.T) {
	pila := prepareStackRepo(t)
	swapParentResolver(t, fixedResolver("feature-a"))

	res, err := AnalizarRama(NuevoLedger(pila.gitDir), OpcionesRama{
		Fabrica:  fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		Parallel: 2,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalizarRama failed: %v", err)
	}
	if res.Propio == nil {
		t.Fatal("Propio = nil, expected stacked resolution")
	}
	if res.Propio.Parent != "feature-a" || res.Propio.ParentSource != string(git.ParentSourceLocalMergeBase) {
		t.Errorf("parent = %s/%s, want feature-a/local_merge_base", res.Propio.Parent, res.Propio.ParentSource)
	}
	if res.Propio.Base != "main" || res.Propio.ContextoDesde != pila.baseSHA {
		t.Errorf("context = %s from %s, want main from %s", res.Propio.Base, res.Propio.ContextoDesde, pila.baseSHA)
	}
	if res.Propio.PropioDesde != pila.shaA {
		t.Errorf("PropioDesde = %s, want %s", res.Propio.PropioDesde, pila.shaA)
	}
	if len(res.Propio.Evidencia) == 0 {
		t.Error("empty Evidencia: the resolution must be explainable")
	}
	if len(res.SHAs) != 1 || res.SHAs[0] != pila.shaB {
		t.Errorf("SHAs = %v, want only the own diff [%s]", res.SHAs, pila.shaB)
	}
	if res.Volumen != 3 {
		t.Errorf("Volumen = %d, want 3 (own commit only)", res.Volumen)
	}
}

// TestStackedBranchInheritedFindingDoesNotBlock: the CRITICAL introduced by
// A shows up as inherited when reviewing B and never makes B blocking.
func TestStackedBranchInheritedFindingDoesNotBlock(t *testing.T) {
	pila := prepareStackRepo(t)
	ledger := NuevoLedger(pila.gitDir)
	swapParentResolver(t, fixedResolver("feature-a"))

	gitEjecutar(t, "checkout", "feature-a")
	if _, err := AnalizarRama(ledger, OpcionesRama{
		Fabrica:  fabricaStub(&auditorStub{auditSalida: auditOutputCritical}),
		Parallel: 1,
	}); err != nil {
		t.Fatalf("audit of A failed: %v", err)
	}
	gitEjecutar(t, "checkout", "feature-b")

	res, err := AnalizarRama(ledger, OpcionesRama{
		Fabrica:  fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalizarRama failed: %v", err)
	}
	if len(res.Heredados) == 0 {
		t.Fatal("empty Heredados: A's CRITICAL must surface as inherited")
	}
	for _, h := range res.Heredados {
		if h.SHA != pila.shaA || h.Hallazgo.Severity != SevCritical {
			t.Errorf("wrong inherited finding: %+v", h)
		}
	}
	if bloqueantes := BloqueantesDeRama(res.Fichas); len(bloqueantes) != 0 {
		t.Errorf("B is blocking with %d own critical findings: A's finding is inherited", len(bloqueantes))
	}
	if len(res.Fichas) != 1 || res.Fichas[0].SHA != pila.shaB {
		t.Errorf("Fichas = %v, esperado solo la propia de B", fichasSHAs(res.Fichas))
	}
}

// TestStackedBranchOwnFindingBlocks: a CRITICAL inside B's own diff
// stays own/current and blocks its PR.
func TestStackedBranchOwnFindingBlocks(t *testing.T) {
	pila := prepareStackRepo(t)
	swapParentResolver(t, fixedResolver("feature-a"))

	res, err := AnalizarRama(NuevoLedger(pila.gitDir), OpcionesRama{
		Fabrica:  fabricaStub(&auditorStub{auditSalida: auditOutputCritical}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalizarRama failed: %v", err)
	}
	if bloqueantes := BloqueantesDeRama(res.Fichas); len(bloqueantes) == 0 {
		t.Fatal("0 blockers: an own finding must block")
	}
	if len(res.Heredados) != 0 {
		t.Errorf("Heredados = %d, want 0 (no context fichas)", len(res.Heredados))
	}
}

// TestStackedBranchContextIsReadOnly: context commits without a ficha
// are neither audited nor become pending; fixing them belongs to the parent PR.
func TestStackedBranchContextIsReadOnly(t *testing.T) {
	pila := prepareStackRepo(t)
	swapParentResolver(t, fixedResolver("feature-a"))

	res, err := AnalizarRama(NuevoLedger(pila.gitDir), OpcionesRama{
		Fabrica:  fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{ResolveParent: true},
	})
	if err != nil {
		t.Fatalf("AnalizarRama failed: %v", err)
	}
	for _, sha := range res.Pendientes {
		if sha == pila.shaA {
			t.Error("context commit entered Pendientes: context must not be reviewed")
		}
	}
	fichaA, err := NuevoLedger(pila.gitDir).LeerFicha(pila.shaA)
	if err != nil {
		t.Fatal(err)
	}
	if fichaA != nil {
		t.Error("the context commit was audited: stacked mode must be read-only outside the own diff")
	}
}

// TestStackedBranchExplicitParent: explicit internal input goes through
// the T8.1 seam and is verified as a real commit.
func TestStackedBranchExplicitParent(t *testing.T) {
	pila := prepareStackRepo(t)

	res, err := AnalizarRama(NuevoLedger(pila.gitDir), OpcionesRama{
		Fabrica:  fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		Parallel: 1,
		OwnDiff:  &OwnDiffOptions{Parent: "feature-a"},
	})
	if err != nil {
		t.Fatalf("AnalizarRama failed: %v", err)
	}
	if res.Propio.Parent != "feature-a" || res.Propio.ParentSource != string(git.ParentSourceExplicit) {
		t.Errorf("parent = %s/%s, want feature-a/explicit", res.Propio.Parent, res.Propio.ParentSource)
	}

	_, err = AnalizarRama(NuevoLedger(pila.gitDir), OpcionesRama{
		Fabrica: fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		OwnDiff: &OwnDiffOptions{Parent: "no-existe"},
	})
	if err == nil || !strings.Contains(err.Error(), "not a commit") {
		t.Errorf("missing reference = %v, want explicit failure", err)
	}
}

// TestStackedBranchNoSignalFailsWithoutMain: without a reliable parent signal
// the analysis fails explicitly; main is never assumed as the parent.
func TestStackedBranchNoSignalFailsWithoutMain(t *testing.T) {
	pila := prepareStackRepo(t)
	swapParentResolver(t, func(git.ParentResolutionOptions) (git.ParentResolution, error) {
		return git.ParentResolution{}, &git.ParentResolutionError{
			Reason:   "no reliable parent signal",
			Evidence: []string{"local merge-base candidates: none"},
		}
	})

	res, err := AnalizarRama(NuevoLedger(pila.gitDir), OpcionesRama{
		Fabrica: fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		OwnDiff: &OwnDiffOptions{ResolveParent: true},
	})
	if err == nil || !strings.Contains(err.Error(), "no reliable parent signal") {
		t.Fatalf("error = %v, want explicit failure for missing signal", err)
	}
	if res != nil && res.Propio != nil && res.Propio.Parent == "main" {
		t.Error("main was assumed as parent: forbidden in a stack")
	}

	// End to end against the REAL T8.1 resolver: restore it first so this
	// block genuinely exercises git.ResolveParentBranch instead of the fake
	// above. A branch with no pull request, no upstream and no local sibling
	// branches has no reliable signal and must fail instead of falling back
	// to main. The resolver's evidence always names the detected current
	// branch; requiring it proves the fake is out of the loop.
	swapParentResolver(t, git.ResolveParentBranch)
	sola := prepararRepoRama(t)
	if _, err := AnalizarRama(NuevoLedger(sola), OpcionesRama{
		Fabrica: fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		OwnDiff: &OwnDiffOptions{ResolveParent: true},
	}); err == nil || !strings.Contains(err.Error(), "could not resolve parent branch") ||
		!strings.Contains(err.Error(), "current branch=") {
		t.Fatalf("error = %v, want explicit failure from the REAL T8.1 resolver", err)
	}
}

// TestStackedBranchWithoutOptionsFails: enabling stacked mode without an
// explicit parent or resolution is a configuration error, not silence.
func TestStackedBranchWithoutOptionsFails(t *testing.T) {
	pila := prepareStackRepo(t)
	_, err := AnalizarRama(NuevoLedger(pila.gitDir), OpcionesRama{
		Fabrica: fabricaStub(&auditorStub{auditSalida: salidaAuditOK}),
		OwnDiff: &OwnDiffOptions{},
	})
	if err == nil || !errors.Is(err, errOwnDiffWithoutParent) {
		t.Fatalf("error = %v, esperado errOwnDiffWithoutParent", err)
	}
}

func fichasSHAs(fichas []Ficha) []string {
	shas := make([]string, 0, len(fichas))
	for _, ficha := range fichas {
		shas = append(shas, ficha.SHA)
	}
	return shas
}
