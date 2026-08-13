package review

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
)

// agenteFake devuelve salidas fijas y cuenta las llamadas.
type agenteFake struct {
	respuestas []string
	llamadas   int
	mu         sync.Mutex
}

func (a *agenteFake) EjecutarPrompt(prompt string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	salida := "{\"dim\":\"logic\",\"verdict\":\"ok\"}"
	if a.llamadas < len(a.respuestas) {
		salida = a.respuestas[a.llamadas]
	}
	a.llamadas++
	return salida, nil
}

func (a *agenteFake) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func fabricaFija(respuestas []string) (FabricaAuditor, *agenteFake) {
	fake := &agenteFake{respuestas: respuestas}
	return func(_ ReviewBundle, dimension string) (AuditorAgente, string, error) {
		return fake, "normal", nil
	}, fake
}

func bundlesPrueba(dims ...string) []ReviewBundle {
	return []ReviewBundle{{Name: "test", Dimensions: dims, Priority: PriorityRequired, Cost: 1}}
}

func TestAuditarCommitTodoOk(t *testing.T) {
	fabrica, _ := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 2, OpcionesAuditoria{
		SHA: "abc12345", Mensaje: "msg", Diff: "diff", Bundles: bundlesPrueba(DimLogic, DimSpec),
	})
	if resultado.Veredicto != VerdictOK {
		t.Errorf("veredicto = %q, esperado ok", resultado.Veredicto)
	}
	if len(resultado.Dims) != 2 {
		t.Errorf("dims auditadas = %d, esperado 2", len(resultado.Dims))
	}
}

type proveedorContextoFake struct{ err error }

func (proveedorContextoFake) Nombre() string { return "codegraph" }
func (p proveedorContextoFake) Contexto(string, []string) ([]Reference, error) {
	return []Reference{{Path: "internal/review/engine_test.go", Relation: RelationAffectedTest, Reason: ReasonCodeGraph}}, p.err
}

type agentePrompt struct{ prompt string }

func (a *agentePrompt) EjecutarPrompt(prompt string) (string, error) {
	a.prompt = prompt
	return `{"dim":"logic","verdict":"ok"}`, nil
}

func (a *agentePrompt) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func TestAuditarCommitIncluyeContextoSinHacerloFatal(t *testing.T) {
	agente := &agentePrompt{}
	fabrica := func(_ ReviewBundle, _ string) (AuditorAgente, string, error) { return agente, "normal", nil }
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: bundlesPrueba(DimLogic),
		RutasContexto: []string{"internal/review/engine.go"}, ProveedorContexto: proveedorContextoFake{}})
	if resultado.Veredicto != VerdictOK || !strings.Contains(agente.prompt, `"path":"internal/review/engine_test.go"`) || !strings.Contains(agente.prompt, "UNTRUSTED_ADVISORY_PATH_METADATA") {
		t.Fatalf("contexto no incluido: veredicto=%s prompt=%q", resultado.Veredicto, agente.prompt)
	}

	agente.prompt = ""
	resultado = AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: bundlesPrueba(DimLogic),
		ProveedorContexto: proveedorContextoFake{err: errors.New("unreachable")}})
	if resultado.Veredicto != VerdictOK || strings.Contains(agente.prompt, "Reviewer context") {
		t.Fatalf("fallo de contexto afectó revisión: veredicto=%s prompt=%q", resultado.Veredicto, agente.prompt)
	}
}

func TestAuditarCommitDisplaysOnlyValidatedPaths(t *testing.T) {
	agente := &agentePrompt{}
	fabrica := func(_ ReviewBundle, _ string) (AuditorAgente, string, error) { return agente, "normal", nil }
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: "abc", Bundles: bundlesPrueba(DimLogic), RutasContexto: []string{"safe.go", "bad\ninjection", "*.go", "../outside.go"},
	})
	if resultado.Veredicto != VerdictOK || !strings.Contains(agente.prompt, "Permitted paths:\n- safe.go") {
		t.Fatalf("prompt did not retain validated path: %q", agente.prompt)
	}
	for _, rejected := range []string{"bad\ninjection", "*.go", "../outside.go"} {
		if strings.Contains(agente.prompt, rejected) {
			t.Fatalf("prompt contains rejected path %q: %q", rejected, agente.prompt)
		}
	}
}

