package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// inspectorFalso reproduce lo que ve un lector sin cerrojo: unas cuantas
// observaciones del tail a medio escribir y después el estado real.
type inspectorFalso struct {
	tailsPendientes  int
	llamadas         int
	estadoFinal      agentrun.LifecycleState
	errorPersistente error
}

func (i *inspectorFalso) Inspect(context.Context, agentrun.Identity) (execution.Inspection, error) {
	i.llamadas++
	if i.errorPersistente != nil {
		return execution.Inspection{}, i.errorPersistente
	}
	if i.tailsPendientes > 0 {
		i.tailsPendientes--
		return execution.Inspection{}, store.IncompleteEventTailError{RunID: "run-de-prueba"}
	}
	return execution.Inspection{Projection: store.RunProjection{State: i.estadoFinal}}, nil
}

// TestObserveUntilSettledToleraUnTailAMedioEscribir cubre un flake real: la
// suite completa fallaba ~1 de cada 5 pasadas con "never reached a stable state
// after recover: store: incomplete final execution event".
//
// Los lectores del store son deliberadamente SIN CERROJO (ver el comentario de
// ReadAttemptOutcomes) mientras el escritor sí lo toma, así que un lector puede
// observar el último registro JSONL a medio anexar. El propio store llama a ese
// estado "recoverable": se resuelve en cuanto termina la escritura. Pero
// observeUntilSettled salía con error ante CUALQUIER fallo de Inspect, así que
// convertía un "todavía no" en un fallo de infraestructura.
func TestObserveUntilSettledToleraUnTailAMedioEscribir(t *testing.T) {
	inspector := &inspectorFalso{tailsPendientes: 3, estadoFinal: agentrun.StateSucceeded}

	proyeccion, err := observeUntilSettled(context.Background(), inspector, "run-de-prueba")

	if err != nil {
		t.Fatalf("err = %v; un tail a medio escribir es transitorio, no un fallo", err)
	}
	if proyeccion.State != agentrun.StateSucceeded {
		t.Errorf("estado = %q, esperado %q", proyeccion.State, agentrun.StateSucceeded)
	}
	if inspector.llamadas != 4 {
		t.Errorf("llamadas = %d, esperado 4 (tres tails y la observación buena)", inspector.llamadas)
	}
}

// TestObserveUntilSettledNoTragaUnTailQueNuncaSeCompleta fija el otro lado: un
// tail truncado de verdad, dejado por un proceso que murió a media escritura,
// NO se resuelve solo. Tolerarlo sin límite colgaría el comando para siempre.
func TestObserveUntilSettledNoTragaUnTailQueNuncaSeCompleta(t *testing.T) {
	inspector := &inspectorFalso{errorPersistente: store.IncompleteEventTailError{RunID: "run-de-prueba"}}

	_, err := observeUntilSettled(context.Background(), inspector, "run-de-prueba")

	if err == nil {
		t.Fatal("un tail que nunca se completa debe acabar reportándose, no esperarse eternamente")
	}
	if !errors.Is(err, store.ErrIncompleteEventTail) {
		t.Errorf("err = %v, esperado que conserve ErrIncompleteEventTail", err)
	}
}

// TestObserveUntilSettledNoReintentaOtrosErrores: solo el tail incompleto es
// transitorio. Cualquier otro fallo sale inmediatamente, como antes.
func TestObserveUntilSettledNoReintentaOtrosErrores(t *testing.T) {
	inspector := &inspectorFalso{errorPersistente: errors.New("store corrupto")}

	if _, err := observeUntilSettled(context.Background(), inspector, "run-de-prueba"); err == nil {
		t.Fatal("esperado error")
	}
	if inspector.llamadas != 1 {
		t.Errorf("llamadas = %d, esperado 1: un error no transitorio no se reintenta", inspector.llamadas)
	}
}
