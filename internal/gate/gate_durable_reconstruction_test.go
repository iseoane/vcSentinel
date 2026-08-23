// Reconstruction contract test for ticket 11: given ONLY the admitted store
// contents of one durable gate execution — the root run record, its children
// discovered through the persisted ParentRunID linkage, and the root's
// terminal settlement detail — the gate summary (terminal state, failing
// layer, child enumeration, per-validation-job evidence bindings, final
// class) must rebuild identically to what EjecutarGate returned live. This
// test is the contract; every value asserted here comes from store reads,
// never from in-process orchestration state.
package gate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// reconstructedSummary is everything the store alone can tell about one
// durable gate execution.
type reconstructedSummary struct {
	// rootState is the settled lifecycle state of the root run.
	rootState agentrun.LifecycleState
	// layer names the failing layer parsed from the root settlement detail
	// ("validation" | "review" | "infrastructure"); empty means green.
	layer string
	// enumerated are the child run IDs machine-parsed from the root
	// settlement detail's "|children=" suffix.
	enumerated []string
	// scanned are the child run IDs discovered purely through the persisted
	// ParentRunID linkage of their admission records.
	scanned []string
	// classes count the terminal AttemptOutcome classes across children.
	classes map[agentrun.OutcomeClass]int
	// outputHashes collects every child's persisted OutputHash.
	outputHashes []string
}

// reconstruirDesdeTienda rebuilds the summary from STORE CONTENTS ONLY.
func reconstruirDesdeTienda(t *testing.T, st *store.Store) reconstructedSummary {
	t.Helper()
	ids, err := st.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs() error = %v", err)
	}
	parents := make(map[string]string, len(ids))
	var rootID string
	for _, id := range ids {
		request, err := st.ReadExecutionRequest(id)
		if err != nil {
			t.Fatalf("ReadExecutionRequest(%s) error = %v", id, err)
		}
		parents[id] = request.ParentRunID
		if request.ParentRunID == "" {
			if rootID != "" {
				t.Fatalf("store holds multiple parentless runs (%s, %s)", rootID, id)
			}
			rootID = id
		}
	}
	if rootID == "" {
		t.Fatal("store holds no root run")
	}

	sumario := reconstructedSummary{classes: map[agentrun.OutcomeClass]int{}}
	for _, id := range ids {
		if parents[id] == "" {
			continue
		}
		if parents[id] != rootID {
			t.Fatalf("run %s links to foreign parent %s", id, parents[id])
		}
		sumario.scanned = append(sumario.scanned, id)
		inspeccion, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(id))
		if err != nil {
			t.Fatalf("child %s inspection failed: %v", id, err)
		}
		if len(inspeccion.Outcomes) != 1 {
			t.Fatalf("child %s recorded %d outcomes, expected exactly one terminal attempt", id, len(inspeccion.Outcomes))
		}
		outcome := inspeccion.Outcomes[0]
		sumario.classes[outcome.Class]++
		if outcome.OutputHash != "" {
			sumario.outputHashes = append(sumario.outputHashes, outcome.OutputHash)
		}
	}

	inspeccion, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(rootID))
	if err != nil {
		t.Fatalf("root inspection failed: %v", err)
	}
	sumario.rootState = inspeccion.Projection.State
	for _, outcome := range inspeccion.Outcomes {
		sumario.layer = capaDesdeDetalle(outcome.Error)
		if suffix, ok := sufijoHijos(outcome.Error); ok {
			sumario.enumerated = suffix
		}
	}
	return sumario
}

// capaDesdeDetalle parses the failing-layer marker out of a settlement
// detail; empty means no layer marker (a green root).
func capaDesdeDetalle(detalle string) string {
	for _, capa := range []string{"validation", "review", "infrastructure"} {
		if strings.Contains(detalle, "failing layer: "+capa) {
			return capa
		}
	}
	return ""
}

// sufijoHijos parses the machine-parseable "|children=<id,id,...>" suffix of
// a settlement detail.
func sufijoHijos(detalle string) ([]string, bool) {
	marker := "|children="
	index := strings.Index(detalle, marker)
	if index < 0 {
		return nil, false
	}
	lista := detalle[index+len(marker):]
	if lista == "" {
		return []string{}, true
	}
	return strings.Split(lista, ","), true
}

// expectedTerminal maps a live facade state onto the durable vocabulary so
// reconstruction can be compared against what EjecutarGate returned.
func expectedTerminal(estado string) (agentrun.LifecycleState, string) {
	switch estado {
	case EstadoPass:
		return agentrun.StateSucceeded, ""
	case EstadoValidationFailed:
		return agentrun.StateFailed, "validation"
	case EstadoCodeReviewFailed, EstadoNeedsUserReview:
		return agentrun.StateFailed, "review"
	default:
		return agentrun.StateUnavailable, "infrastructure"
	}
}

// sortedCopy returns a sorted copy of a string slice.
func sortedCopy(values []string) []string {
	copied := slices.Clone(values)
	slices.Sort(copied)
	return copied
}