func TestAuditarCommitBlockManda(t *testing.T) {
	fabrica, _ := fabricaFija([]string{
		`{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"bug"}]}`,
		`{"dim":"spec","verdict":"unavailable","reason":"rate_limit"}`,
	})
	resultado := AuditarCommit(fabrica, 2, OpcionesAuditoria{
		SHA: "abc12345", Bundles: bundlesPrueba(DimLogic, DimSpec),
	})
	if resultado.Veredicto != VerdictBlock {
		t.Errorf("veredicto = %q, esperado block (manda sobre unavailable)", resultado.Veredicto)
	}
}

func TestAuditarCommitUnavailable(t *testing.T) {
	fabrica, _ := fabricaFija([]string{`{"dim":"logic","verdict":"unavailable","reason":"rate_limit"}`})
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc12345", Bundles: bundlesPrueba(DimLogic)})
	if resultado.Veredicto != VerdictUnavailable {
		t.Errorf("veredicto = %q, esperado unavailable", resultado.Veredicto)
	}
}

func TestAuditarCommitPreguntaSinRespuestas(t *testing.T) {
	fabrica, _ := fabricaFija([]string{
		`{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿abortar?"}]}`,
	})
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc12345", Bundles: bundlesPrueba(DimLogic)})
	if resultado.Veredicto != VerdictQuestion {
		t.Errorf("veredicto = %q, esperado question", resultado.Veredicto)
	}
	if len(resultado.Preguntas) != 1 || resultado.Preguntas[0].ID != "Q1" {
		t.Errorf("preguntas = %+v, esperado Q1", resultado.Preguntas)
	}
}

func TestAuditarCommitPreguntaResueltaConRespuestas(t *testing.T) {
	fabrica, fake := fabricaFija([]string{
		`{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿abortar?"}]}`,
		`{"dim":"logic","verdict":"ok"}`,
	})
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: "abc12345", Bundles: bundlesPrueba(DimLogic), Respuestas: "Q1: sí",
	})
	if resultado.Veredicto != VerdictOK {
		t.Errorf("veredicto = %q, esperado ok tras la ronda de respuestas", resultado.Veredicto)
	}
	if fake.llamadas != 2 {
		t.Errorf("llamadas = %d, esperado 2 (pregunta + segunda ronda)", fake.llamadas)
	}
}

func TestAuditarCommitErrorDeEjecucionEsUnavailable(t *testing.T) {
	fabrica := func(_ ReviewBundle, dimension string) (AuditorAgente, string, error) {
		return nil, "normal", nil
	}
	_ = fabrica
	// El caso real: el agente devuelve un error de ejecución (timeout).
	fabricaErr := func(_ ReviewBundle, dimension string) (AuditorAgente, string, error) {
		return agenteError{}, "normal", nil
	}
	resultado := AuditarCommit(fabricaErr, 1, OpcionesAuditoria{SHA: "abc12345", Bundles: bundlesPrueba(DimLogic)})
	if resultado.Veredicto != VerdictUnavailable {
		t.Errorf("veredicto = %q, esperado unavailable por error de ejecución", resultado.Veredicto)
	}
}

type agenteError struct{}

func (agenteError) EjecutarPrompt(prompt string) (string, error) {
	return "", errTimeoutSimulado
}

func (agenteError) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return agenteError{}.EjecutarPrompt(prompt)
}

var errTimeoutSimulado = &errorSimulado{}

type errorSimulado struct{}

func (e *errorSimulado) Error() string { return "simulated timeout" }

func TestAuditarCommitParaleloUno(t *testing.T) {
	fabrica, fake := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: "abc12345", Bundles: bundlesPrueba(DimLogic, DimStyle, DimDesign),
	})
	if len(resultado.Dims) != 3 {
		t.Errorf("dims = %d, esperado 3 con semáforo 1", len(resultado.Dims))
	}
	if fake.llamadas != 3 {
		t.Errorf("llamadas = %d, esperado 3", fake.llamadas)
	}
}

