package review_test

// Test de caja negra (package review_test) a propósito: el criterio de
// salida de F2 exige inyectar un *store.Store real en AnalizarRama, y
// internal/store ya importa internal/review (review.Hallazgo, migración v1
// de review.Ledger). Un test interno (package review) que importara
// internal/store crearía el ciclo review→store→review; en caja negra no hay
// ciclo porque review_test no lo importa nadie.
//
// Por eso este archivo no puede reutilizar los helpers no exportados de
// rama_test.go (gitEjecutar, auditorStub, prepararRepoRama...): se duplican
// aquí, mínimos, solo para este test.

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// rebaseReviewerStub implements review.AuditorAgente and counts audit calls,
// excluding the unused overview. It returns a real finding so B-01 (findings
// lost after blob adoption) remains detectable.
type rebaseReviewerStub struct {
	calls int
}

// rebaseFindingDescription identifies the injected finding that must remain
// recoverable under the post-rebase SHA.
const rebaseFindingDescription = "rebase test finding"

func (a *rebaseReviewerStub) EjecutarPrompt(prompt string) (string, error) {
	a.calls++
	return "BEGIN_REVIEW\n" +
		`{"dim":"logic","verdict":"warn","findings":[{"dimension":"logic","file":"b.txt","line":1,"severity":"WARNING","description":"` + rebaseFindingDescription + `","suggestion":"review before rebase","evidence":"content-b","confidence":"high"}]}` +
		"\nEND_REVIEW\n", nil
}

func (a *rebaseReviewerStub) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func (a *rebaseReviewerStub) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

// fichaTieneHallazgo recorre todas las revisiones/dimensiones de una ficha
// buscando un ReviewFinding con esa descripción exacta.
func fichaTieneHallazgo(f review.Ficha, descripcion string) bool {
	for _, rev := range f.Revisions {
		for _, dim := range rev.Dims {
			for _, hallazgo := range dim.Findings {
				if hallazgo.Description == descripcion {
					return true
				}
			}
		}
	}
	return false
}

func fabricaStubRebase(a *rebaseReviewerStub) review.FabricaAuditor {
	return func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
		return a, "stub", nil
	}
}

func gitEjecutarRebase(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v falló: %v\n%s", args, err, out)
	}
}

