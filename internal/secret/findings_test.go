package secret

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func TestProjectSecretIncidentsEmpty(t *testing.T) {
	if got := ProjectSecretIncidents(nil); len(got) != 0 {
		t.Fatalf("ProjectSecretIncidents(nil) = %#v, expected empty", got)
	}
}

func TestProjectSecretIncidentsDeterministicWithoutDimension(t *testing.T) {
	incidents := []Incident{{Path: "docs/runbook.md", Shape: "github_token", Line: 12}}
	got := ProjectSecretIncidents(incidents)
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

func TestProjectSecretIncidentsNeverExposesValue(t *testing.T) {
	value := "ghp_" + strings.Repeat("A", 24)
	_ = value
	got := ProjectSecretIncidents([]Incident{{Path: "notes.md", Shape: "github_token", Line: 3}})
	for _, field := range []string{got[0].Title, got[0].Description, got[0].Evidence} {
		if strings.Contains(field, "ghp_") {
			t.Errorf("field %q carries a credential-looking value", field)
		}
	}
	if expected := "exposed credential (github_token)"; got[0].Title != expected {
		t.Errorf("Title = %q, expected %q", got[0].Title, expected)
	}
}

func TestProjectSecretIncidentsDistinctFingerprintsPerShape(t *testing.T) {
	got := ProjectSecretIncidents([]Incident{
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

func TestSecretAdvisoriesEmpty(t *testing.T) {
	if got := SecretAdvisories(nil, nil); len(got) != 0 {
		t.Fatalf("SecretAdvisories(nil, nil) = %#v, expected no output on absence", got)
	}
}

func TestSecretAdvisoriesNamePathAndShape(t *testing.T) {
	got := SecretAdvisories([]Incident{{Path: "docs/runbook.md", Shape: "pem_block", Line: 7}}, nil)
	if len(got) != 1 {
		t.Fatalf("len = %d, expected 1", len(got))
	}
	for _, want := range []string{"docs/runbook.md", "7", "pem_block"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("advisory %q does not name %q", got[0], want)
		}
	}
}

func TestSecretAdvisoriesUnknownIsNotClean(t *testing.T) {
	got := SecretAdvisories(nil, []string{"assets/logo.png"})
	if len(got) != 1 {
		t.Fatalf("len = %d, expected 1", len(got))
	}
	if !strings.Contains(got[0], "assets/logo.png") {
		t.Errorf("advisory %q does not name the unreadable path", got[0])
	}
	for _, word := range []string{"clean", "limpio", "0 credentials", "no credentials"} {
		if strings.Contains(strings.ToLower(got[0]), word) {
			t.Errorf("advisory %q renders absence as a verdict", got[0])
		}
	}
}
