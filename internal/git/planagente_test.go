package git

import (
	"os"
	"strings"
	"testing"
)

// escribirGigante crea un archivo de código con más líneas que
// LimiteCodigoGigante para forzar la rama de decisión pendiente.
func escribirGigante(t *testing.T, nombre string) {
	t.Helper()
	contenido := strings.Repeat("// línea de relleno\n", LimiteCodigoGigante+50)
	if err := os.WriteFile(nombre, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", nombre, err)
	}
}

// TestConstruirPlanParaAgenteRegistraDecisionSinCommitear cubre la aceptación
// de T0.9: con un archivo de código gigante, el plan expone la decisión
// pendiente y no crea ningún commit.
func TestConstruirPlanParaAgenteRegistraDecisionSinCommitear(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "base.txt", "base\n")
	commitsAntes := contarCommits(t)

	escribirGigante(t, "gigante.go")

	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("ConstruirPlanParaAgente devolvió error: %v", err)
	}
	if len(plan.DecisionesPendientes) != 1 {
		t.Fatalf("se esperaba 1 decisión pendiente, obtuve %d", len(plan.DecisionesPendientes))
	}
	decision := plan.DecisionesPendientes[0]
	if decision.Archivo != "gigante.go" {
		t.Errorf("archivo de la decisión = %q, esperado gigante.go", decision.Archivo)
	}
	if decision.ID == "" {
		t.Error("la decisión pendiente no tiene id")
	}
	esperadas := []string{RespuestaBypass, RespuestaAbortar}
	if strings.Join(decision.Opciones, ",") != strings.Join(esperadas, ",") {
		t.Errorf("opciones = %v, esperado %v", decision.Opciones, esperadas)
	}
	if commitsAntes != contarCommits(t) {
		t.Error("ConstruirPlanParaAgente creó commits: debe proponer sin ejecutar")
	}
}

// TestConstruirPlanParaAgenteEsIdempotente cubre la segunda aceptación de
// T0.9: dos ejecuciones seguidas sobre el mismo árbol dan el mismo plan_id y
// el mismo estado_worktree.
func TestConstruirPlanParaAgenteEsIdempotente(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir app.go: %v", err)
	}

	primero, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("primera ejecución falló: %v", err)
	}
	segundo, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("segunda ejecución falló: %v", err)
	}
	if primero.PlanID == "" {
		t.Fatal("plan_id vacío")
	}
	if primero.PlanID != segundo.PlanID {
		t.Errorf("plan_id no determinista: %q vs %q", primero.PlanID, segundo.PlanID)
	}
	if primero.EstadoWorktree != segundo.EstadoWorktree {
		t.Errorf("estado_worktree no determinista: %q vs %q", primero.EstadoWorktree, segundo.EstadoWorktree)
	}
}

// TestEstadoWorktreeCambiaConElContenido protege la ligadura al árbol que
// T0.10 usará para negarse a aplicar un plan calculado sobre otro estado.
func TestEstadoWorktreeCambiaConElContenido(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir app.go: %v", err)
	}
	antes, err := HashEstadoWorktree()
	if err != nil {
		t.Fatalf("HashEstadoWorktree falló: %v", err)
	}
	if err := os.WriteFile("app.go", []byte("package app\n\nfunc Nuevo() {}\n"), 0644); err != nil {
		t.Fatalf("no se pudo reescribir app.go: %v", err)
	}
	despues, err := HashEstadoWorktree()
	if err != nil {
		t.Fatalf("HashEstadoWorktree falló: %v", err)
	}
	if antes == despues {
		t.Error("el hash del árbol no cambió tras modificar el contenido de un archivo")
	}
}

func contarCommits(t *testing.T) string {
	t.Helper()
	salida, err := ejecutarGitSalida("rev-list", "--count", "HEAD")
	if err != nil {
		t.Fatalf("no se pudo contar commits: %v", err)
	}
	return strings.TrimSpace(salida)
}
