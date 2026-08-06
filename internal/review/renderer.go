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
}

// riesgos reúne los hallazgos CRITICAL y WARNING de la última revisión de
// cada ficha (los ADVISORY son información, no riesgos).
func riesgos(fichas []Ficha) []string {
	var lineas []string
	for _, ficha := range fichas {
		ultima, ok := ultimaRevision(ficha)
		if !ok {
			continue
		}
		sha := ficha.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		for _, dr := range ultima.Dims {
			for _, h := range dr.Findings {
				if h.Severity != SevCritical && h.Severity != SevWarning {
					continue
				}
				lineas = append(lineas, fmt.Sprintf("- %s `%s` [%s] %s — %s (%s:%d)",
					severidadEmoji(h.Severity), sha, h.Dimension, h.Severity, h.Description, h.File, h.Line))
			}
		}
	}
	return lineas
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

// veredictoDeRama resume el peor veredicto global de la rama: el del commit
// con la revisión más severa. Sin fichas devuelve VerdictOK.
func veredictoDeRama(fichas []Ficha) string {
	peor := VerdictOK
	for _, ficha := range fichas {
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
	vd := veredictoDeRama(fichas)
	return fmt.Sprintf("%s **Veredicto de auditoría: %s** — %s",
		veredictoEmoji(vd), vd, conteoResultados(fichas))
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

// RenderPlantillaPr construye el cuerpo del PR (sentinel_pr.md, guía §12.4):
// línea de riesgo, rationale del overview, matriz, sección de verificación
// honesta y firma con versión. El cuerpo se trunca al límite de GitHub con el
// marcador explícito.
func RenderPlantillaPr(fichas []Ficha, overview *ResultadoOverview, verificacion VerificacionPlantilla, version string) string {
	var b strings.Builder
	b.WriteString(lineaRiesgo(fichas) + "\n\n")

	b.WriteString("## Rationale\n")
	if overview != nil {
		b.WriteString(overview.Rationale + "\n\n")
	} else {
		b.WriteString("_Sin overview: revisa los commits individuales._\n\n")
	}

	b.WriteString("## Matriz de auditoría\n")
	b.WriteString(RenderMatriz(fichas) + "\n\n")

	b.WriteString("## Verificación\n")
	b.WriteString(seccionVerificacion(verificacion) + "\n")

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("_Generated by VAS Sentinel %s — auditoría de commits, no CI._\n", version))
	return TruncarCuerpo(b.String(), LimiteCuerpoPR)
}
