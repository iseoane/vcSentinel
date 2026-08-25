package review

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ordenColumnas devuelve el orden canónico de las columnas de la matriz
// commit × dimensión. Es una función (no un slice a nivel de paquete) para
// que ningún llamador pueda mutar el orden global de la matriz.
func ordenColumnas() []string {
	return []string{DimLogic, DimStyle, DimDesign, DimTests, DimSecurity, DimSpec}
}

// veredictoEmoji mapea un veredicto de dimensión (o un resultado de revisión)
// a su icono de tabla. La última revisión de cada ficha manda.
func veredictoEmoji(verdicto string) string {
	switch verdicto {
	case VerdictOK:
		return "✅"
	case VerdictWarn:
		return "⚠️"
	case VerdictBlock:
		return "🚨"
	case VerdictQuestion:
		return "❓"
	case VerdictUnavailable:
		return "⛔"
	}
	return "❔"
}

// ultimaRevision devuelve la última revisión de la ficha y su índice (0 si no
// hay revisiones). El veredicto mostrado siempre es el de la última.
func ultimaRevision(ficha Ficha) (Revision, bool) {
	if len(ficha.Revisions) == 0 {
		return Revision{}, false
	}
	return ficha.Revisions[len(ficha.Revisions)-1], true
}

// revisionSuperoBlock indica si la última revisión salió ok cuando alguna
// anterior estaba en block (re-auditoría tras un fix).
func revisionSuperoBlock(ficha Ficha) bool {
	ultima, ok := ultimaRevision(ficha)
	if !ok || ultima.Result != VerdictOK || len(ficha.Revisions) < 2 {
		return false
	}
	for _, rev := range ficha.Revisions[:len(ficha.Revisions)-1] {
		if rev.Result == VerdictBlock {
			return true
		}
	}
	return false
}

// celdaMatriz construye la celda de una dimensión para la última revisión:
// emoji del veredicto y, si la revisión superó un block previo, la marca de
// la guía "spec ✅ (2ª rev — CRITICAL superado)".
func celdaMatriz(ficha Ficha, dim string) string {
	ultima, ok := ultimaRevision(ficha)
	if !ok {
		return "—"
	}
	for _, dr := range ultima.Dims {
		if dr.Dim != dim {
			continue
		}
		if dr.Verdict == VerdictOK && revisionSuperoBlock(ficha) {
			return fmt.Sprintf("✅ (%dª rev — CRITICAL superado)", len(ficha.Revisions))
		}
		return veredictoEmoji(dr.Verdict)
	}
	return "—"
}

// mensajeCorto recorta el mensaje de commit para que la fila de la matriz no
// desborde la tabla.
func mensajeCorto(mensaje string) string {
	const maximo = 48
	runas := []rune(mensaje)
	if len(runas) <= maximo {
		return mensaje
	}
	return string(runas[:maximo]) + "..."
}

// RenderMatriz construye la tabla markdown commit × dimensión de una rama:
// una fila por ficha (SHA corto + mensaje) y las seis dimensiones canónicas
// como columnas fijas. Cada celda muestra el veredicto de la última revisión
// de la dimensión; "—" indica que esa dimensión no fue auditada.
func RenderMatriz(fichas []Ficha) string {
	if len(fichas) == 0 {
		return "_No hay commits auditados._"
	}

	columnas := ordenColumnas()
	var b strings.Builder
	b.WriteString("| Commit | " + strings.Join(columnas, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(columnas)+1) + "\n")
	for _, ficha := range fichas {
		sha := ficha.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		celdas := make([]string, 0, len(columnas))
		for _, dim := range columnas {
			celdas = append(celdas, celdaMatriz(ficha, dim))
		}
		b.WriteString(fmt.Sprintf("| `%s` %s | %s |\n", sha, mensajeCorto(ficha.Message), strings.Join(celdas, " | ")))
	}
	return b.String()
}

