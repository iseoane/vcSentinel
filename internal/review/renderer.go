package review

import (
	"fmt"
	"strings"
)

// Orden canónico de las columnas de la matriz commit × dimensión.
var ordenDimensiones = []string{DimLogic, DimStyle, DimDesign, DimTests, DimSecurity, DimSpec}

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

	var b strings.Builder
	b.WriteString("| Commit | " + strings.Join(ordenDimensiones, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(ordenDimensiones)+1) + "\n")
	for _, ficha := range fichas {
		sha := ficha.SHA
		if len(sha) > 7 {
			sha = sha[:7]
		}
		celdas := make([]string, 0, len(ordenDimensiones))
		for _, dim := range ordenDimensiones {
			celdas = append(celdas, celdaMatriz(ficha, dim))
		}
		b.WriteString(fmt.Sprintf("| `%s` %s | %s |\n", sha, mensajeCorto(ficha.Message), strings.Join(celdas, " | ")))
	}
	return b.String()
}

// resultadoEmoji mapea el resultado global de una revisión a su icono.
func resultadoEmoji(resultado string) string {
	return veredictoEmoji(resultado)
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
			sha, resultadoEmoji(ultima.Result), ultima.Result, ficha.Model, len(ficha.Revisions), corregida))
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

// marcadorTruncamiento es el texto que señala un cuerpo recortado.
var marcadorTruncamiento = "\n\n> ⚠️ Cuerpo truncado: se omitieron %d bytes.\n"

// TruncarCuerpo recorta un texto al límite de bytes del body (GitHub limita
// el cuerpo de un PR) marcando el truncamiento de forma explícita. El tamaño
// del resultado nunca supera maxBytes.
func TruncarCuerpo(texto string, maxBytes int) string {
	if len(texto) <= maxBytes || maxBytes <= 0 {
		return texto
	}
	marcador := fmt.Sprintf(marcadorTruncamiento, 0)
	marcador = fmt.Sprintf(marcadorTruncamiento, len(texto)-(maxBytes-len(marcador)))
	if len(marcador) > maxBytes {
		return marcador[:maxBytes]
	}
	conservado := texto[:maxBytes-len(marcador)]
	return conservado + marcador
}
