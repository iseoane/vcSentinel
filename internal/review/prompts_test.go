package review

import (
	"strings"
	"testing"
)

func TestPromptContieneContrato(t *testing.T) {
	prompt := ConstruirPromptAuditoria(DimSecurity, "feat(auth): valida tokens", "diff --git a/auth.go b/auth.go\n+token", "")

	for _, fragmento := range []string{
		"security",
		"feat(auth): valida tokens",
		"diff --git a/auth.go b/auth.go",
		"BEGIN_REVIEW",
		"END_REVIEW",
		"CRITICAL",
		"question",
		"JSONL",
	} {
		if !strings.Contains(prompt, fragmento) {
			t.Errorf("el prompt debería contener %q", fragmento)
		}
	}
}

func TestPromptIncluyeGlosarioDeLaDimension(t *testing.T) {
	prompt := ConstruirPromptAuditoria(DimDesign, "msg", "diff", "")
	if !strings.Contains(prompt, DefinicionesDimensiones[DimDesign]) {
		t.Error("el prompt debería incluir la definición canónica de design")
	}
}

func TestPromptConRespuestasIncluyeSegundaRonda(t *testing.T) {
	prompt := ConstruirPromptAuditoria(DimLogic, "msg", "diff", "Q1: sí; Q2: mantener la cola")
	if !strings.Contains(prompt, "Q1: sí; Q2: mantener la cola") {
		t.Error("el prompt debería incluir las respuestas del usuario")
	}
}

func TestPromptSinRespuestasNoIncluyeSeccion(t *testing.T) {
	prompt := ConstruirPromptAuditoria(DimLogic, "msg", "diff", "")
	if strings.Contains(prompt, "Clarifications from the user") {
		t.Error("sin respuestas no debería aparecer la sección de aclaraciones")
	}
}

func TestPromptDimensionDesconocidaNoRompe(t *testing.T) {
	prompt := ConstruirPromptAuditoria("perf", "msg", "diff", "")
	if prompt == "" {
		t.Fatal("una dimensión desconocida debería producir un prompt válido")
	}
}