// commitEnRamaRebase añade un archivo y commitea en la rama actual.
func commitEnRamaRebase(t *testing.T, nombre, contenido string) {
	t.Helper()
	if err := os.WriteFile(nombre, []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarRebase(t, "add", nombre)
	gitEjecutarRebase(t, "commit", "-m", "feat("+nombre+"): contenido de prueba")
}

// TestAnalizarRamaSobreviveRebaseViaBlob es el criterio de salida #1 de F2:
// "un git rebase que no altera contenido conserva el 100% de los findings".
// Audita 3 commits reales, hace un rebase real que reescribe sus SHAs sin
// tocar el contenido de ningún archivo, y comprueba que cero commits quedan
// pendientes y que el auditor NO se vuelve a invocar ni una sola vez más.
func TestAnalizarRamaSobreviveRebaseViaBlob(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
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
	gitEjecutarRebase(t, "commit", "-m", "feat(base): base de la rama")
	gitEjecutarRebase(t, "checkout", "-b", "feature")

	commitEnRamaRebase(t, "a.txt", "contenido a\n")
	commitEnRamaRebase(t, "b.txt", "contenido b\n")
	commitEnRamaRebase(t, "c.txt", "contenido c\n")

	gitDir := repo + "/.git"
	ledger := review.NuevoLedger(gitDir)
	st := store.NuevoStore(gitDir)
	stub := &rebaseReviewerStub{}

	res, err := review.AnalizarRama(ledger, review.OpcionesRama{
		Fabrica: fabricaStubRebase(stub), Parallel: 1, Store: st,
	})
	if err != nil {
		t.Fatalf("primera pasada falló: %v", err)
	}
	if len(res.Pendientes) != 3 {
		t.Fatalf("Pendientes = %v, esperado 3 commits nuevos", res.Pendientes)
	}
	callsBefore := stub.calls
	if callsBefore == 0 {
		t.Fatal("reviewer should have been called during the first pass")
	}
	if len(res.SHAs) != 3 {
		t.Fatalf("SHAs antes del rebase = %v, esperado 3 commits", res.SHAs)
	}
	// El commit del medio (b.txt) es el que rastreamos: SHAsRango devuelve
	// los SHAs en orden cronológico (--reverse), así que la posición se
	// conserva a través del rebase porque el rebase no reordena commits.
	shaBAntes := res.SHAs[1]

	// Rebase real: avanza main con un commit ajeno a "feature" y reescribe
	// los 3 commits de feature sobre esa nueva base. El contenido de a.txt,
	// b.txt y c.txt no cambia una línea, pero sus SHAs sí (el padre cambió).
	gitEjecutarRebase(t, "checkout", "main")
	if err := os.WriteFile("docs.txt", []byte("docs\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarRebase(t, "add", "docs.txt")
	gitEjecutarRebase(t, "commit", "-m", "docs: avanzar main")
	gitEjecutarRebase(t, "checkout", "feature")
	gitEjecutarRebase(t, "rebase", "main")

	res2, err := review.AnalizarRama(ledger, review.OpcionesRama{
		Fabrica: fabricaStubRebase(stub), Parallel: 1, Store: st,
	})
	if err != nil {
		t.Fatalf("segunda pasada (post-rebase) falló: %v", err)
	}
	if len(res2.Pendientes) != 0 {
		t.Errorf("Pendientes tras el rebase = %v, esperado 0 (contenido ya revisado por blob)", res2.Pendientes)
	}
	if stub.calls != callsBefore {
		t.Errorf("reviewer was called %d additional times after rebase; expected 0", stub.calls-callsBefore)
	}

	// El criterio de salida real de F2 no es solo "Pendientes = 0 y el
	// auditor no se re-invoca" (eso ya lo prueba lo anterior): es que el
	// HALLAZGO REAL sigue siendo recuperable bajo el SHA nuevo del commit
	// reescrito. Antes del fix de B-01, AnalizarRama hacía "continue" sin
	// escribir nada bajo el SHA nuevo y este hallazgo desaparecía de
	// res2.Fichas.
	if len(res2.SHAs) != 3 {
		t.Fatalf("SHAs tras el rebase = %v, esperado 3 commits", res2.SHAs)
	}
	shaBDespues := res2.SHAs[1]
	if shaBDespues == shaBAntes {
		t.Fatal("el SHA de b.txt no cambió tras el rebase: el test no prueba nada")
	}

	var fichaB *review.Ficha
	for i := range res2.Fichas {
		if res2.Fichas[i].SHA == shaBDespues {
			fichaB = &res2.Fichas[i]
		}
	}
	if fichaB == nil {
		t.Fatalf("no hay ficha para el SHA post-rebase de b.txt (%s) en res2.Fichas: el hallazgo real desapareció", shaBDespues)
	}
	if !fichaTieneHallazgo(*fichaB, rebaseFindingDescription) {
		t.Errorf("la ficha adoptada bajo %s no conserva el hallazgo real: %+v", shaBDespues, fichaB)
	}
}

// gitOutputRebase runs git and returns its trimmed stdout. The stacked test
// needs the pre-rebase tip of the parent layer to rebase the child onto the
// rewritten parent, and gitEjecutarRebase discards output.
func gitOutputRebase(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		t.Fatalf("git %v falló: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestStackedBranchSurvivesBaseRebaseViaBlob is F8 exit criterion 2: rebasing
// the base of a stacked PR preserves the review of everything that did not
// change. It differs from TestAnalizarRamaSobreviveRebaseViaBlob (F2 criterion
// 1, no stack) because here the rewritten commit is the PARENT layer: the
// child's own range is recomputed against a parent whose SHA changed, so blob
// coverage has to survive both rewrites at once.
func TestStackedBranchSurvivesBaseRebaseViaBlob(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
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
	gitEjecutarRebase(t, "commit", "-m", "feat(base): base de la pila")

	// Stack main → layer-a → layer-b. layer-a is the parent PR; layer-b is
	// the one under review.
	gitEjecutarRebase(t, "checkout", "-b", "layer-a")
	commitEnRamaRebase(t, "a.txt", "contenido a\n")
	gitEjecutarRebase(t, "checkout", "-b", "layer-b")
	commitEnRamaRebase(t, "b1.txt", "contenido b1\n")
	commitEnRamaRebase(t, "b2.txt", "contenido b2\n")

	gitDir := repo + "/.git"
	ledger := review.NuevoLedger(gitDir)
	st := store.NuevoStore(gitDir)
	stub := &rebaseReviewerStub{}
	opciones := func() review.OpcionesRama {
		return review.OpcionesRama{
			Base: "main", Fabrica: fabricaStubRebase(stub), Parallel: 1, Store: st,
			OwnDiff: &review.OwnDiffOptions{Parent: "layer-a"},
		}
	}

	res, err := review.AnalizarRama(ledger, opciones())
	if err != nil {
		t.Fatalf("primera pasada falló: %v", err)
	}
	// Own range only: a.txt belongs to layer-a and must not be audited here.
	if len(res.SHAs) != 2 {
		t.Fatalf("SHAs = %v, esperado los 2 commits propios de layer-b", res.SHAs)
	}
	if len(res.Pendientes) != 2 {
		t.Fatalf("Pendientes = %v, esperado 2", res.Pendientes)
	}
	callsBefore := stub.calls
	if callsBefore == 0 {
		t.Fatal("el auditor debería haberse llamado en la primera pasada")
	}
	shaB1Antes := res.SHAs[0]

	// Rebase of the STACK BASE: main advances, layer-a is rewritten onto it,
	// and layer-b is replanted onto the rewritten layer-a. Not a line of
	// b1.txt or b2.txt changes, but every SHA in the stack does.
	tipAAntes := gitOutputRebase(t, "rev-parse", "layer-a")
	gitEjecutarRebase(t, "checkout", "main")
	if err := os.WriteFile("docs.txt", []byte("docs\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarRebase(t, "add", "docs.txt")
	gitEjecutarRebase(t, "commit", "-m", "docs: avanzar main")
	gitEjecutarRebase(t, "checkout", "layer-a")
	gitEjecutarRebase(t, "rebase", "main")
	gitEjecutarRebase(t, "checkout", "layer-b")
	gitEjecutarRebase(t, "rebase", "--onto", "layer-a", tipAAntes)

	res2, err := review.AnalizarRama(ledger, opciones())
	if err != nil {
		t.Fatalf("segunda pasada (post-rebase de la base) falló: %v", err)
	}
	if len(res2.SHAs) != 2 {
		t.Fatalf("SHAs tras el rebase = %v, esperado 2", res2.SHAs)
	}
	shaB1Despues := res2.SHAs[0]
	if shaB1Despues == shaB1Antes {
		t.Fatal("el SHA de b1.txt no cambió tras rebasar la base: el test no prueba nada")
	}
	if len(res2.Pendientes) != 0 {
		t.Errorf("Pendientes tras rebasar la base = %v, esperado 0 (contenido ya revisado por blob)", res2.Pendientes)
	}
	if stub.calls != callsBefore {
		t.Errorf("el auditor se llamó %d veces más tras rebasar la base; esperado 0", stub.calls-callsBefore)
	}

	// El criterio no es solo "no se re-audita": el hallazgo real debe seguir
	// recuperable bajo el SHA nuevo del commit reescrito.
	var fichaB1 *review.Ficha
	for i := range res2.Fichas {
		if res2.Fichas[i].SHA == shaB1Despues {
			fichaB1 = &res2.Fichas[i]
		}
	}
	if fichaB1 == nil {
		t.Fatalf("no hay ficha para el SHA post-rebase de b1.txt (%s): la revisión se perdió al rebasar la base", shaB1Despues)
	}
	if !fichaTieneHallazgo(*fichaB1, rebaseFindingDescription) {
		t.Errorf("la ficha adoptada bajo %s no conserva el hallazgo real: %+v", shaB1Despues, fichaB1)
	}
}