// conteoResultados cuenta las fichas por resultado global de su última
// revisión y devuelve la línea de resumen, incluyendo question/unavailable
// solo si existen.
func conteoResultados(fichas []Ficha) string {
	cuentas := map[string]int{}
	for _, ficha := range fichas {
		ultima, ok := ultimaRevision(ficha)
		if !ok {
			continue
		}
		cuentas[ultima.Result]++
	}
	linea := fmt.Sprintf("🟢 ok: %d · 🟡 warn: %d · 🚨 block: %d",
		cuentas[VerdictOK], cuentas[VerdictWarn], cuentas[VerdictBlock])
	if cuentas[VerdictQuestion] > 0 {
		linea += fmt.Sprintf(" · ❓ question: %d", cuentas[VerdictQuestion])
	}
	if cuentas[VerdictUnavailable] > 0 {
		linea += fmt.Sprintf(" · ⛔ unavailable: %d", cuentas[VerdictUnavailable])
	}
	return linea
}

// severidadEmoji mapea una severidad de hallazgo a su icono de riesgo.
func severidadEmoji(severidad string) string {
	switch severidad {
	case SevCritical:
		return "🚨"
	case SevWarning:
		return "⚠️"
	case SevAdvisory:
		return "🔵"
	}
	return "❔"
}

// ComandoVerificado es un comando de verificación ejecutado con su exit code
// real (determinista) — EVIDENCIA, nunca un PASS inventado.
type ComandoVerificado struct {
	Comando string
	Exit    int
}

// VerificacionPlantilla es la sección de verificación del PR: solo EVIDENCIA
// real, nunca un PASS inventado (regla de oro de la guía §12.3).
type VerificacionPlantilla struct {
	Modo     string              // determinista | delegado | omitido | configurar
	Comandos []ComandoVerificado // exit codes reales por comando
	Tested   []string            // contrato tested del agente (delegación)
	Motivo   string              // por qué no se ejecutó (omisión/configuración)

	// Validacion son los exit codes reales de internal/validation (T1.8),
	// ejecutados ANTES de la revisión semántica de la rama. Es una naturaleza
	// de evidencia distinta de Comandos (que traduce ops.Verificar, la
	// verificación post-hoc de tests/build): misma FORMA (comando + exit code
	// real, por eso se reusa ComandoVerificado sin duplicar el tipo), pero
	// distinto ORIGEN — de ahí el campo propio y su propia sección en la
	// plantilla, nunca mezclada con Comandos.
	Validacion []ComandoVerificado
}

// estaPendiente decide si una ficha aún aporta al veredicto de la rama. Una
// ficha corregida (FixedIn) ya no cuenta: su block fue resuelto en un commit
// posterior fuera de ella. Es la única definición de "pendiente" del paquete.
func estaPendiente(ficha Ficha) bool {
	return ficha.FixedIn == ""
}

// riesgos reúne los hallazgos CRITICAL y WARNING de la última revisión de
// cada ficha pendiente (los ADVISORY son información, no riesgos). Las fichas
// corregidas (FixedIn) no aportan riesgos pendientes.
//
// T6.5: lee ultima.HallazgosEfectivos() (ledger.go) — el único punto de
// selección que también consume BloqueantesDeRama más abajo — en vez de
// bifurcar entre AggregatedFindings y Dims aquí mismo. Eso evita el bug de
// diseño de T6.5 (un hallazgo semántico ya superseded por T6.2 solo
// desaparecía de riesgos(), nunca de BloqueantesDeRama) y el continue
// incondicional que podía tomar control de la ficha sin haber comprobado
// severidad primero.
func riesgos(fichas []Ficha) []string {
	var lineas []string
	for _, ficha := range fichas {
		if !estaPendiente(ficha) {
			continue
		}
		ultima, ok := ultimaRevision(ficha)
		if !ok {
			continue
		}
		sha := ficha.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		for _, h := range ultima.HallazgosEfectivos() {
			if h.Severity != SevCritical && h.Severity != SevWarning {
				continue
			}
			lineas = append(lineas, renderMergedFinding(sha, h))
		}
	}
	return lineas
}

// mergedFindingSourceLabel distinguishes a merged Hallazgo's origin for
// rendering (T6.5): SourceReview is a semantic LLM inference, SourceValidation
// a deterministic command. Any other value (or none) is labeled "unknown"
// rather than guessed, since a Hallazgo can only be trusted to say what it
// actually declares.
func mergedFindingSourceLabel(source string) string {
	switch source {
	case SourceReview:
		return "review"
	case SourceValidation:
		return "validation"
	default:
		return "unknown"
	}
}

