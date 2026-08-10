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

// Fuentes posibles de un Hallazgo (finding v2): de qué produjo el hallazgo.
const (
	SourceValidation = "validation" // comando determinista (internal/validation)
	SourceReview     = "review"     // inferencia semántica de un agente LLM
)

// Estados del ciclo de vida de un Hallazgo (finding v2). pending es el
// estado inicial; confirmed/refuted los fija la refutación mecánica o el
// agente; accepted_by_user y reopened los fija un humano; fixed lo fija la
// verificación posterior al parche.
const (
	StatusPending        = "pending"
	StatusConfirmed      = "confirmed"
	StatusRefuted        = "refuted"
	StatusAcceptedByUser = "accepted_by_user"
	StatusFixed          = "fixed"
	StatusReopened       = "reopened"
)

// Niveles de confianza para aplicar automáticamente la corrección sugerida
// de un Hallazgo. safe: aplicable sin revisión; needs_review: aplicable pero
// un humano debe confirmar; manual: no hay corrección mecánica posible.
const (
	FixableSafe        = "safe"
	FixableNeedsReview = "needs_review"
	FixableManual      = "manual"
)

// Productor identifica quién o qué generó un Hallazgo: un agente LLM (con su
// modelo y esfuerzo) o un comando determinista (binario). ModeloVerificado
// distingue un modelo cuyo nombre fue confirmado por el propio agente
// (p. ej. vía --version o metadata de la API) de uno asumido por config.
type Productor struct {
	Agente           string `json:"agent"`
	Binario          string `json:"binary,omitempty"`
	Modelo           string `json:"model,omitempty"`
	Esfuerzo         string `json:"reasoning_effort,omitempty"`
	ModeloVerificado bool   `json:"model_verified"`
}

// Ubicacion sitúa un Hallazgo en el código. Blob es el hash del contenido del
// archivo en el momento del hallazgo: permite detectar si el archivo cambió
// desde entonces sin depender de que la línea siga significando lo mismo.
type Ubicacion struct {
	Archivo     string `json:"file"`
	Blob        string `json:"blob,omitempty"`
	LineaInicio int    `json:"line_start"`
	LineaFin    int    `json:"line_end,omitempty"`
	Simbolo     string `json:"symbol,omitempty"`
}

// Hallazgo es el finding v2: convive con ReviewFinding (v1) sin sustituirlo.
// Añade procedencia (Source/Producer), certeza (Confidence), ciclo de vida
// (Status) y evidencia literal para que un falso positivo se pueda refutar
// mecánicamente sin releer el código a mano.
//
// internal/validation.Hallazgo (F1) tiene forma similar (Source/Severity/
// Evidencia) pero nació en otro paquete para otro propósito: el resultado de
// un comando determinista (lint/test/build), no de un agente LLM. Esta
// estructura NO importa ni depende de internal/validation a propósito: son
// conceptos análogos, no el mismo tipo, y unificarlos (si llega a hacer
// falta) es tarea de una fase futura (F6, agregador, según el README de
// reingeniería). Un Hallazgo con Source=SourceValidation se rellenaría con
// Confidence: 1.0 (certeza total: es un exit code real, no una inferencia
// semántica) y Producer describiendo el comando ejecutado (Binario/Agente)
// en vez de un modelo LLM (Modelo/Esfuerzo/ModeloVerificado quedarían vacíos).
type Hallazgo struct {
	ID             string    `json:"id"`
	Source         string    `json:"source"`
	Producer       Productor `json:"producer"`
	Dimension      string    `json:"dimension"`
	Severity       string    `json:"severity"`
	Confidence     float64   `json:"confidence"`
	Status         string    `json:"status"`
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	Location       Ubicacion `json:"location"`
	Evidence       string    `json:"evidence"`
	Impact         string    `json:"impact,omitempty"`
	Recommendation string    `json:"recommendation,omitempty"`
	Fixable        string    `json:"fixable"`
	IntroducedBy   string    `json:"introduced_by,omitempty"`
	Fingerprint    string    `json:"fingerprint"`
}

// Motivos de descarte de un Hallazgo durante la validación de evidencia.
const (
	MotivoSinEvidencia          = "sin evidencia: Evidence vacío"
	MotivoArchivoNoResuelto     = "no se pudo resolver el contenido del archivo citado"
	MotivoEvidenciaNoEncontrada = "la evidencia no aparece literalmente en el contenido del archivo"
)

// Descarte registra por qué se descartó un Hallazgo al validar su evidencia.
// Descartar un hallazgo nunca invalida la ejecución completa (T2.2): el
// motivo viaja junto al hallazgo para que quede trazabilidad de qué se perdió
// y por qué, sin abortar la validación de los demás.
type Descarte struct {
	Hallazgo Hallazgo
	Motivo   string
}

