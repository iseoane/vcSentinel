package review

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
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
	SHA            string
	Mensaje        string
	Diff           string
	Dims           []string
	Respuestas     string           // --answer: aclaraciones del usuario (1 ronda extra)
	PerfilOverride string           // --profile: fuerza un perfil sobre el mapa
	OnDimension    func(dim string) // opcional: avisa cuando arranca cada dimensión
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

// dimensionesPorCapa es la matriz saco × dimensión: qué dimensiones se auditan
// según la capa de los archivos que toca el commit (spec siempre se añade).
var dimensionesPorCapa = map[string][]string{
	"config":   {DimSecurity, DimDesign},
	"backend":  {DimLogic, DimDesign, DimSecurity},
	"frontend": {DimStyle, DimLogic},
	"test":     {DimTests},
}

// ordenCanonicoDimensiones fija el orden de salida de las dimensiones.
var ordenCanonicoDimensiones = []string{DimLogic, DimStyle, DimDesign, DimTests, DimSecurity, DimSpec}

// DimensionesParaArchivos deduce las dimensiones de auditoría del conjunto de
// archivos de un commit: unión de las de su capa más spec (transversal).
func DimensionesParaArchivos(archivos []string) []string {
	unidas := map[string]bool{}
	for _, archivo := range archivos {
		capa := git.ClasificarCapa(archivo)
		for _, dim := range dimensionesPorCapa[capa] {
			unidas[dim] = true
		}
	}
	unidas[DimSpec] = true

	var dims []string
	for _, dim := range ordenCanonicoDimensiones {
		if unidas[dim] {
			dims = append(dims, dim)
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
				rd.Resultado, rd.Error = auditarConAgente(agente, dimension, opts)
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
func auditarConAgente(agente AuditorAgente, dimension string, opts OpcionesAuditoria) (*DimensionResult, error) {
	salida, err := agente.EjecutarPrompt(ConstruirPromptAuditoria(dimension, opts.Mensaje, opts.Diff, ""))
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: "provider_unavailable"}, err
	}

	crudo, err := ParsearDimensionResult(salida)
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
	}

	// Segunda ronda solo si el agente pidió aclaraciones y el usuario respondió.
	if crudo.Verdict == VerdictQuestion && opts.Respuestas != "" {
		salida, err = agente.EjecutarPrompt(ConstruirPromptAuditoria(dimension, opts.Mensaje, opts.Diff, opts.Respuestas))
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