func TestBundlesForRisk(t *testing.T) {
	casos := []struct {
		nombre          string
		riesgo          risk.Nivel
		caracteristicas []change.Caracteristica
		esperado        []ReviewBundle
	}{
		{"none", risk.NivelNone, nil, nil},
		{"low", risk.NivelLow, nil, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}, Effort: EffortLow}}},
		{"standard", risk.NivelStandard, nil, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}}, {Name: BundleQuality, Dimensions: []string{DimDesign}}}},
		{"elevated", risk.NivelElevated, nil, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}}, {Name: BundleQuality, Dimensions: []string{DimDesign}}, {Name: BundleSecurity, Dimensions: []string{DimSecurity}}}},
		{"high with characteristics", risk.NivelHigh, []change.Caracteristica{{Nombre: "public_api", Estado: change.CaracteristicaPresente}, {Nombre: "concurrency", Estado: change.CaracteristicaPresente}}, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}}, {Name: BundleQuality, Dimensions: []string{DimDesign}}, {Name: BundleSecurity, Dimensions: []string{DimSecurity}}, {Name: BundleContracts, Dimensions: []string{DimSpec}}, {Name: BundleConcurrencyData, Dimensions: []string{DimLogic}}}},
		{"unknown is conservative high", risk.Nivel("unknown"), nil, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}}, {Name: BundleQuality, Dimensions: []string{DimDesign}}, {Name: BundleSecurity, Dimensions: []string{DimSecurity}}}},
		{"high cross module", risk.NivelHigh, []change.Caracteristica{{Nombre: "cross_module", Estado: change.CaracteristicaPresente}}, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}}, {Name: BundleQuality, Dimensions: []string{DimDesign}}, {Name: BundleSecurity, Dimensions: []string{DimSecurity}}, {Name: BundleContracts, Dimensions: []string{DimSpec}}}},
		{"high database", risk.NivelHigh, []change.Caracteristica{{Nombre: "database", Estado: change.CaracteristicaPresente}}, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}}, {Name: BundleQuality, Dimensions: []string{DimDesign}}, {Name: BundleSecurity, Dimensions: []string{DimSecurity}}, {Name: BundleConcurrencyData, Dimensions: []string{DimLogic}}}},
		{"high absent characteristics add no bundles", risk.NivelHigh, []change.Caracteristica{{Nombre: "public_api", Estado: change.CaracteristicaAusente}, {Nombre: "cross_module", Estado: change.CaracteristicaAusente}, {Nombre: "concurrency", Estado: change.CaracteristicaAusente}, {Nombre: "database", Estado: change.CaracteristicaAusente}}, []ReviewBundle{{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}}, {Name: BundleQuality, Dimensions: []string{DimDesign}}, {Name: BundleSecurity, Dimensions: []string{DimSecurity}}}},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			obtenido := BundlesForRisk(risk.Resultado{Nivel: caso.riesgo}, caso.caracteristicas)
			if !reflect.DeepEqual(bundlesWithoutBudget(obtenido), caso.esperado) {
				t.Errorf("BundlesForRisk(%s) = %#v, expected %#v", caso.riesgo, obtenido, caso.esperado)
			}
		})
	}
}

func bundlesWithoutBudget(bundles []ReviewBundle) []ReviewBundle {
	resultado := append([]ReviewBundle(nil), bundles...)
	for i := range resultado {
		resultado[i].Priority, resultado[i].Cost = 0, 0
	}
	return resultado
}

func TestPlanForProfileUsesCompleteRiskSignals(t *testing.T) {
	profile := change.ChangeProfile{
		Kind:    "dependency",
		Symbols: change.ChangeSymbols{Complete: true},
	}
	plan := PlanForProfile(profile, []string{"internal/backend/auth.go", "vassentinel.yml"})
	if plan.Risk.Nivel != risk.NivelHigh || !hasBundle(plan.Bundles, BundleSecurity) {
		t.Fatalf("plan = %+v, expected high risk with security coverage", plan)
	}
}

