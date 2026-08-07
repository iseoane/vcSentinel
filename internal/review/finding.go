package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Dimensiones canónicas de auditoría. Son el contrato Go + prompt del agente:
// el vocabulario en inglés es fijo y validado de forma estricta.
const (
	DimLogic    = "logic"
	DimStyle    = "style"
	DimDesign   = "design"
	DimTests    = "tests"
	DimSecurity = "security"
	DimSpec     = "spec"
)

// DimensionesValidas contiene las seis dimensiones canónicas.
var DimensionesValidas = map[string]bool{
	DimLogic:    true,
	DimStyle:    true,
	DimDesign:   true,
	DimTests:    true,
	DimSecurity: true,
	DimSpec:     true,
}

// Severidades de un hallazgo.
const (
	SevCritical = "CRITICAL"
	SevWarning  = "WARNING"
	SevAdvisory = "ADVISORY"
)

// Veredictos de una dimensión.
const (
	VerdictOK          = "ok"
	VerdictWarn        = "warn"
	VerdictBlock       = "block"
	VerdictQuestion    = "question"
	VerdictUnavailable = "unavailable"
)

var veredictosValidos = map[string]bool{
	VerdictOK:          true,
	VerdictWarn:        true,
	VerdictBlock:       true,
	VerdictQuestion:    true,
	VerdictUnavailable: true,
}

// Linea es el número de línea de un hallazgo. Los agentes a veces emiten
// "line" como número y a veces como string ("126"); UnmarshalJSON acepta
// ambos para que un string numérico no descarte el hallazgo completo.
type Linea int

// UnmarshalJSON acepta tanto un número JSON como un string numérico. Un
// string no numérico (p. ej. "L126-130") produce error para que la línea
// JSONL se descarte como inválida.
func (l *Linea) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("línea no numérica %q", s)
		}
		*l = Linea(n)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*l = Linea(n)
	return nil
}

// ReviewFinding es un hallazgo concreto del agente sobre una línea de un
// archivo del commit auditado.
type ReviewFinding struct {
	Dimension   string `json:"dimension"`
	File        string `json:"file"`
	Line        Linea  `json:"line"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`
}

