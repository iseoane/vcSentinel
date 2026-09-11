package review

import (
	"strings"
	"testing"
)

func TestRenderPRReviewBodyUsesTheFixedSectionOrder(t *testing.T) {
	body, err := RenderPRReviewBody(&BranchResult{
		Branch: "feature/persisted-review",
		SHAs:   []string{"abc123"},
		Records: []Record{recordHelper("abc123", "feat(review): persist review", "test",
			revisionHelper(VerdictOK, DimensionResult{Dim: DimSpec, Verdict: VerdictOK})),
		},
		Overview: &OverviewResult{
			Changed: []string{"Persists the branch review entry.", "Seals the machine attestation.", "Reports audit evidence in the Pipeline."},
			Risk:    "The change is isolated to PR review persistence.",
		},
	}, []IntentLine{{SHA: "abc123", Text: "persist the branch review", Source: "declared"}}, TemplateVerification{
		Validation: []VerifiedCommand{{Comando: "go vet ./...", Exit: 0}},
		Mode:       "determinista",
		Comandos:   []VerifiedCommand{{Comando: "go test ./...", Exit: 0}},
	}, Attestation{HeadSHA: "abc123", Branch: "feature/persisted-review", Verdict: VerdictOK}, nil)
	if err != nil {
		t.Fatalf("RenderPRReviewBody() error = %v", err)
	}

	sections := []string{"## Intent", "## What Changed", "## Risk Assessment", "## Testing", "## Pipeline"}
	previous := -1
	for _, section := range sections {
		index := strings.Index(body, section)
		if index < 0 {
			t.Fatalf("body is missing %q:\n%s", section, body)
		}
		if index <= previous {
			t.Fatalf("section %q is out of order:\n%s", section, body)
		}
		previous = index
	}
	for _, want := range []string{
		"persist the branch review _(declared by the human)_",
		"Persists the branch review entry.",
		"✅ `go vet ./...` (exit 0)",
		"✅ `go test ./...` (exit 0)",
		"<!-- vas-sentinel-attestation:v1",
		"<b>review</b>",
		RenderMatrix([]Record{recordHelper("abc123", "feat(review): persist review", "test", revisionHelper(VerdictOK, DimensionResult{Dim: DimSpec, Verdict: VerdictOK}))}),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestRenderPRReviewBodyPreservesIntentProvenanceAndReportsMissingCommits(t *testing.T) {
	body, err := RenderPRReviewBody(&BranchResult{SHAs: []string{
		"aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd",
	}}, []IntentLine{
		{SHA: "aaaaaaaa", Text: "protect the release pipeline", Source: "declared"},
		{SHA: "bbbbbbbb", Text: "protect the release pipeline", Source: "declared"},
		{SHA: "cccccccc", Text: "protect the release pipeline", Source: "conversation"},
	}, TemplateVerification{}, Attestation{HeadSHA: "dddddddd", Verdict: VerdictOK}, nil)
	if err != nil {
		t.Fatalf("RenderPRReviewBody() error = %v", err)
	}
	intentSection := body[:strings.Index(body, "## What Changed")]
	if got := strings.Count(intentSection, "protect the release pipeline"); got != 2 {
		t.Fatalf("intent claims = %d, want the declared and conversation claims once each:\n%s", got, intentSection)
	}
	for _, want := range []string{
		"_(declared by the human)_",
		"_(derived from the working conversation)_",
		"_1 of 4 commits carry no recorded intent: dddddddd._",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestRenderPRReviewBodyKeepsComputedBlockVerdictWhenRiskIsReassuring(t *testing.T) {
	body, err := RenderPRReviewBody(&BranchResult{
		Records: []Record{recordHelper("blocked", "feat: unsafe", "test",
			revisionHelper(VerdictBlock, DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock})),
		},
		Overview: &OverviewResult{Risk: "Everything is safe and low risk.\n## Forged section"},
	}, nil, TemplateVerification{}, Attestation{Verdict: VerdictBlock}, nil)
	if err != nil {
		t.Fatalf("RenderPRReviewBody() error = %v", err)
	}
	if !strings.Contains(body, "🚨 **Audit verdict: block**") || !strings.Contains(body, "Everything is safe and low risk. ## Forged section") {
		t.Fatalf("computed block verdict was not preserved:\n%s", body)
	}
	if strings.Count(body, "## Risk Assessment") != 1 {
		t.Fatalf("untrusted risk forged a section:\n%s", body)
	}
}