func TestAuditarCommitBudgetExhaustionIsDeclared(t *testing.T) {
	fabrica, fake := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 2, OpcionesAuditoria{
		SHA: "abc", Budget: ReviewBudget{MaxCost: 1},
		Bundles: []ReviewBundle{
			{Name: BundleCorrectness, Dimensions: []string{DimLogic}, Priority: PriorityRequired, Cost: 1},
			{Name: BundleSecurity, Dimensions: []string{DimSecurity}, Priority: PriorityOptional, Cost: 1},
		},
	})
	if fake.llamadas != 1 || len(resultado.Skipped) != 1 || resultado.Skipped[0].Name != BundleSecurity || resultado.Skipped[0].Reason != "budget_exhausted" {
		t.Fatalf("calls=%d skipped=%+v", fake.llamadas, resultado.Skipped)
	}
}

func TestAuditarCommitRetriesTransportErrorOnce(t *testing.T) {
	calls := 0
	fabrica := func(_ ReviewBundle, _ string) (AuditorAgente, string, error) {
		return auditorFunc(func(string) (string, error) {
			calls++
			if calls == 1 {
				return "", context.DeadlineExceeded
			}
			return `{"dim":"logic","verdict":"ok"}`, nil
		}), "normal", nil
	}
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: bundlesPrueba(DimLogic)})
	if calls != 2 || resultado.Veredicto != VerdictOK {
		t.Fatalf("calls=%d verdict=%s", calls, resultado.Veredicto)
	}
}

func TestAuditarCommitDurationBudgetIsDeterministic(t *testing.T) {
	times := []time.Time{time.Unix(0, 0), time.Unix(0, int64(time.Second))}
	clock := func() time.Time {
		now := times[0]
		times = times[1:]
		return now
	}
	fabrica, fake := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: "abc", Budget: ReviewBudget{MaxDuration: time.Second, Clock: clock},
		Bundles: []ReviewBundle{
			{Name: BundleCorrectness, Dimensions: []string{DimLogic}, Priority: PriorityRequired, Cost: 1},
			{Name: BundleSecurity, Dimensions: []string{DimSecurity}, Priority: PriorityOptional, Cost: 1},
		},
	})
	if fake.llamadas != 1 || len(resultado.Skipped) != 1 || resultado.Skipped[0].Name != BundleSecurity {
		t.Fatalf("calls=%d skipped=%+v", fake.llamadas, resultado.Skipped)
	}
}

func TestAuditarCommitExecutesOptionalBundlesWithOverlappingDimensions(t *testing.T) {
	fabrica, fake := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: []ReviewBundle{
		{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec}, Priority: PriorityRequired, Cost: 1},
		{Name: BundleContracts, Dimensions: []string{DimSpec}, Priority: PriorityOptional, Cost: 1},
		{Name: BundleConcurrencyData, Dimensions: []string{DimLogic}, Priority: PriorityOptional, Cost: 1},
	}})
	if fake.llamadas != 4 || len(resultado.Dims) != 4 {
		t.Fatalf("calls=%d dims=%+v", fake.llamadas, resultado.Dims)
	}
}

func TestAuditarCommitPreservesOptionalBundlePurpose(t *testing.T) {
	var bundles []string
	var prompts []string
	fabrica := func(bundle ReviewBundle, dimension string) (AuditorAgente, string, error) {
		bundles = append(bundles, bundle.Name)
		return auditorFunc(func(prompt string) (string, error) {
			prompts = append(prompts, prompt)
			return `{"dim":"logic","verdict":"ok"}`, nil
		}), "normal", nil
	}
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: []ReviewBundle{
		{Name: BundleCorrectness, Dimensions: []string{DimLogic}, Priority: PriorityRequired, Cost: 1},
		{Name: BundleContracts, Dimensions: []string{DimSpec}, Priority: PriorityOptional, Cost: 1},
		{Name: BundleConcurrencyData, Dimensions: []string{DimLogic}, Priority: PriorityOptional, Cost: 1},
	}})
	if len(resultado.Dims) != 3 || !hasStrings(bundles, BundleCorrectness, BundleContracts, BundleConcurrencyData) {
		t.Fatalf("bundles=%v dims=%+v", bundles, resultado.Dims)
	}
	if !hasPrompt(prompts, "Contract compatibility") || !hasPrompt(prompts, "Concurrency and data integrity") {
		t.Fatalf("optional prompts did not preserve purpose: %q", prompts)
	}
	if !hasResultBundle(resultado.Dims, BundleContracts) || !hasResultBundle(resultado.Dims, BundleConcurrencyData) {
		t.Fatalf("bundle identities = %+v", resultado.Dims)
	}
	for _, dimension := range resultado.Dims {
		if dimension.Resultado.Bundle != dimension.Bundle {
			t.Fatalf("result bundle=%q, expected %q", dimension.Resultado.Bundle, dimension.Bundle)
		}
	}
}