// evidenceEmbedMaxBytes bounds how much of a finding's raw evidence text is
// embedded in the PR body Markdown (T6.5 review finding: security WARNING).
// Evidence/FindingEvidence.Evidence can be the incriminating code fragment
// itself — for a security finding, possibly an embedded secret — and the PR
// body is an external, indexable, cached surface. recortarRunas (already
// used by TruncarCuerpo for the whole body) bounds it the same way, never
// splitting a UTF-8 rune.
const evidenceEmbedMaxBytes = 300

// sanitizeEvidence prepares a finding's raw evidence text before it is
// embedded in a Markdown list item published to the PR body (T6.5 review
// findings: security WARNING x2). Evidence/FindingEvidence.Evidence is
// untrusted output (an LLM inference or a command's literal stdout/stderr),
// so it is: bounded in size with recortarRunas; collapsed to a single line
// so an embedded newline cannot break or forge the surrounding Markdown list
// structure; and wrapped in inline code, replacing any literal backtick it
// already contains so it can never terminate the code span early.
func sanitizeEvidence(evidencia string) string {
	acotada := recortarRunas(evidencia, evidenceEmbedMaxBytes)
	return "`" + sanitizeText(acotada) + "`"
}

// sanitizeText neutralizes free-form untrusted text before it is
// interpolated into the same Markdown list item sanitizeEvidence already
// guards. Hallazgo.Description and Location.Archivo share Evidence's
// untrusted origin — both are decoded straight from the LLM's findingCrudo
// JSON output (finding.go), not from a value the guardian resolves itself —
// so an embedded newline or backtick in either could otherwise break or
// forge the surrounding Markdown list structure (T6.5bis review finding:
// security WARNING). Unlike sanitizeEvidence, it does not wrap the result
// in inline code or bound its size: a description/path is prose, not an
// evidence code fragment, and wrapping it in a code span would change its
// visual meaning.
func sanitizeText(texto string) string {
	sinSaltos := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(texto)
	return strings.ReplaceAll(sinSaltos, "`", "'")
}

// mergedFindingEvidenceLines renders every corroborating evidence T6.1's
// aggregation retained in EvidenceSet, one line per source. A Hallazgo that
// never went through aggregation (EvidenceSet nil, or non-nil but with no
// Values — e.g. deserialized from {"evidence_set":{"values":[]}}) falls back
// to its single legacy Evidence string, so a merged-but-unique finding still
// shows its one piece of evidence instead of nothing.
func mergedFindingEvidenceLines(h Hallazgo) []string {
	if h.EvidenceSet == nil || len(h.EvidenceSet.Values) == 0 {
		if strings.TrimSpace(h.Evidence) == "" {
			return nil
		}
		return []string{fmt.Sprintf("  - evidence [%s]: %s (confidence %.2f)", h.Dimension, sanitizeEvidence(h.Evidence), h.Confidence)}
	}
	lineas := make([]string, 0, len(h.EvidenceSet.Values))
	for _, v := range h.EvidenceSet.Values {
		lineas = append(lineas, fmt.Sprintf("  - evidence [%s]: %s (confidence %.2f)", v.Dimension, sanitizeEvidence(v.Evidence), v.Confidence))
	}
	return lineas
}

// renderMergedFinding renders one aggregated Hallazgo — the T6.1 merge +
// T6.2 supersede result carried in ResultadoAuditoria.Findings — showing its
// distinguished Source and every accumulated evidence plus the combined
// confidence, instead of the single Evidence string a raw per-dimension
// ReviewFinding line shows. The location suffix is omitted entirely when no
// location was resolved, instead of rendering the empty placeholder "(:0)"
// (T6.5 review finding: logic ADVISORY). The "(source, confidence)" segment
// is likewise omitted entirely when h.Source == "": that only happens for a
// legacy v1 ReviewFinding converted by hallazgoDesdeReviewFinding, which
// never had a real Source to report — stamparProductorEfectivo/
// stamparSourceReview always stamp one on a real Hallazgo — so showing
// "(unknown, confidence 0.00)" would fabricate a datum that does not exist
// instead of reporting its absence (T6.5bis review finding: logic WARNING).
// Description and Location.Archivo are sanitized with sanitizeText before
// interpolation: they share Evidence's untrusted LLM origin, so an embedded
// newline or backtick in either must not be able to forge a Markdown list
// line the same way an unsanitized Evidence could (T6.5bis review finding:
// security WARNING).
func renderMergedFinding(sha string, h Hallazgo) string {
	ubicacion := ""
	if h.Location.Archivo != "" {
		ubicacion = fmt.Sprintf(" (%s:%d)", sanitizeText(h.Location.Archivo), h.Location.LineaInicio)
	}
	origen := ""
	if h.Source != "" {
		origen = fmt.Sprintf(" (%s, confidence %.2f)", mergedFindingSourceLabel(h.Source), h.Confidence)
	}
	linea := fmt.Sprintf("- %s `%s` [%s] %s%s — %s%s",
		severidadEmoji(h.Severity), sha, h.Dimension, h.Severity,
		origen, sanitizeText(h.Description), ubicacion)
	for _, evidencia := range mergedFindingEvidenceLines(h) {
		linea += "\n" + evidencia
	}
	return linea
}

