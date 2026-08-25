package review

import (
	"strings"
	"testing"
)

func TestConstruirPromptAuditoriaIncludesBaselineContractFields(t *testing.T) {
	prompt := ConstruirPromptAuditoria(DimLogic, "fix: preserve contract", "diff --git a/a.go", "")

	for _, required := range []string{
		`Audit ONE commit against the "logic" dimension.`,
		"Commit message:\nfix: preserve contract",
		"Diff to audit:\ndiff --git a/a.go",
		"BEGIN_REVIEW",
		"END_REVIEW",
		`{"dim": "logic", "verdict": "ok"}`,
		"Keys: dim, verdict, findings",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt does not contain %q:\n%s", required, prompt)
		}
	}
}

func TestConstruirPromptAuditoriaIncludesDimensionGlossary(t *testing.T) {
	for dimension, definition := range DefinicionesDimensiones {
		prompt := ConstruirPromptAuditoria(dimension, "message", "diff", "")
		if !strings.Contains(prompt, definition) {
			t.Errorf("prompt for %q does not contain glossary definition %q:\n%s", dimension, definition, prompt)
		}
	}
}

func TestConstruirPromptAuditoriaIncludesAnswersOnlyWhenProvided(t *testing.T) {
	withAnswers := ConstruirPromptAuditoria(DimLogic, "message", "diff", "Q1: yes")
	if !strings.Contains(withAnswers, "Clarifications from the user") || !strings.Contains(withAnswers, "Q1: yes") {
		t.Fatalf("prompt omits answers:\n%s", withAnswers)
	}

	withoutAnswers := ConstruirPromptAuditoria(DimLogic, "message", "diff", "  \t")
	if strings.Contains(withoutAnswers, "Clarifications from the user") {
		t.Fatalf("prompt includes empty answers section:\n%s", withoutAnswers)
	}
}

func TestConstruirPromptAuditoriaUsesFallbackForUnknownDimension(t *testing.T) {
	prompt := ConstruirPromptAuditoria("unknown", "message", "diff", "")
	if !strings.Contains(prompt, "No definition available.") {
		t.Fatalf("prompt does not use unknown-dimension fallback:\n%s", prompt)
	}
}

func TestConstruirPromptAuditoriaAllowsBoundedReadOnlyExploration(t *testing.T) {
	prompt := ConstruirPromptAuditoria(DimLogic, "fix(review): bounded tools", "diff", "")

	for _, required := range []string{
		"Read, Grep, and Glob",
		"planned paths only",
		"Do not use Bash",
		"do not write files",
		"do not use the network",
		"literal evidence",
		"confidence",
		"Verify it before claiming that a symbol, file, or behavior does not exist",
	} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt does not contain %q:\n%s", required, prompt)
		}
	}

	if strings.Contains(prompt, "Do NOT run any commands, tools or scripts") {
		t.Fatalf("prompt still forbids all tools:\n%s", prompt)
	}
}

func TestConstruirPromptConContextoListsPlannedPaths(t *testing.T) {
	prompt := construirPromptConContexto(ReviewBundle{}, DimLogic, "message", "diff", "", "", []string{"b.go", "a.go"}, "", "")

	if !strings.Contains(prompt, "Permitted paths:\n- a.go\n- b.go") {
		t.Fatalf("prompt does not list sorted permitted paths:\n%s", prompt)
	}
}