func TestAuditarCommitDuplicateOptionalBundleDoesNotConsumeBudget(t *testing.T) {
	fabrica, fake := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Budget: ReviewBudget{MaxCost: 2}, Bundles: []ReviewBundle{
		{Name: BundleCorrectness, Dimensions: []string{DimLogic}, Priority: PriorityRequired, Cost: 1},
		{Name: BundleCorrectness, Dimensions: []string{DimLogic}, Priority: PriorityOptional, Cost: 1},
		{Name: BundleContracts, Dimensions: []string{DimSpec}, Priority: PriorityOptional, Cost: 1},
	}})
	if fake.llamadas != 2 || len(resultado.Dims) != 2 {
		t.Fatalf("calls=%d dims=%+v", fake.llamadas, resultado.Dims)
	}
	if len(resultado.Skipped) != 1 || resultado.Skipped[0] != (SkippedBundle{Name: BundleCorrectness, Reason: "duplicate_bundle"}) {
		t.Fatalf("skipped=%+v", resultado.Skipped)
	}
}

func hasBundle(bundles []ReviewBundle, name string) bool {
	for _, bundle := range bundles {
		if bundle.Name == name {
			return true
		}
	}
	return false
}

func hasStrings(values []string, wanted ...string) bool {
	for _, want := range wanted {
		found := false
		for _, value := range values {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func hasPrompt(prompts []string, purpose string) bool {
	for _, prompt := range prompts {
		if strings.Contains(prompt, purpose) {
			return true
		}
	}
	return false
}

func hasResultBundle(results []ResultadoDimension, bundle string) bool {
	for _, result := range results {
		if result.Bundle == bundle {
			return true
		}
	}
	return false
}

func TestAuditarCommitInvalidOutputIsNotRetried(t *testing.T) {
	fabrica, fake := fabricaFija([]string{"not json", `{"dim":"logic","verdict":"ok"}`})
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: bundlesPrueba(DimLogic)})
	if fake.llamadas != 1 || resultado.Veredicto != VerdictUnavailable {
		t.Fatalf("calls=%d verdict=%s", fake.llamadas, resultado.Veredicto)
	}
}

func TestAuditarCommitUnavailableDoesNotHideBlock(t *testing.T) {
	llamadas := 0
	fabrica := func(_ ReviewBundle, dim string) (AuditorAgente, string, error) {
		return auditorFunc(func(string) (string, error) {
			llamadas++
			if dim == DimSecurity {
				return "", context.DeadlineExceeded
			}
			return `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"bug"}]}`, nil
		}), "normal", nil
	}
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: []ReviewBundle{{Name: "both", Dimensions: []string{DimLogic, DimSecurity}, Priority: PriorityRequired, Cost: 1}}})
	if llamadas != 3 || resultado.Veredicto != VerdictBlock {
		t.Fatalf("calls=%d verdict=%s", llamadas, resultado.Veredicto)
	}
}

type auditorFunc func(string) (string, error)

func (f auditorFunc) EjecutarPrompt(prompt string) (string, error) { return f(prompt) }

func (f auditorFunc) EjecutarRevision(prompt, _ string, _ []string) (string, error) { return f(prompt) }

func TestAuditarCommitRejectsUnrestrictedPromptAdapter(t *testing.T) {
	called := false
	fabrica := func(_ ReviewBundle, _ string) (AuditorAgente, string, error) {
		return promptOnlyAuditor{called: &called}, "normal", nil
	}
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Bundles: bundlesPrueba(DimLogic)})
	if resultado.Veredicto != VerdictUnavailable || called {
		t.Fatalf("verdict=%q prompt-called=%t", resultado.Veredicto, called)
	}
}

type promptOnlyAuditor struct{ called *bool }

func (a promptOnlyAuditor) EjecutarPrompt(string) (string, error) {
	*a.called = true
	return `{"dim":"logic","verdict":"ok"}`, nil
}