// RenderResumen construye el resumen de veredictos y riesgos de la rama:
// conteo global, una fila por commit (resultado, modelo, revisiones y
// corrección) y los riesgos CRITICAL/WARNING pendientes.
func RenderResumen(fichas []Ficha) string {
	if len(fichas) == 0 {
		return "_No hay commits auditados._"
	}

	var b strings.Builder
	b.WriteString("### Resumen\n")
	b.WriteString(conteoResultados(fichas) + "\n\n")
	b.WriteString("| Commit | Resultado | Modelo | Revs | Corregida |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, ficha := range fichas {
		ultima, ok := ultimaRevision(ficha)
		if !ok {
			continue
		}
		sha := ficha.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		corregida := "—"
		if ficha.FixedIn != "" {
			fi := ficha.FixedIn
			if len(fi) > 7 {
				fi = fi[:7]
			}
			corregida = fmt.Sprintf("🔧 corregida en `%s`", fi)
		}
		b.WriteString(fmt.Sprintf("| `%s` | %s %s | %s | %d | %s |\n",
			sha, veredictoEmoji(ultima.Result), ultima.Result, ficha.Model, len(ficha.Revisions), corregida))
	}

	b.WriteString("\n### Riesgos\n")
	pendientes := riesgos(fichas)
	if len(pendientes) == 0 {
		b.WriteString("- Ninguno\n")
	} else {
		for _, linea := range pendientes {
			b.WriteString(linea + "\n")
		}
	}
	return b.String()
}

// MarcadorTruncamiento es el texto que señala un cuerpo recortado.
var MarcadorTruncamiento = "\n\n> ⚠️ Cuerpo truncado: se omitieron %d bytes.\n"

// LimiteCuerpoPR es el límite de tamaño del cuerpo de un PR (GitHub limita el
// body). El render de la plantilla nunca supera este tamaño.
const LimiteCuerpoPR = 65536

// recortarRunas recorta texto a maxBytes sin partir una runa UTF-8. Un límite
// no positivo devuelve el texto vacío. El corte es válido si el primer byte
// descartado inicia una runa; si es un byte de continuación, la runa quedó
// partida y se retrocede un byte.
func recortarRunas(texto string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(texto) <= maxBytes {
		return texto
	}
	cortado := texto[:maxBytes]
	for len(cortado) > 0 && !utf8.RuneStart(texto[len(cortado)]) {
		cortado = cortado[:len(cortado)-1]
	}
	return cortado
}

// TruncarCuerpo recorta un texto al límite de bytes del body (GitHub limita
// el cuerpo de un PR) marcando el truncamiento de forma explícita y con el
// número exacto de bytes omitidos. El tamaño del resultado nunca supera
// maxBytes y el corte nunca parte una runa UTF-8.
func TruncarCuerpo(texto string, maxBytes int) string {
	if maxBytes <= 0 || len(texto) <= maxBytes {
		return texto
	}

	// Presupuesto para el contenido: el marcador sin el número ocupa su
	// tamaño fijo; los dígitos del conteo se ajustan después.
	base := strings.Replace(MarcadorTruncamiento, "%d", "", 1)
	presupuesto := maxBytes - len(base)
	if presupuesto <= 0 {
		return recortarRunas(fmt.Sprintf(MarcadorTruncamiento, 0), maxBytes)
	}

	conservado := recortarRunas(texto, presupuesto)
	marcador := fmt.Sprintf(MarcadorTruncamiento, len(texto)-len(conservado))
	if len(conservado)+len(marcador) > maxBytes {
		// Los dígitos del conteo desplazan el marcador: recortar el
		// contenido lo justo y recalcular con el número exacto.
		exceso := len(conservado) + len(marcador) - maxBytes
		conservado = recortarRunas(conservado, len(conservado)-exceso)
		marcador = fmt.Sprintf(MarcadorTruncamiento, len(texto)-len(conservado))
	}
	if len(conservado)+len(marcador) > maxBytes {
		// Caso extremo (conteos con muchos dígitos): ni el marcador solo
		// cabe, se recorta a sí mismo.
		marcador = recortarRunas(marcador, maxBytes-len(conservado))
	}
	return conservado + marcador
}

