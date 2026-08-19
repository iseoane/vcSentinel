package review

import (
	"crypto/sha256"
	"encoding/hex"
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
	Dimension           string `json:"dimension"`
	File                string `json:"file"`
	Line                Linea  `json:"line"`
	Severity            string `json:"severity"`
	Description         string `json:"description"`
	Suggestion          string `json:"suggestion"`
	Source              string `json:"source,omitempty"`
	Status              string `json:"status,omitempty"`
	RefutationReason    string `json:"refutation_reason,omitempty"`
	RefutationEvidence  string `json:"refutation_evidence,omitempty"`
	RefutationLineStart int    `json:"refutation_line_start,omitempty"`
	RefutationLineEnd   int    `json:"refutation_line_end,omitempty"`
	RefutationRangeHash string `json:"refutation_range_hash,omitempty"`
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
	ID                  string              `json:"id"`
	Source              string              `json:"source"`
	Producer            Productor           `json:"producer"`
	Dimension           string              `json:"dimension"`
	Severity            string              `json:"severity"`
	Confidence          float64             `json:"confidence"`
	Status              string              `json:"status"`
	Title               string              `json:"title"`
	Description         string              `json:"description"`
	Location            Ubicacion           `json:"location"`
	Evidence            string              `json:"evidence"`
	EvidenceSet         *FindingEvidenceSet `json:"evidence_set,omitempty"`
	Impact              string              `json:"impact,omitempty"`
	Recommendation      string              `json:"recommendation,omitempty"`
	Fixable             string              `json:"fixable"`
	IntroducedBy        string              `json:"introduced_by,omitempty"`
	Fingerprint         string              `json:"fingerprint"`
	RefutationReason    string              `json:"refutation_reason,omitempty"`
	RefutationEvidence  string              `json:"refutation_evidence,omitempty"`
	RefutationLineStart int                 `json:"refutation_line_start,omitempty"`
	RefutationLineEnd   int                 `json:"refutation_line_end,omitempty"`
	RefutationRangeHash string              `json:"refutation_range_hash,omitempty"`
}

// FindingEvidence records the source evidence preserved during aggregation.
type FindingEvidence struct {
	Dimension  string    `json:"dimension"`
	Producer   Productor `json:"producer"`
	Evidence   string    `json:"evidence"`
	Confidence float64   `json:"confidence"`
}

// FindingEvidenceSet holds the evidence retained by an aggregated finding.
// A pointer keeps Hallazgo comparable for existing consumers.
type FindingEvidenceSet struct {
	Values []FindingEvidence `json:"values"`
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

// Fingerprint calcula una huella estable de un Hallazgo (finding v2) para
// poder rastrear "el mismo defecto" entre dos revisiones aunque el archivo se
// haya reindentado o se haya renumerado por una edición en otra parte: no usa
// Location.LineaInicio/LineaFin (los números de línea son el dato menos
// estable del hallazgo, cambian con cualquier edición previa en el archivo) ni
// Location.Blob (identifica una versión exacta del archivo, no el defecto en
// sí, que puede sobrevivir varios commits sin tocarse).
//
// Componentes, en este orden:
//  1. Dimension: la categoría del hallazgo (logic, security, ...).
//  2. Location.Simbolo si está disponible (más preciso: sobrevive a que el
//     símbolo se mueva de línea o incluso de archivo); si el agente no lo
//     resolvió, cae en Location.Archivo para no colisionar hallazgos de
//     archivos distintos bajo una clave vacía.
//  3. Evidence normalizada con normalizarParaComparar (T2.2): la misma
//     normalización que ya tolera reindentado al validar evidencia contra el
//     contenido del archivo: aquí sirve exactamente para lo mismo, tolerar
//     reindentado sin duplicar la lógica de comparación.
//  4. Title como "regla": Description e Impact narran detalles concretos de
//     la instancia (qué línea, qué valor, qué consecuencia observada), que
//     varían aunque el TIPO de defecto sea el mismo; Title es la etiqueta que
//     el propio agente usa para nombrar el tipo de problema (p. ej.
//     "condición siempre verdadera") y se repite igual entre instancias del
//     mismo defecto, que es justo lo que necesita la propiedad de "regla".
func Fingerprint(h Hallazgo) string {
	simboloORuta := h.Location.Simbolo
	if simboloORuta == "" {
		simboloORuta = h.Location.Archivo
	}
	entrada := empaquetarConLongitud(
		h.Dimension,
		simboloORuta,
		normalizarParaComparar(h.Evidence),
		h.Title,
	)
	suma := sha256.Sum256([]byte(entrada))
	return hex.EncodeToString(suma[:])
}

// empaquetarConLongitud concatena componentes prefijando cada uno con su
// longitud decimal y ":". Un separador simple como "|" sería ambiguo si algún
// componente lo contuviera literalmente (p. ej. evidencia con un "|" dentro de
// una expresión lógica); prefijar con la longitud hace que la concatenación
// sea inambigua sin importar qué caracteres traiga cada componente.
func empaquetarConLongitud(componentes ...string) string {
	var b strings.Builder
	for _, c := range componentes {
		b.WriteString(strconv.Itoa(len(c)))
		b.WriteByte(':')
		b.WriteString(c)
	}
	return b.String()
}

// AgentQuestion es una aclaración que el agente necesita para poder auditar.
type AgentQuestion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// File is the path the question refers to, when the agent can attribute
	// it to one file.
	File string `json:"file,omitempty"`
}

