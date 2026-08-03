package review

import (
	"fmt"
	"strings"
)

// DefinicionesDimensiones es el glosario canónico inyectado en el prompt del
// agente para que la salida use SIEMPRE los términos canónicos de las seis
// dimensiones.
var DefinicionesDimensiones = map[string]string{
	DimLogic:    "Correctness of behavior: conditions, error handling, edge cases and side effects.",
	DimStyle:    "Language and clarity: naming, idiomatic code and adherence to repository conventions.",
	DimDesign:   "Structure and coupling: cohesion, open/closed principle, deep modules and dependencies pointing toward the domain.",
	DimTests:    "Test value: meaningful coverage, determinism and a failing test implying a real behavior change.",
	DimSecurity: "Privilege boundaries, untrusted input handling and exposed data.",
	DimSpec:     "The diff does exactly what the commit message claims: no out-of-scope work and no unbacked claims.",
}

// ConstruirPromptAuditoria arma el prompt maestro para auditar un commit
// contra una dimensión. Incluye el mensaje del commit, el diff, el glosario
// de la dimensión, los smells de diseño como guía y, si el usuario resolvió
// preguntas (--answer), esas respuestas como segunda ronda.
func ConstruirPromptAuditoria(dimension, mensaje, diff, respuestas string) string {
	definicion := DefinicionesDimensiones[dimension]
	if definicion == "" {
		definicion = "No definition available."
	}

	seccionRespuestas := ""
	if strings.TrimSpace(respuestas) != "" {
		seccionRespuestas = fmt.Sprintf(`
Clarifications from the user (resolve the pending questions with these and finish the audit):
%s
`, respuestas)
	}

	return fmt.Sprintf(`You are a rigorous technical auditor. Audit ONE commit against the %q dimension.

Dimension definition:
%s

Commit message:
%s

Diff to audit:
%s

Audit rules:
- Report only actionable findings introduced by this diff; distinguish pre-existing issues from new ones.
- Severity: CRITICAL only for a real defect introduced here; WARNING for reasonable debt; ADVISORY for suggestions.
- Use code smells as a guide: primitive obsession, duplicated code, feature envy, switch/if chains, long parameter lists, etc.
- If you cannot audit without clarification, return a "question" verdict with at most 3 concise questions (answerable yes/no or a concrete choice).
- If there is nothing to report, return {"dim": %q, "verdict": "ok"}.
- Output ONLY one JSONL object between BEGIN_REVIEW and END_REVIEW. No markdown outside the delimiters. No commentary. Keys: dim, verdict, findings (dimension, file, line, severity, description, suggestion), questions (id, text), reason.`+seccionRespuestas+`
BEGIN_REVIEW
END_REVIEW`, dimension, definicion, mensaje, diff, dimension)
}