// VeredictoDeRama resume el peor veredicto global de la rama: el del commit
// con la revisión más severa. Ignora las fichas ya corregidas (FixedIn): su
// block original fue resuelto en un commit posterior, y el gate no puede
// bloquear la publicación por un hallazgo corregido. Sin fichas pendientes
// devuelve VerdictOK. Lo usan la plantilla de PR y el gate de block de
// pr create.
func VeredictoDeRama(fichas []Ficha) string {
	peor := VerdictOK
	for _, ficha := range fichas {
		if !estaPendiente(ficha) {
			// Corregida: ya no aporta al veredicto de la rama.
			continue
		}
		ultima, ok := ultimaRevision(ficha)
		if !ok {
			continue
		}
		if ordenSeveridad(ultima.Result) > ordenSeveridad(peor) {
			peor = ultima.Result
		}
	}
	return peor
}

// ordenSeveridad ordena los veredictos para poder comparar gravedad.
// Contrato de sincronización: al añadir un veredicto nuevo (constante
// Verdict*), actualizar también este ranking y veredictoDeRama.
func ordenSeveridad(veredicto string) int {
	switch veredicto {
	case VerdictBlock:
		return 4
	case VerdictQuestion:
		return 3
	case VerdictWarn:
		return 2
	case VerdictUnavailable:
		return 1
	default:
		return 0
	}
}

// lineaRiesgo es la primera línea de la plantilla: emoji del veredicto de
// auditoría (NO el estado de CI, guía §12.4) más el conteo global.
func lineaRiesgo(fichas []Ficha) string {
	vd := VeredictoDeRama(fichas)
	return fmt.Sprintf("%s **Veredicto de auditoría: %s** — %s",
		veredictoEmoji(vd), vd, conteoResultados(fichas))
}

func VerdictLine(res *ResultadoRama) string {
	if res.Net == nil {
		return lineaRiesgo(res.Fichas)
	}
	vd := res.Net.Audit.Veredicto
	return fmt.Sprintf("%s **Net audit verdict: %s** — %d finding(s) on net diff %.7s..%.7s",
		veredictoEmoji(vd), vd, len(res.Net.Audit.Findings), res.Net.From, res.Net.To)
}

func InheritedSection(heredados []HallazgoHeredado) string {
	if len(heredados) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## INHERITED (non-blocking)\n")
	for _, h := range heredados {
		fmt.Fprintf(&b, "- %.7s %s/%s\n", h.SHA, h.Hallazgo.Dimension, h.Hallazgo.Severity)
	}
	return b.String() + "\n"
}

// seccionVerificacion describe la verificación de forma honesta (§12.3):
// exit codes reales por comando, contrato tested del agente o el motivo
// explícito de por qué no se ejecutó. Nunca un PASS inventado.
func seccionVerificacion(v VerificacionPlantilla) string {
	var b strings.Builder
	switch v.Modo {
	case "determinista":
		for _, c := range v.Comandos {
			icono := "✅"
			if c.Exit != 0 {
				icono = "❌"
			}
			b.WriteString(fmt.Sprintf("- %s `%s` (exit %d)\n", icono, c.Comando, c.Exit))
		}
	case "delegado":
		for _, tested := range v.Tested {
			b.WriteString(fmt.Sprintf("- 🤖 agent: `%s`\n", tested))
		}
	case "configurar":
		b.WriteString("- ⏸️  Verificación no ejecutada: se detuvo para configurar `vassentinel.yml`.\n")
	case "omitido":
		motivo := v.Motivo
		if motivo == "" {
			motivo = "omitido"
		}
		b.WriteString(fmt.Sprintf("- ⚪ Tests no ejecutados (%s).\n", motivo))
	default:
		b.WriteString("- ⚪ Tests no ejecutados.\n")
	}
	return b.String()
}