// DimensionResult es el veredicto del agente para una dimensión concreta.
// Advertencias recoge normalizaciones aplicadas durante el parseo (p. ej.
// severidad desconocida rebajada a ADVISORY) y no se serializa en la ficha.
//
// Hallazgos (T2.4) es paralelo a Findings, no un sustituto: cada finding
// crudo del array "findings" que trae al menos un campo exclusivo de v2
// (Hallazgo) se decodifica ADEMÁS como Hallazgo completo y se añade aquí,
// sin dejar de aparecer también en Findings (v1). Un finding que hoy solo
// trae los campos v1 (el contrato real del agente hasta F5) deja Hallazgos
// vacío: la capacidad de parsear v2 no depende de que el prompt ya lo emita.
type DimensionResult struct {
	Bundle          string          `json:"bundle,omitempty"`
	Dim             string          `json:"dim"`
	Verdict         string          `json:"verdict"`
	Findings        []ReviewFinding `json:"findings,omitempty"`
	Hallazgos       []Hallazgo      `json:"hallazgos,omitempty"`
	Questions       []AgentQuestion `json:"questions,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	RefutedCritical bool            `json:"refuted_critical,omitempty"`
	Advertencias    []string        `json:"-"`
}

// findingCrudo decodifica un elemento del array "findings" de una línea
// JSONL (o del objeto multilínea) aceptando a la vez los campos v1
// (ReviewFinding, siempre presentes hoy) y los campos exclusivos de v2
// (Hallazgo, F5). Los campos v1 comparten clave JSON con su homólogo de
// Hallazgo donde existe (severity/description): no hay dos campos para lo
// mismo, solo dos estructuras de destino a partir del mismo dato decodificado
// una sola vez.
//
// Los campos exclusivos de v2 son punteros a propósito: nil distingue "el
// agente no mandó este campo" de "lo mandó con su valor cero" (p. ej.
// confidence: 0), que es justo la señal usada por esV2() para decidir si el
// finding trae forma v2 sin exigir que el agente mande todos los campos v2
// a la vez.
type findingCrudo struct {
	Dimension   string `json:"dimension"`
	File        string `json:"file"`
	Line        Linea  `json:"line"`
	Severity    string `json:"severity"`
	Description string `json:"description"`
	Suggestion  string `json:"suggestion"`

	ID             *string          `json:"id"`
	Source         *string          `json:"source"`
	Producer       *Productor       `json:"producer"`
	Confidence     *confidenceScore `json:"confidence"`
	Status         *string          `json:"status"`
	Title          *string          `json:"title"`
	Evidence       *string          `json:"evidence"`
	Location       *Ubicacion       `json:"location"`
	Impact         *string          `json:"impact"`
	Recommendation *string          `json:"recommendation"`
	Fixable        *string          `json:"fixable"`
	IntroducedBy   *string          `json:"introduced_by"`
}

// confidenceScore parses a finding's "confidence" field. The review prompt
// (T5.6) asks the model for a category ("high", "medium", "low") rather than
// a raw float, since an LLM has no reliable basis for a precise numeric
// estimate; this maps that category onto the float64 scale the rest of the
// v2 contract already uses (F2: Confidence 1.0 == full certainty). A bare
// number is still accepted as-is for callers that already emit one (e.g. the
// validation-sourced findings from F2, which use 1.0 directly).
type confidenceScore float64

const (
	confidenceHigh   confidenceScore = 0.9
	confidenceMedium confidenceScore = 0.6
	confidenceLow    confidenceScore = 0.3
)

func (c *confidenceScore) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "high":
			*c = confidenceHigh
		case "medium":
			*c = confidenceMedium
		case "low":
			*c = confidenceLow
		default:
			return fmt.Errorf("unknown confidence level %q", s)
		}
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*c = confidenceScore(n)
	return nil
}

// esV2 indica si el finding crudo trae al menos un campo exclusivo de v2.
func (f findingCrudo) esV2() bool {
	return f.ID != nil || f.Source != nil || f.Producer != nil || f.Confidence != nil ||
		f.Status != nil || f.Title != nil || f.Evidence != nil || f.Location != nil ||
		f.Impact != nil || f.Recommendation != nil || f.Fixable != nil || f.IntroducedBy != nil
}

// aReviewFinding proyecta los campos v1 del finding crudo, ignorando los
// exclusivos de v2: es el mismo ReviewFinding que se construía antes de T2.4.
func (f findingCrudo) aReviewFinding() ReviewFinding {
	return ReviewFinding{
		Dimension:   f.Dimension,
		File:        f.File,
		Line:        f.Line,
		Severity:    f.Severity,
		Description: f.Description,
		Suggestion:  f.Suggestion,
	}
}

// aHallazgo construye el Hallazgo (v2) completo del finding crudo.
// dimensionLinea es la dimensión declarada por la línea/objeto contenedor
// (crudo.Dim), no un campo del finding individual. Si el finding no trae
// Location explícita, se usa File/Line (v1) como ubicación de respaldo, para
// que Fingerprint no colisione hallazgos de archivos distintos bajo una
// ubicación vacía.
func (f findingCrudo) aHallazgo(dimensionLinea string) Hallazgo {
	ubicacion := Ubicacion{Archivo: f.File, LineaInicio: int(f.Line)}
	// Guard por VACÍO, no solo por nil: un "location": {} explícito en el
	// JSON produce un *Ubicacion no nil pero sin Archivo ni Simbolo, así que
	// mirar solo f.Location != nil dejaría pasar una ubicación vacía y
	// reintroduciría la colisión de Fingerprint entre archivos distintos que
	// el respaldo v1 (File/Line) existe justamente para evitar.
	if f.Location != nil && (f.Location.Archivo != "" || f.Location.Simbolo != "") {
		ubicacion = *f.Location
	}
	h := Hallazgo{
		Dimension:   dimensionLinea,
		Severity:    f.Severity,
		Description: f.Description,
		Location:    ubicacion,
	}
	if f.ID != nil {
		h.ID = *f.ID
	}
	if f.Source != nil {
		h.Source = *f.Source
	}
	if f.Producer != nil {
		h.Producer = *f.Producer
	}
	if f.Confidence != nil {
		h.Confidence = float64(*f.Confidence)
	}
	if f.Status != nil {
		h.Status = *f.Status
	}
	if f.Title != nil {
		h.Title = *f.Title
	}
	if f.Evidence != nil {
		h.Evidence = *f.Evidence
	}
	if f.Impact != nil {
		h.Impact = *f.Impact
	}
	if f.Recommendation != nil {
		h.Recommendation = *f.Recommendation
	}
	if f.Fixable != nil {
		h.Fixable = *f.Fixable
	}
	if f.IntroducedBy != nil {
		h.IntroducedBy = *f.IntroducedBy
	}
	h.Fingerprint = Fingerprint(h)
	return h
}

// procesarFindings convierte los findings crudos de una línea/objeto en sus
// formas v1 (ReviewFinding, compatibilidad) y v2 (Hallazgo, solo para los que
// traen algún campo exclusivo). Normaliza la severidad UNA vez por finding
// con el mismo criterio que ya existía para v1 (T2.4: no crear dos criterios
// de normalización distintos), y esa severidad normalizada es la que ven
// tanto el ReviewFinding como el Hallazgo resultantes.
func procesarFindings(crudos []findingCrudo, dimensionLinea string, normalizaciones *[]string) ([]ReviewFinding, []Hallazgo) {
	findingsV1 := make([]ReviewFinding, 0, len(crudos))
	var hallazgosV2 []Hallazgo
	for _, f := range crudos {
		if f.Severity != SevCritical && f.Severity != SevWarning && f.Severity != SevAdvisory {
			*normalizaciones = append(*normalizaciones,
				fmt.Sprintf("severidad %q en %s:%d normalizada a ADVISORY", f.Severity, f.File, f.Line))
			f.Severity = SevAdvisory
		}
		findingsV1 = append(findingsV1, f.aReviewFinding())
		if f.esV2() {
			hallazgosV2 = append(hallazgosV2, f.aHallazgo(dimensionLinea))
		}
	}
	return findingsV1, hallazgosV2
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
			Findings  []findingCrudo  `json:"findings"`
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

		findingsV1, hallazgosV2 := procesarFindings(crudo.Findings, crudo.Dim, &normalizaciones)

		if descartadas > 0 {
			normalizaciones = append(normalizaciones, fmt.Sprintf("%d líneas no JSONL descartadas", descartadas))
		}
		return &DimensionResult{
			Dim:          crudo.Dim,
			Verdict:      veredictoFinal(crudo.Verdict, findingsV1, &normalizaciones),
			Findings:     findingsV1,
			Hallazgos:    hallazgosV2,
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
		Findings  []findingCrudo  `json:"findings"`
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

	findingsV1, hallazgosV2 := procesarFindings(crudo.Findings, crudo.Dim, &normalizaciones)

	return &DimensionResult{
		Dim:          crudo.Dim,
		Verdict:      veredictoFinal(verdict, findingsV1, &normalizaciones),
		Findings:     findingsV1,
		Hallazgos:    hallazgosV2,
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