// HallazgosConEvidenciaValida filtra los Hallazgo (finding v2) cuya Evidence
// no se puede comprobar mecánicamente contra el contenido real del archivo
// que citan: es el filtro más barato contra alucinaciones de un LLM, sin
// necesidad de que otro modelo lo juzgue.
//
// leerContenido es una costura de inyección deliberada: internal/review NO
// importa internal/git para no acoplar el modelo de dominio de la auditoría
// a la implementación concreta de lectura de git. Quien llama pasa un cierre
// (p. ej. sobre git.ContenidoDeArchivoEnCommit) atado al commit que se está
// auditando.
//
// Un hallazgo se descarta si Evidence está vacío, si Location.Archivo está
// vacío o su contenido no se puede resolver, o si Evidence (normalizado) no
// aparece literalmente en el contenido del archivo (normalizado). Descartar
// un hallazgo nunca detiene la validación de los demás ni propaga el error
// de leerContenido hacia arriba: de N hallazgos, si uno es inválido, los
// otros N-1 sobreviven intactos.
func HallazgosConEvidenciaValida(hallazgos []Hallazgo, leerContenido func(archivo string) (string, error)) ([]Hallazgo, []Descarte) {
	validos := make([]Hallazgo, 0, len(hallazgos))
	var descartes []Descarte
	for _, h := range hallazgos {
		if motivo := motivoDescarteEvidencia(h, leerContenido); motivo != "" {
			descartes = append(descartes, Descarte{Hallazgo: h, Motivo: motivo})
			continue
		}
		validos = append(validos, h)
	}
	return validos, descartes
}

// motivoDescarteEvidencia devuelve el motivo por el que un Hallazgo se
// descartaría, o "" si su evidencia es válida.
func motivoDescarteEvidencia(h Hallazgo, leerContenido func(archivo string) (string, error)) string {
	if strings.TrimSpace(h.Evidence) == "" {
		return MotivoSinEvidencia
	}
	if strings.TrimSpace(h.Location.Archivo) == "" {
		return MotivoArchivoNoResuelto
	}
	contenido, err := leerContenido(h.Location.Archivo)
	if err != nil {
		return MotivoArchivoNoResuelto
	}
	if !strings.Contains(normalizarParaComparar(contenido), normalizarParaComparar(h.Evidence)) {
		return MotivoEvidenciaNoEncontrada
	}
	return ""
}

// normalizarParaComparar recorta espacios al principio/final de cada línea y
// unifica fin de línea (\r\n -> \n), solo para comparar evidencia: tolera que
// un LLM reindente al citar código sin tolerar un parecido vago (no toca nada
// más: ni comentarios ni indentación intermedia).
func normalizarParaComparar(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lineas := strings.Split(s, "\n")
	for i, linea := range lineas {
		lineas[i] = strings.TrimSpace(linea)
	}
	return strings.Join(lineas, "\n")
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
	// Fallback: algunos modelos (p. ej. el perfil cheap con reasoning low)
	// emiten el objeto JSON formateado en varias líneas (pretty-printed)
	// dentro del bloque. Ninguna línea individual es JSONL válido, pero el
	// bloque completo sí es un objeto JSON; parsearlo entero evita que una
	// auditoría válida se degrade a unavailable.
	if res, ok := parsearObjetoMultilinea(bloque); ok {
		return res, nil
	}
	return nil, ErrJSONLInvalido
}

// parsearObjetoMultilinea intenta interpretar el bloque como un único objeto
// JSON (tolerando formato pretty-printed con saltos de línea). Devuelve ok
// solo si el parseo completo tiene éxito y la dimensión es canónica.
func parsearObjetoMultilinea(bloque string) (*DimensionResult, bool) {
	var crudo struct {
		Dim       string          `json:"dim"`
		Verdict   string          `json:"verdict"`
		Findings  []ReviewFinding `json:"findings"`
		Questions []AgentQuestion `json:"questions"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(bloque)), &crudo); err != nil {
		return nil, false
	}
	if crudo.Dim == "" || !DimensionesValidas[crudo.Dim] {
		return nil, false
	}

	var normalizaciones []string
	verdict := crudo.Verdict
	if !veredictosValidos[verdict] {
		if len(crudo.Findings) == 0 {
			return nil, false
		}
		normalizaciones = append(normalizaciones,
			fmt.Sprintf("veredicto %q normalizado según severidades de hallazgos", verdict))
		verdict = ""
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

	return &DimensionResult{
		Dim:          crudo.Dim,
		Verdict:      veredictoFinal(verdict, hallazgos, &normalizaciones),
		Findings:     hallazgos,
		Questions:    crudo.Questions,
		Reason:       crudo.Reason,
		Advertencias: normalizaciones,
	}, true
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