// afirmarEvidenciaLigada proves the multiset of child OutputHashes equals the
// HashAdapterOutput digest of every validation evidence serialization.
func afirmarEvidenciaLigada(t *testing.T, evidencia []ValidationEvidence, got []string) {
	t.Helper()
	want := make([]string, 0, len(evidencia))
	for _, entry := range evidencia {
		want = append(want, execution.HashAdapterOutput(entry.String()))
	}
	slices.Sort(want)
	gotSorted := slices.Clone(got)
	slices.Sort(gotSorted)
	if !slices.Equal(want, gotSorted) {
		t.Fatalf("child OutputHashes %v do not bind the evidence serializations %v", gotSorted, want)
	}
}

func TestGateDurableReconstructionFromStore(t *testing.T) {
	t.Run("validation failure reconstructs layer outcome, children, and evidence", func(t *testing.T) {
		transportes := 0
		base := opcionesBase(cfgDosCapabilities(), ejecutorSeleccionado(map[string]validation.ValidationRun{
			"echo test": {Exit: 1, Salida: "salida real del comando fallido"},
		}), fabricaContadora(new(int), "", nil))
		var capturados []validation.ValidationRun
		base.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			runs, err := ejecutarPerfilSinCandidato(perfil, alcance, o)
			capturados = runs
			return runs, err
		}
		durableOpts := opcionesDurable(t, base, &transportes)

		resultado := EjecutarGate(durableOpts)

		if resultado.Estado != EstadoValidationFailed {
			t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoValidationFailed)
		}
		if transportes != 0 {
			t.Fatalf("review started after a validation failure: factory invoked %d times", transportes)
		}

		sumario := reconstruirDesdeTienda(t, durableOpts.DurableStore)

		wantState, wantLayer := expectedTerminal(resultado.Estado)
		if sumario.rootState != wantState || sumario.layer != wantLayer {
			t.Fatalf("reconstructed terminal = {%v %s}, want {%v %s} for live estado %q",
				sumario.rootState, sumario.layer, wantState, wantLayer, resultado.Estado)
		}
		// Set equality: enumeration order is profile/admission order while
		// the scan order is the store's lexicographic listing.
		if !slices.Equal(sortedCopy(sumario.enumerated), sortedCopy(sumario.scanned)) {
			t.Fatalf("enumerated children %v != ParentRunID scan %v", sumario.enumerated, sumario.scanned)
		}
		if len(sumario.scanned) != 2 {
			t.Fatalf("scanned children = %d, expected both validation jobs", len(sumario.scanned))
		}
		if sumario.classes[agentrun.OutcomeSuccess] != 1 || sumario.classes[agentrun.OutcomeFailure] != 1 {
			t.Fatalf("child classes = %v, expected one success and one failure", sumario.classes)
		}
		afirmarEvidenciaLigada(t, RecordValidationEvidence(capturados), sumario.outputHashes)
	})

	t.Run("green gate reconstructs success from the root record alone", func(t *testing.T) {
		transportes := 0
		base := opcionesBase(cfgConPerfil("lint", "echo ok"), ejecutorSeleccionado(nil),
			fabricaContadora(new(int), `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
		var capturados []validation.ValidationRun
		base.EjecutarValidacion = func(perfil string, alcance []string, o validation.OpcionesEjecucion) ([]validation.ValidationRun, error) {
			runs, err := ejecutarPerfilSinCandidato(perfil, alcance, o)
			capturados = runs
			return runs, err
		}
		durableOpts := opcionesDurable(t, base, &transportes)

		resultado := EjecutarGate(durableOpts)

		if resultado.Estado != EstadoPass {
			t.Fatalf("estado = %q, expected %q", resultado.Estado, EstadoPass)
		}
		if transportes != 1 {
			t.Fatalf("transport factory invoked %d times, expected exactly one review audit", transportes)
		}

		sumario := reconstruirDesdeTienda(t, durableOpts.DurableStore)

		wantState, wantLayer := expectedTerminal(resultado.Estado)
		if sumario.rootState != wantState || sumario.layer != wantLayer {
			t.Fatalf("reconstructed terminal = {%v %s}, want {%v %s} for live estado %q",
				sumario.rootState, sumario.layer, wantState, wantLayer, resultado.Estado)
		}
		// Green settlements carry no children suffix; the scan still finds
		// both validation jobs through their persisted parent linkage. The
		// review-side runs live in the factory's own backing store, so this
		// store holds exactly root plus validation children.
		if len(sumario.enumerated) != 0 {
			t.Fatalf("green root enumerated children %v, expected none", sumario.enumerated)
		}
		if len(sumario.scanned) != 1 {
			t.Fatalf("scanned children = %d, expected the single validation job", len(sumario.scanned))
		}
		if sumario.classes[agentrun.OutcomeSuccess] != 1 {
			t.Fatalf("child classes = %v, expected one success", sumario.classes)
		}
		afirmarEvidenciaLigada(t, RecordValidationEvidence(capturados), sumario.outputHashes)
	})
}
