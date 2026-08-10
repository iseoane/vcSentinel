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
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// stubAuditorRebase implementa review.AuditorAgente y cuenta cuántas veces
// se le invoca para auditar (no para el overview, que no se usa aquí).
type stubAuditorRebase struct {
	llamadas int
}

func (a *stubAuditorRebase) EjecutarPrompt(prompt string) (string, error) {
	a.llamadas++
	return "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"ok\"}\nEND_REVIEW\n", nil
}

func fabricaStubRebase(a *stubAuditorRebase) review.FabricaAuditor {
	return func(dimension string) (review.AuditorAgente, string, error) {
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
	stub := &stubAuditorRebase{}

	res, err := review.AnalizarRama(ledger, review.OpcionesRama{
		Fabrica: fabricaStubRebase(stub), Parallel: 1, Store: st,
	})
	if err != nil {
		t.Fatalf("primera pasada falló: %v", err)
	}
	if len(res.Pendientes) != 3 {
		t.Fatalf("Pendientes = %v, esperado 3 commits nuevos", res.Pendientes)
	}
	llamadasAntes := stub.llamadas
	if llamadasAntes == 0 {
		t.Fatal("el auditor debería haberse invocado en la primera pasada")
	}

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
	if stub.llamadas != llamadasAntes {
		t.Errorf("el auditor se invocó %d veces más tras el rebase, esperado 0 llamadas nuevas", stub.llamadas-llamadasAntes)
	}
}
