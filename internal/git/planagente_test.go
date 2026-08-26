package git

import (
	"errors"
	"os"
	"strings"
	"testing"
)

type adapterPlanFake struct {
	mensaje string
	err     error
	diff    string
}

func (a *adapterPlanFake) ObtenerMensajeCommit([]string, string, int) (string, error) {
	return a.mensaje, a.err
}

func (a *adapterPlanFake) ObtenerMensajeCommitConDiff(_ []string, _ string, _ int, diff string) (string, error) {
	a.diff = diff
	return a.mensaje, a.err
}

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
	if len(plan.DecisionesPendientes) != 0 {
		t.Fatalf("la clase indivisible ya no pregunta; obtuve %d pendientes", len(plan.DecisionesPendientes))
	}
	if len(plan.DecisionesAutomaticas) != 1 {
		t.Fatalf("se esperaba 1 decisión automática, obtuve %d", len(plan.DecisionesAutomaticas))
	}
	automatica := plan.DecisionesAutomaticas[0]
	if automatica.Archivo != "gigante.go" {
		t.Errorf("archivo de la decisión = %q, esperado gigante.go", automatica.Archivo)
	}
	if automatica.ID == "" {
		t.Error("la decisión automática no tiene id")
	}
	if automatica.Motivo != MotivoIndivisible {
		t.Errorf("motivo = %q, esperado %q", automatica.Motivo, MotivoIndivisible)
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

func TestConstruirPlanParaAgenteGeneraMensajesConFallbackEstable(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "base.txt", "base\n")
	commitEnRepo(t, "app.go", "package app\n")
	if err := os.WriteFile("app.go", []byte("package app\n\nfunc nueva() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	exitoso := &adapterPlanFake{mensaje: "feat(slice): describir lote"}
	semantico, err := ConstruirPlanParaAgenteConAdapter(exitoso)
	if err != nil {
		t.Fatalf("plan semántico falló: %v", err)
	}
	fallido, err := ConstruirPlanParaAgenteConAdapter(&adapterPlanFake{err: errors.New("agente caído")})
	if err != nil {
		t.Fatalf("plan con fallback falló: %v", err)
	}
	if semantico.Lotes[0].Mensaje != "feat(slice): describir lote" {
		t.Fatalf("mensaje semántico = %q", semantico.Lotes[0].Mensaje)
	}
	if !strings.Contains(exitoso.diff, "+func nueva()") {
		t.Fatalf("el adaptador no recibió el micro-diff: %q", exitoso.diff)
	}
	if fallido.Lotes[0].Mensaje != "chore(slice): auto-fragmented backend batch #1" {
		t.Fatalf("fallback = %q", fallido.Lotes[0].Mensaje)
	}
	if semantico.PlanID != fallido.PlanID {
		t.Fatalf("PlanID depende del mensaje: %q != %q", semantico.PlanID, fallido.PlanID)
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
	antes, err := HashEstadoWorktree([]string{"app.go"})
	if err != nil {
		t.Fatalf("HashEstadoWorktree falló: %v", err)
	}
	if err := os.WriteFile("app.go", []byte("package app\n\nfunc Nuevo() {}\n"), 0644); err != nil {
		t.Fatalf("no se pudo reescribir app.go: %v", err)
	}
	despues, err := HashEstadoWorktree([]string{"app.go"})
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
