package main

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/secret"
)

func TestProyectarIncidentesSecretoVacio(t *testing.T) {
	if got := proyectarIncidentesSecreto(nil); len(got) != 0 {
		t.Fatalf("proyectarIncidentesSecreto(nil) = %#v, expected empty", got)
	}
}

func TestProyectarIncidentesSecretoEsDeterministaYSinDimension(t *testing.T) {
	incidentes := []secret.Incident{{Path: "docs/runbook.md", Shape: "github_token", Line: 12}}
	got := proyectarIncidentesSecreto(incidentes)
	if len(got) != 1 {
		t.Fatalf("len = %d, expected 1", len(got))
	}
	h := got[0]
	if h.Source != review.SourceValidation {
		t.Errorf("Source = %q, expected %q", h.Source, review.SourceValidation)
	}
	if h.Dimension != "" {
		t.Errorf("Dimension = %q, expected empty (never a review dimension, never supersedes)", h.Dimension)
	}
	if h.Severity != review.SevWarning {
		t.Errorf("Severity = %q, expected %q (reports, never blocks)", h.Severity, review.SevWarning)
	}
	if h.Status != review.StatusPending {
		t.Errorf("Status = %q, expected %q", h.Status, review.StatusPending)
	}
	if h.Confidence != 0.9 {
		t.Errorf("Confidence = %v, expected 0.9", h.Confidence)
	}
	if h.Evidence != "" {
		t.Errorf("Evidence = %q, expected empty (the value must never persist)", h.Evidence)
	}
	if h.Location.Archivo != "docs/runbook.md" || h.Location.LineaInicio != 12 {
		t.Errorf("Location = %+v, expected docs/runbook.md:12", h.Location)
	}
	if h.Fingerprint == "" {
		t.Error("Fingerprint empty, expected a stable computed value")
	}
	if h.Fingerprint != review.Fingerprint(h) {
		t.Error("Fingerprint is not stable under recomputation")
	}
}

func TestProyectarIncidentesSecretoNuncaExponeElValor(t *testing.T) {
	valor := "ghp_" + strings.Repeat("A", 24)
	_ = valor
	got := proyectarIncidentesSecreto([]secret.Incident{{Path: "notes.md", Shape: "github_token", Line: 3}})
	for _, campo := range []string{got[0].Title, got[0].Description, got[0].Evidence} {
		if strings.Contains(campo, "ghp_") {
			t.Errorf("field %q carries a credential-looking value", campo)
		}
	}
	if esperado := "exposed credential (github_token)"; got[0].Title != esperado {
		t.Errorf("Title = %q, expected %q", got[0].Title, esperado)
	}
}

func TestProyectarIncidentesSecretoHuellasDistintasPorForma(t *testing.T) {
	got := proyectarIncidentesSecreto([]secret.Incident{
		{Path: "a.md", Shape: "github_token", Line: 1},
		{Path: "a.md", Shape: "pem_block", Line: 9},
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, expected 2", len(got))
	}
	if got[0].Fingerprint == got[1].Fingerprint {
		t.Error("two shapes in one file share a fingerprint; dispositions could not address them apart")
	}
}

func TestAvisosSecretoVacios(t *testing.T) {
	if got := avisosSecreto(nil, nil); len(got) != 0 {
		t.Fatalf("avisosSecreto(nil, nil) = %#v, expected no output on absence", got)
	}
}

func TestAvisosSecretoNombraRutaYForma(t *testing.T) {
	got := avisosSecreto([]secret.Incident{{Path: "docs/runbook.md", Shape: "pem_block", Line: 7}}, nil)
	if len(got) != 1 {
		t.Fatalf("len = %d, expected 1", len(got))
	}
	for _, want := range []string{"docs/runbook.md", "7", "pem_block"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("aviso %q does not name %q", got[0], want)
		}
	}
}

func TestAvisosSecretoDesconocidoNoEsLimpio(t *testing.T) {
	got := avisosSecreto(nil, []string{"assets/logo.png"})
	if len(got) != 1 {
		t.Fatalf("len = %d, expected 1", len(got))
	}
	if !strings.Contains(got[0], "assets/logo.png") {
		t.Errorf("aviso %q does not name the unreadable path", got[0])
	}
	for _, word := range []string{"clean", "limpio", "0 credentials", "no credentials"} {
		if strings.Contains(strings.ToLower(got[0]), word) {
			t.Errorf("aviso %q renders absence as a verdict", got[0])
		}
	}
}