// seccionValidacion describe la validación previa (T1.8, internal/validation):
// exit codes reales de las capabilities ejecutadas ANTES de la revisión
// semántica. Nunca inventa un PASS: sin comandos, lo dice explícitamente en
// vez de omitirlo en silencio (misma regla de oro que seccionVerificacion).
func seccionValidacion(cmds []ComandoVerificado) string {
	if len(cmds) == 0 {
		return "- ⚪ Sin comandos de validación configurados.\n"
	}
	var b strings.Builder
	for _, c := range cmds {
		icono := "✅"
		if c.Exit != 0 {
			icono = "❌"
		}
		b.WriteString(fmt.Sprintf("- %s `%s` (exit %d)\n", icono, c.Comando, c.Exit))
	}
	return b.String()
}

// BloqueantesDeRama devuelve los hallazgos CRITICAL de la última revisión de
// cada ficha: son los bloqueos del gate de pr create (guía §12.4). Las fichas
// corregidas (FixedIn) no aportan bloqueantes: su block ya fue resuelto en un
// commit posterior.
//
// T6.5 review finding (design, el más importante): antes leía solo Dims, sin
// el supersede de T6.2 — un hallazgo semántico ya descartado por estar
// superado por uno determinista seguía bloqueando aquí aunque riesgos() ya
// no lo mostrara. Ahora consume ultima.HallazgosEfectivos() (ledger.go), el
// mismo punto de selección que riesgos(), y proyecta el resultado de vuelta a
// []ReviewFinding para no romper el contrato público: el único llamador real
// (avisoSemantico en cmd/sentinel/comandos_pr.go) solo usa Severity/File/
// Line/Description, así que cambiar la firma pública era más invasivo de lo
// necesario para arreglar el bug real.
func BloqueantesDeRama(fichas []Ficha) []ReviewFinding {
	var bloqueantes []ReviewFinding
	for _, ficha := range fichas {
		if !estaPendiente(ficha) {
			continue
		}
		ultima, ok := ultimaRevision(ficha)
		if !ok {
			continue
		}
		for _, h := range ultima.HallazgosEfectivos() {
			if h.Severity == SevCritical {
				bloqueantes = append(bloqueantes, reviewFindingDesdeHallazgo(h))
			}
		}
	}
	return bloqueantes
}

func RenderPlantillaPrRama(res *ResultadoRama, verificacion VerificacionPlantilla, version string) string {
	var b strings.Builder
	b.WriteString(VerdictLine(res) + "\n\n")

	// Validación previa (T1.8): lo que corrió ANTES de auditar, en su propia
	// sección — nunca mezclada con la verificación post-hoc de más abajo.
	b.WriteString("## Validación\n")
	b.WriteString(seccionValidacion(verificacion.Validacion) + "\n")

	b.WriteString("## Rationale\n")
	if res.Overview != nil {
		b.WriteString(res.Overview.Rationale + "\n\n")
	} else {
		b.WriteString("_Sin overview: revisa los commits individuales._\n\n")
	}

	b.WriteString("OWN (per-commit audit)\n")
	b.WriteString(RenderMatriz(res.Fichas) + "\n\n")

	b.WriteString(InheritedSection(res.Heredados))

	b.WriteString("## Riesgos\n")
	var pendientes []string
	if res.Net == nil {
		pendientes = riesgos(res.Fichas)
	} else {
		for _, h := range res.Net.Audit.Findings {
			if h.Severity == SevCritical || h.Severity == SevWarning {
				pendientes = append(pendientes, renderMergedFinding(fmt.Sprintf("%.7s", res.Net.To), h))
			}
		}
	}
	if len(pendientes) == 0 {
		b.WriteString("_Sin riesgos pendientes en la última revisión._\n\n")
	} else {
		for _, r := range pendientes {
			b.WriteString(r + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Verificación\n")
	b.WriteString(seccionVerificacion(verificacion) + "\n")

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("_Generated by VAS Sentinel %s — auditoría de commits, no CI._\n", version))
	return TruncarCuerpo(b.String(), LimiteCuerpoPR)
}
func RenderPlantillaPr(fichas []Ficha, overview *ResultadoOverview, verificacion VerificacionPlantilla, version string) string {
	return RenderPlantillaPrRama(&ResultadoRama{Fichas: fichas, Overview: overview}, verificacion, version)
}
