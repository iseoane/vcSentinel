package review

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
)

// AuditorAgente es la interfaz que el motor usa para hablar con el agente.
// CLIAdapter la implementa; los tests inyectan dobles.
type AuditorAgente interface {
	EjecutarPrompt(prompt string) (string, error)
}

// FabricaAuditor construye el agente para una dimensión y devuelve además el
// nombre del perfil aplicado. Inyectable en los tests.
type FabricaAuditor func(dimension string) (AuditorAgente, string, error)

// OpcionesAuditoria define un trabajo de auditoría sobre un commit.
type OpcionesAuditoria struct {
	SHA               string
	Mensaje           string
	Diff              string
	Dims              []string
	Respuestas        string           // --answer: aclaraciones del usuario (1 ronda extra)
	PerfilOverride    string           // --profile: fuerza un perfil sobre el mapa
	OnDimension       func(dim string) // opcional: avisa cuando arranca cada dimensión
	ProveedorContexto ContextProvider
	RutasContexto     []string
}

// ResultadoDimension es el veredicto de una dimensión tras la auditoría.
type ResultadoDimension struct {
	Dim       string
	Perfil    string
	Resultado *DimensionResult
	Error     error
}

// ResultadoAuditoria agrega el veredicto global del commit.
type ResultadoAuditoria struct {
	SHA       string
	Dims      []ResultadoDimension
	Veredicto string // ok | warn | block | question | unavailable
	Preguntas []AgentQuestion
}

const (
	BundleCorrectness     = "correctness"
	BundleQuality         = "quality"
	BundleSecurity        = "security"
	BundleContracts       = "contracts_compatibility"
	BundleConcurrencyData = "concurrency_data"
	EffortLow             = "low"
)

// ReviewBundle is one planned semantic-review agent and its dimensions.
type ReviewBundle struct {
	Name       string
	Dimensions []string
	Effort     string
}

// BundlesForRisk selects the review agents from the shared risk profile.
// Style is intentionally absent: deterministic lint owns it.
func BundlesForRisk(resultado risk.Resultado, caracteristicas []change.Caracteristica) []ReviewBundle {
	bundles := []ReviewBundle{
		{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}},
	}
	switch resultado.Nivel {
	case risk.NivelNone:
		return nil
	case risk.NivelLow:
		bundles[0].Effort = EffortLow
		return bundles
	case risk.NivelStandard:
		return append(bundles, ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}})
	case risk.NivelElevated:
		return append(bundles,
			ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}},
			ReviewBundle{Name: BundleSecurity, Dimensions: []string{DimSecurity}})
	case risk.NivelHigh:
		bundles = append(bundles,
			ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}},
			ReviewBundle{Name: BundleSecurity, Dimensions: []string{DimSecurity}})
		if caracteristicaPresente(caracteristicas, "public_api") || caracteristicaPresente(caracteristicas, "cross_module") {
			bundles = append(bundles, ReviewBundle{Name: BundleContracts, Dimensions: []string{DimSpec}})
		}
		if caracteristicaPresente(caracteristicas, "concurrency") || caracteristicaPresente(caracteristicas, "database") {
			bundles = append(bundles, ReviewBundle{Name: BundleConcurrencyData, Dimensions: []string{DimLogic}})
		}
		return bundles
	default:
		return BundlesForRisk(risk.Resultado{Nivel: risk.NivelStandard}, caracteristicas)
	}
}

func caracteristicaPresente(caracteristicas []change.Caracteristica, nombre string) bool {
	for _, caracteristica := range caracteristicas {
		if caracteristica.Nombre == nombre && caracteristica.Estado == change.CaracteristicaPresente {
			return true
		}
	}
	return false
}

// DimensionesParaArchivos preserves callers that have not yet supplied a risk
// profile. Their former layer policy is replaced by the standard risk bundle.
func DimensionesParaArchivos(_ []string) []string {
	var dims []string
	vistas := map[string]bool{}
	for _, bundle := range BundlesForRisk(risk.Resultado{Nivel: risk.NivelStandard}, nil) {
		for _, dim := range bundle.Dimensions {
			if !vistas[dim] {
				dims = append(dims, dim)
				vistas[dim] = true
			}
		}
	}
	return dims
}

