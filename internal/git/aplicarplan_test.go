package git

import (
	"errors"
	"os"
	"testing"
)

// prepararPlanConGigante deja el repo con un archivo normal y otro gigante,
// y devuelve el plan emitido (con su decisión pendiente).
func prepararPlanConGigante(t *testing.T) *PlanSerializado {
	t.Helper()
	prepararRepoTemp(t)
	commitEnRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir app.go: %v", err)
	}
	escribirGigante(t, "gigante.go")

	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("ConstruirPlanParaAgente falló: %v", err)
	}
	if len(plan.DecisionesPendientes) != 1 {
		t.Fatalf("se esperaba 1 decisión pendiente, obtuve %d", len(plan.DecisionesPendientes))
	}
	return plan
}

func respuestasBypass(plan *PlanSerializado) RespuestasPlan {
	respuestas := map[string]string{}
	for _, d := range plan.DecisionesPendientes {
		respuestas[d.ID] = RespuestaBypass
	}
	return RespuestasPlan{PlanID: plan.PlanID, Respuestas: respuestas}
}

// TestAplicarPlanRechazaArbolModificado: ligadura al árbol. Un plan calculado
// sobre otro estado no se ejecuta.
func TestAplicarPlanRechazaArbolModificado(t *testing.T) {
	plan := prepararPlanConGigante(t)
	commitsAntes := contarCommits(t)

	if err := os.WriteFile("app.go", []byte("package app\n\nfunc Nuevo() {}\n"), 0644); err != nil {
		t.Fatalf("no se pudo modificar app.go: %v", err)
	}

	_, err := AplicarPlanAprobado(plan, respuestasBypass(plan))
	if !errors.Is(err, ErrArbolCambiado) {
		t.Fatalf("error = %v, esperado ErrArbolCambiado", err)
	}
	if contarCommits(t) != commitsAntes {
		t.Error("se crearon commits pese al rechazo")
	}
}

// TestAplicarPlanRechazaRespuestasDeOtroPlan: ligadura al plan. Una aprobación
// de un plan anterior no sirve para uno nuevo.
func TestAplicarPlanRechazaRespuestasDeOtroPlan(t *testing.T) {
	plan := prepararPlanConGigante(t)
	commitsAntes := contarCommits(t)

	respuestas := respuestasBypass(plan)
	respuestas.PlanID = "plan-anterior"

	_, err := AplicarPlanAprobado(plan, respuestas)
	if !errors.Is(err, ErrPlanNoCoincide) {
		t.Fatalf("error = %v, esperado ErrPlanNoCoincide", err)
	}
	if contarCommits(t) != commitsAntes {
		t.Error("se crearon commits pese al rechazo")
	}
}

// TestAplicarPlanExigeRespuestaExplicita: sin valores por defecto. Ni siquiera
// «aprobar todo» es implícito.
func TestAplicarPlanExigeRespuestaExplicita(t *testing.T) {
	plan := prepararPlanConGigante(t)
	commitsAntes := contarCommits(t)

	_, err := AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID, Respuestas: map[string]string{}})
	if !errors.Is(err, ErrDecisionSinRespuesta) {
		t.Fatalf("error = %v, esperado ErrDecisionSinRespuesta", err)
	}
	if contarCommits(t) != commitsAntes {
		t.Error("se crearon commits pese al rechazo")
	}
}

// TestAplicarPlanRespetaElAborto: responder "abortar" no commitea nada.
func TestAplicarPlanRespetaElAborto(t *testing.T) {
	plan := prepararPlanConGigante(t)
	commitsAntes := contarCommits(t)

	respuestas := respuestasBypass(plan)
	respuestas.Respuestas[plan.DecisionesPendientes[0].ID] = RespuestaAbortar

	_, err := AplicarPlanAprobado(plan, respuestas)
	if !errors.Is(err, ErrDecisionAbortada) {
		t.Fatalf("error = %v, esperado ErrDecisionAbortada", err)
	}
	if contarCommits(t) != commitsAntes {
		t.Error("se crearon commits pese al aborto")
	}
}

// TestAplicarPlanCaminoFeliz: los commits esperados, con los mensajes del plan.
func TestAplicarPlanCaminoFeliz(t *testing.T) {
	plan := prepararPlanConGigante(t)

	resultados, err := AplicarPlanAprobado(plan, respuestasBypass(plan))
	if err != nil {
		t.Fatalf("AplicarPlanAprobado devolvió error: %v", err)
	}
	if len(resultados) != len(plan.Lotes) {
		t.Fatalf("commits = %d, esperado %d", len(resultados), len(plan.Lotes))
	}
	for i, resultado := range resultados {
		if resultado.Mensaje != plan.Lotes[i].Mensaje {
			t.Errorf("commit %d: mensaje %q, esperado %q", i, resultado.Mensaje, plan.Lotes[i].Mensaje)
		}
	}
	limpio, err := WorktreeLimpio()
	if err != nil {
		t.Fatalf("WorktreeLimpio falló: %v", err)
	}
	if !limpio {
		t.Error("quedaron cambios pendientes tras aplicar el plan completo")
	}
}
