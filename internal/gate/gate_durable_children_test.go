// Cutover proof for the wiring-time review-child learning seam (ticket 11
// slice 3). Review candidate identities are process-salted inside the shared
// durable transport, so the orchestrator learns its actually-admitted review
// children from the DurableReviewChildren observer sink. This file pins that,
// when the factory routes reviews into the SAME store as the root run, the
// root settlement's "|children=" enumeration equals the persisted ParentRunID
// scan — the exact production cutover shape.
package gate

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func TestGateDurableLearnedReviewChildrenEnumerateScanned(t *testing.T) {
	const bloqueoJSON = `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo confirmable"}]}`
	transportes := 0
	var aprendidos []string

	base := opcionesBase(cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil),
		fabricaContadora(new(int), bloqueoJSON, nil))
	base.EjecutarValidacion = ejecutarPerfilSinCandidato
	base.FabricaRefutador = func() (review.AuditorAgente, string, error) {
		return &auditorFalso{salida: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
	}

	durable := base
	durable.DurableRuns = true
	durable.Stage = "pre-push"
	durable.CandidateSHA = base.OpcionesRevision.SHA
	// ONE store for the root run, the validation-job settlements, and every
	// routed review invocation: the production cutover shape.
	st := store.NuevoStore(filepath.Join(t.TempDir(), "gate-common"))
	durable.DurableStore = st
	durable.DurableReviewChildren = func() []agentrun.Identity {
		ids := make([]agentrun.Identity, 0, len(aprendidos))
		for _, id := range aprendidos {
			ids = append(ids, agentrun.Identity(id))
		}
		return ids
	}
	durable.DurableReviewTransportFactory = func(rootRunID agentrun.Identity) review.ReviewTransport {
		transportes++
		transport := reviewexec.NewDurableTransport(st,
			store.RunPolicy{ID: "policy:test-gate-review", ParentRunID: string(rootRunID)},
			base.OpcionesRevision.SHA, nil,
			reviewexec.WithRunObserver(func(runID string) { aprendidos = append(aprendidos, runID) }))
		return func(bundleName, dimension, prompt string, agente review.AuditorAgente) (string, string, error) {
			restricted, ok := agente.(reviewexec.RestrictedReviewer)
			if !ok {
				return "", "", review.ErrRestrictedRequired
			}
			output, evidence, err := transport.Run(restricted, bundleName+"/"+dimension, prompt)
			if err != nil {
				return "", "", err
			}
			return output, evidence.InvocationID, nil
		}
	}

	resultado := EjecutarGate(durable)

	if resultado.Estado != EstadoCodeReviewFailed {
		t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoCodeReviewFailed)
	}
	if transportes < 1 {
		t.Fatal("review transport factory was never invoked, expected at least one routed audit")
	}
	if len(aprendidos) == 0 {
		t.Fatal("the observer sink learned no review child identity")
	}

	sumario := reconstruirDesdeTienda(t, st)

	// The salted review runs must be enumerable: the machine-parseable
	// settlement suffix and the persisted ParentRunID scan must describe the
	// SAME set of children (validation jobs plus every admitted review run).
	if !slices.Equal(sortedCopy(sumario.enumerated), sortedCopy(sumario.scanned)) {
		t.Fatalf("enumerated children %v != ParentRunID scan %v", sumario.enumerated, sumario.scanned)
	}
	wantChildren := 1 + len(aprendidos) // single-capability profile: one validation job
	if len(sumario.scanned) != wantChildren {
		t.Fatalf("scanned children = %d (%v), want %d (1 validation job + %d learned review runs)",
			len(sumario.scanned), sumario.scanned, wantChildren, len(aprendidos))
	}
	wantState, wantLayer := expectedTerminal(resultado.Estado)
	if sumario.rootState != wantState || sumario.layer != wantLayer {
		t.Fatalf("reconstructed terminal = {%v %s}, want {%v %s}", sumario.rootState, sumario.layer, wantState, wantLayer)
	}
	// Sensible classification of the settled tree: every child reached a
	// terminal attempt exactly once and none of them failed (the reviewer
	// answered; the BLOCK verdict belongs to the caller-side engine, so only
	// the ROOT carries the failure).
	for _, class := range []agentrun.OutcomeClass{agentrun.OutcomeFailure, agentrun.OutcomeUnavailable} {
		if sumario.classes[class] != 0 {
			t.Fatalf("child classes recorded %d %s outcomes, expected none on children", sumario.classes[class], class)
		}
	}
	if sumario.classes[agentrun.OutcomeSuccess] != wantChildren {
		t.Fatalf("child success count = %d, want %d", sumario.classes[agentrun.OutcomeSuccess], wantChildren)
	}
}
