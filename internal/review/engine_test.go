package review

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

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

func fabricaFija(respuestas []string) (FabricaAuditor, *agenteFake) {
	fake := &agenteFake{respuestas: respuestas}
	return func(dimension string) (AuditorAgente, string, error) {
		return fake, "normal", nil
	}, fake
}

func TestAuditarCommitTodoOk(t *testing.T) {
	fabrica, _ := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 2, OpcionesAuditoria{
		SHA: "abc12345", Mensaje: "msg", Diff: "diff", Dims: []string{DimLogic, DimSpec},
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

func TestAuditarCommitIncluyeContextoSinHacerloFatal(t *testing.T) {
	agente := &agentePrompt{}
	fabrica := func(string) (AuditorAgente, string, error) { return agente, "normal", nil }
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Dims: []string{DimLogic},
		RutasContexto: []string{"internal/review/engine.go"}, ProveedorContexto: proveedorContextoFake{}})
	if resultado.Veredicto != VerdictOK || !strings.Contains(agente.prompt, `"path":"internal/review/engine_test.go"`) || !strings.Contains(agente.prompt, "UNTRUSTED_ADVISORY_PATH_METADATA") {
		t.Fatalf("contexto no incluido: veredicto=%s prompt=%q", resultado.Veredicto, agente.prompt)
	}

	agente.prompt = ""
	resultado = AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc", Dims: []string{DimLogic},
		ProveedorContexto: proveedorContextoFake{err: errors.New("unreachable")}})
	if resultado.Veredicto != VerdictOK || strings.Contains(agente.prompt, "Reviewer context") {
		t.Fatalf("fallo de contexto afectó revisión: veredicto=%s prompt=%q", resultado.Veredicto, agente.prompt)
	}
}

func TestAuditarCommitBlockManda(t *testing.T) {
	fabrica, _ := fabricaFija([]string{
		`{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"bug"}]}`,
		`{"dim":"spec","verdict":"unavailable","reason":"rate_limit"}`,
	})
	resultado := AuditarCommit(fabrica, 2, OpcionesAuditoria{
		SHA: "abc12345", Dims: []string{DimLogic, DimSpec},
	})
	if resultado.Veredicto != VerdictBlock {
		t.Errorf("veredicto = %q, esperado block (manda sobre unavailable)", resultado.Veredicto)
	}
}

func TestAuditarCommitUnavailable(t *testing.T) {
	fabrica, _ := fabricaFija([]string{`{"dim":"logic","verdict":"unavailable","reason":"rate_limit"}`})
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc12345", Dims: []string{DimLogic}})
	if resultado.Veredicto != VerdictUnavailable {
		t.Errorf("veredicto = %q, esperado unavailable", resultado.Veredicto)
	}
}

func TestAuditarCommitPreguntaSinRespuestas(t *testing.T) {
	fabrica, _ := fabricaFija([]string{
		`{"dim":"logic","verdict":"question","questions":[{"id":"Q1","text":"¿abortar?"}]}`,
	})
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{SHA: "abc12345", Dims: []string{DimLogic}})
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
		SHA: "abc12345", Dims: []string{DimLogic}, Respuestas: "Q1: sí",
	})
	if resultado.Veredicto != VerdictOK {
		t.Errorf("veredicto = %q, esperado ok tras la ronda de respuestas", resultado.Veredicto)
	}
	if fake.llamadas != 2 {
		t.Errorf("llamadas = %d, esperado 2 (pregunta + segunda ronda)", fake.llamadas)
	}
}

func TestAuditarCommitErrorDeEjecucionEsUnavailable(t *testing.T) {
	fabrica := func(dimension string) (AuditorAgente, string, error) {
		return nil, "normal", nil
	}
	_ = fabrica
	// El caso real: el agente devuelve un error de ejecución (timeout).
	fabricaErr := func(dimension string) (AuditorAgente, string, error) {
		return agenteError{}, "normal", nil
	}
	resultado := AuditarCommit(fabricaErr, 1, OpcionesAuditoria{SHA: "abc12345", Dims: []string{DimLogic}})
	if resultado.Veredicto != VerdictUnavailable {
		t.Errorf("veredicto = %q, esperado unavailable por error de ejecución", resultado.Veredicto)
	}
}

type agenteError struct{}

func (agenteError) EjecutarPrompt(prompt string) (string, error) {
	return "", errTimeoutSimulado
}

var errTimeoutSimulado = &errorSimulado{}

type errorSimulado struct{}

func (e *errorSimulado) Error() string { return "simulated timeout" }

func TestAuditarCommitParaleloUno(t *testing.T) {
	fabrica, fake := fabricaFija(nil)
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: "abc12345", Dims: []string{DimLogic, DimStyle, DimDesign},
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
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			obtenido := BundlesForRisk(risk.Resultado{Nivel: caso.riesgo}, caso.caracteristicas)
			if !reflect.DeepEqual(obtenido, caso.esperado) {
				t.Errorf("BundlesForRisk(%s) = %#v, expected %#v", caso.riesgo, obtenido, caso.esperado)
			}
		})
	}
	if got := DimensionesParaArchivos([]string{"internal/a.go"}); !reflect.DeepEqual(got, []string{DimLogic, DimSpec, DimTests, DimDesign}) {
		t.Errorf("DimensionesParaArchivos fallback = %v", got)
	}
}