// AuditarCommit audita un commit contra sus dimensiones con un semáforo de
// parallel trabajos concurrentes. No toca el ledger: el llamador persiste la
// revisión. La degradación es tipada: error de ejecución o de parseo se
// convierte en veredicto unavailable con razón, nunca en fallo del motor.
func AuditarCommit(fabrica FabricaAuditor, parallel int, opts OpcionesAuditoria) ResultadoAuditoria {
	resultado := ResultadoAuditoria{SHA: opts.SHA}
	contexto := contextoRevisor(opts.ProveedorContexto, opts.SHA, opts.RutasContexto)
	if parallel < 1 {
		parallel = 1
	}

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	mutex := &sync.Mutex{}

	for _, dim := range opts.Dims {
		wg.Add(1)
		go func(dimension string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if opts.OnDimension != nil {
				opts.OnDimension(dimension)
			}

			rd := ResultadoDimension{Dim: dimension}
			agente, perfil, err := fabrica(dimension)
			rd.Perfil = perfil
			if err != nil {
				rd.Error = err
				rd.Resultado = &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}
			} else {
				rd.Resultado, rd.Error = auditarConAgente(agente, dimension, opts, contexto)
			}

			mutex.Lock()
			resultado.Dims = append(resultado.Dims, rd)
			mutex.Unlock()
		}(dim)
	}
	wg.Wait()

	resultado.Veredicto, resultado.Preguntas = veredictoGlobal(resultado.Dims)
	return resultado
}

// auditarConAgente ejecuta el prompt (con la ronda extra de --answer si el
// agente pide aclaraciones) y parsea el JSONL del agente.
func auditarConAgente(agente AuditorAgente, dimension string, opts OpcionesAuditoria, contexto string) (*DimensionResult, error) {
	salida, err := agente.EjecutarPrompt(construirPromptConContexto(dimension, opts.Mensaje, opts.Diff, "", contexto))
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: "provider_unavailable"}, err
	}

	crudo, err := ParsearDimensionResult(salida)
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
	}

	// Segunda ronda solo si el agente pidió aclaraciones y el usuario respondió.
	if crudo.Verdict == VerdictQuestion && opts.Respuestas != "" {
		salida, err = agente.EjecutarPrompt(construirPromptConContexto(dimension, opts.Mensaje, opts.Diff, opts.Respuestas, contexto))
		if err != nil {
			return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: "provider_unavailable"}, err
		}
		crudo, err = ParsearDimensionResult(salida)
		if err != nil {
			return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
		}
	}
	return crudo, nil
}

type Relation string
type Reason string

const (
	RelationAffectedTest Relation = "affected_test"
	ReasonCodeGraph      Reason   = "codegraph_dependency"
)

type Reference struct {
	Path     string   `json:"path"`
	Relation Relation `json:"relation"`
	Reason   Reason   `json:"reason"`
}

type ContextProvider interface {
	Nombre() string
	Contexto(sha string, rutas []string) ([]Reference, error)
}

func contextoRevisor(proveedor ContextProvider, sha string, rutas []string) string {
	if proveedor == nil {
		return ""
	}
	referencias, err := proveedor.Contexto(sha, rutas)
	if err != nil {
		return ""
	}
	datos, err := json.Marshal(referencias)
	if err != nil || len(referencias) == 0 {
		return ""
	}
	return string(datos)
}

// veredictoGlobal decide el veredicto del commit: block manda, luego question
// (sin resolver), luego unavailable, luego warn, y por último ok.
func veredictoGlobal(dims []ResultadoDimension) (string, []AgentQuestion) {
	hayWarn := false
	var preguntas []AgentQuestion
	for _, rd := range dims {
		if rd.Resultado == nil {
			continue
		}
		switch rd.Resultado.Verdict {
		case VerdictBlock:
			return VerdictBlock, preguntas
		case VerdictQuestion:
			preguntas = append(preguntas, rd.Resultado.Questions...)
		case VerdictWarn:
			hayWarn = true
		}
	}
	if len(preguntas) > 0 {
		return VerdictQuestion, preguntas
	}
	for _, rd := range dims {
		if rd.Resultado != nil && rd.Resultado.Verdict == VerdictUnavailable {
			return VerdictUnavailable, nil
		}
	}
	if hayWarn {
		return VerdictWarn, nil
	}
	return VerdictOK, nil
}

// String resume la auditoría para la salida en consola.
func (r ResultadoAuditoria) String() string {
	lineas := make([]string, 0, len(r.Dims)+2)
	lineas = append(lineas, fmt.Sprintf("🔎 Revisión de %s: %s", r.SHA[:8], r.Veredicto))
	for _, rd := range r.Dims {
		veredicto := "?"
		if rd.Resultado != nil {
			veredicto = rd.Resultado.Verdict
		}
		lineas = append(lineas, fmt.Sprintf("  %-9s %-11s %s", rd.Dim, veredicto, rd.Perfil))
	}
	return "\n" + strings.Join(lineas, "\n")
}