// AgentQuestion es una aclaración que el agente necesita para poder auditar.
type AgentQuestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// DimensionResult es el veredicto del agente para una dimensión concreta.
// Advertencias recoge normalizaciones aplicadas durante el parseo (p. ej.
// severidad desconocida rebajada a ADVISORY) y no se serializa en la ficha.
type DimensionResult struct {
	Dim          string          `json:"dim"`
	Verdict      string          `json:"verdict"`
	Findings     []ReviewFinding `json:"findings,omitempty"`
	Questions    []AgentQuestion `json:"questions,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	Advertencias []string        `json:"-"`
}

// Errores tipados del parseo, para que el llamador decida la degradación
// (unavailable/block) sin adivinar.
var (
	ErrSalidaVacia       = errors.New("el agente devolvió una salida vacía")
	ErrJSONLInvalido     = errors.New("ninguna línea JSONL válida con dimensión")
	ErrDimensionInvalida = errors.New("dimensión desconocida")
	ErrVeredictoInvalido = errors.New("veredicto desconocido")
)

// ParsearDimensionResult extrae el resultado de una dimensión de la salida
// cruda del agente. Tolera texto alrededor y fences de markdown: localiza el
// bloque delimitado por BEGIN_REVIEW / END_REVIEW si existe y, si no, usa toda
// la salida. Las líneas que no son JSONL válido se descartan (se cuentan para
// Advertencias); la primera línea con "dim" y "verdict" conocidos gana.
// La dimensión desconocida es un error explícito, nunca un silencio.
func ParsearDimensionResult(salida string) (*DimensionResult, error) {
	bloque := extraerBloqueJSONL(salida)
	lineas := strings.Split(bloque, "\n")

	var descartadas int
	var normalizaciones []string
	for _, linea := range lineas {
		linea = strings.TrimSpace(linea)
		if linea == "" {
			continue
		}

		var crudo struct {
			Dim       string          `json:"dim"`
			Verdict   string          `json:"verdict"`
			Findings  []ReviewFinding `json:"findings"`
			Questions []AgentQuestion `json:"questions"`
			Reason    string          `json:"reason"`
		}
		if err := json.Unmarshal([]byte(linea), &crudo); err != nil {
			descartadas++
			continue
		}
		if crudo.Dim == "" {
			descartadas++
			continue
		}
		if !DimensionesValidas[crudo.Dim] {
			return nil, fmt.Errorf("%w: %q", ErrDimensionInvalida, crudo.Dim)
		}
		if !veredictosValidos[crudo.Verdict] {
			if len(crudo.Findings) == 0 {
				return nil, fmt.Errorf("%w: %q (dimensión %q)", ErrVeredictoInvalido, crudo.Verdict, crudo.Dim)
			}
			// Veredictos de facto ("issues", "error", ...) con hallazgos se
			// derivan de las severidades en lugar de abortar la auditoría.
			normalizaciones = append(normalizaciones,
				fmt.Sprintf("veredicto %q normalizado según severidades de hallazgos", crudo.Verdict))
			crudo.Verdict = ""
		}

		hallazgos := make([]ReviewFinding, 0, len(crudo.Findings))
		for _, h := range crudo.Findings {
			if h.Severity != SevCritical && h.Severity != SevWarning && h.Severity != SevAdvisory {
				normalizaciones = append(normalizaciones,
					fmt.Sprintf("severidad %q en %s:%d normalizada a ADVISORY", h.Severity, h.File, h.Line))
				h.Severity = SevAdvisory
			}
			hallazgos = append(hallazgos, h)
		}

		if descartadas > 0 {
			normalizaciones = append(normalizaciones, fmt.Sprintf("%d líneas no JSONL descartadas", descartadas))
		}
		return &DimensionResult{
			Dim:          crudo.Dim,
			Verdict:      veredictoFinal(crudo.Verdict, hallazgos, &normalizaciones),
			Findings:     hallazgos,
			Questions:    crudo.Questions,
			Reason:       crudo.Reason,
			Advertencias: normalizaciones,
		}, nil
	}

	if strings.TrimSpace(bloque) == "" {
		return nil, ErrSalidaVacia
	}
	return nil, ErrJSONLInvalido
}

// veredictoFinal decide el veredicto de una dimensión tras el parseo: los
// hallazgos mandan sobre el veredicto declarado. Un ok declarado con CRITICAL
// sube a block; un veredicto de facto (derivado de severidades) se resuelve
// aquí. question y unavailable se respetan tal cual.
func veredictoFinal(declarado string, hallazgos []ReviewFinding, normalizaciones *[]string) string {
	if declarado == VerdictQuestion || declarado == VerdictUnavailable {
		return declarado
	}

	derivado := veredictoDeSeveridades(hallazgos)
	if declarado == "" {
		return derivado
	}
	if derivado != declarado && derivado != VerdictOK {
		*normalizaciones = append(*normalizaciones,
			fmt.Sprintf("veredicto %q elevado a %q por severidades de hallazgos", declarado, derivado))
		return derivado
	}
	return declarado
}

// veredictoDeSeveridades mapea hallazgos a veredicto: CRITICAL -> block,
// WARNING/ADVISORY -> warn, sin hallazgos -> ok.
func veredictoDeSeveridades(hallazgos []ReviewFinding) string {
	peor := VerdictOK
	for _, h := range hallazgos {
		switch h.Severity {
		case SevCritical:
			return VerdictBlock
		case SevWarning, SevAdvisory:
			peor = VerdictWarn
		}
	}
	return peor
}

// extraerBloqueJSONL recorta la salida al segmento entre BEGIN_REVIEW y
// END_REVIEW cuando existen; si no, devuelve la salida completa.
func extraerBloqueJSONL(salida string) string {
	inicio := strings.Index(salida, "BEGIN_REVIEW")
	if inicio < 0 {
		return salida
	}
	inicio += len("BEGIN_REVIEW")
	fin := strings.Index(salida[inicio:], "END_REVIEW")
	if fin < 0 {
		return salida[inicio:]
	}
	return salida[inicio : inicio+fin]
}
