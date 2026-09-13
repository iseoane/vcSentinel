package review

import (
	"strings"
	"testing"
)

// TestRenderPRReviewBodySanitizesCommandText covers the persisted body and
// Pipeline copies of command evidence. Literal command backticks render as
// apostrophes, not nested Markdown delimiters; shell punctuation remains intact.
func TestRenderPRReviewBodySanitizesCommandText(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{name: "LF", command: "go test\n## forged", want: "go test ## forged"},
		{name: "CRLF", command: "go test\r\n## forged", want: "go test ## forged"},
		{name: "bare CR", command: "go test\r## forged", want: "go test ## forged"},
		{name: "backticks", command: "printf `literal`", want: "printf 'literal'"},
		{name: "heading", command: "# forged heading", want: "# forged heading"},
		{name: "list item", command: "- forged list item", want: "- forged list item"},
		{name: "details", command: "<details>forged</details>", want: "<details>forged</details>"},
		{name: "HTML comment", command: "<!-- forged -->", want: "<!-- forged -->"},
		{name: "shell punctuation", command: `printf 'hello'; echo "$HOME" && exit 0`, want: `printf 'hello'; echo "$HOME" && exit 0`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := RenderPRReviewBody(&BranchResult{
				Branch: "feature/sanitized-commands",
				SHAs:   []string{"abc123"},
			}, nil, TemplateVerification{
				Mode:       "determinista",
				Comandos:   []VerifiedCommand{{Comando: tc.command, Exit: 0}},
				Validation: []VerifiedCommand{{Comando: tc.command, Exit: 0}},
			}, Attestation{Branch: "feature/sanitized-commands", HeadSHA: "abc123", Verdict: VerdictOK}, nil)
			if err != nil {
				t.Fatalf("RenderPRReviewBody() error = %v", err)
			}
			want := "- ✅ `" + tc.want + "` (exit 0)"
			if !strings.Contains(body, want) {
				t.Fatalf("body missing sanitized command %q:\n%s", want, body)
			}
			if tc.command != tc.want && strings.Contains(body, tc.command) {
				t.Fatalf("body retains unsanitized command %q:\n%s", tc.command, body)
			}
		})
	}
}

func TestRenderPRReviewBodyRendersOmittedVerificationReasonAsCode(t *testing.T) {
	body, err := RenderPRReviewBody(&BranchResult{
		Branch: "feature/verification-error",
		SHAs:   []string{"abc123"},
	}, nil, TemplateVerification{
		Mode:   "omitido",
		Reason: "verification_error: tool failed\r\n<details><summary>forged `code`</summary></details>",
	}, Attestation{Branch: "feature/verification-error", HeadSHA: "abc123", Verdict: VerdictOK}, nil)
	if err != nil {
		t.Fatalf("RenderPRReviewBody() error = %v", err)
	}
	const want = "- ⚪ Tests not run (`verification_error: tool failed <details><summary>forged 'code'</summary></details>`)."
	if !strings.Contains(body, want) {
		t.Fatalf("body missing inert verification reason %q:\n%s", want, body)
	}
}

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
