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
		"<!-- vcsentinel-attestation:v1",
		"<b>review</b>",
		RenderMatrix([]Record{recordHelper("abc123", "feat(review): persist review", "test", revisionHelper(VerdictOK, DimensionResult{Dim: DimSpec, Verdict: VerdictOK}))}),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body is missing %q:\n%s", want, body)
		}
	}
}

func TestPRReviewAttestationUsesCurrentHeadOverSemanticHead(t *testing.T) {
	result := &BranchResult{
		Branch:       "feature/evidence",
		SHAs:         []string{"semantic-head"},
		HeadSHA:      "current-head",
		SemanticBase: "semantic-base",
		SemanticHead: "semantic-head",
		Net:          &NetReview{From: "semantic-base", To: "semantic-head", Audit: AuditResult{Verdict: VerdictOK}},
	}
	attestation := BuildPRReviewAttestation(result, nil, TemplateVerification{}, nil)
	if attestation.HeadSHA != "current-head" {
		t.Fatalf("attestation HeadSHA = %q, want current HEAD", attestation.HeadSHA)
	}
	if _, err := RenderPRReviewBody(result, nil, TemplateVerification{}, attestation, nil); err != nil {
		t.Fatalf("RenderPRReviewBody() rejected current-head attestation: %v", err)
	}
	wrong := attestation
	wrong.HeadSHA = "semantic-head"
	if _, err := RenderPRReviewBody(result, nil, TemplateVerification{}, wrong, nil); err == nil {
		t.Fatal("RenderPRReviewBody() accepted a semantic-head attestation for a current-head result")
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

// TestRenderTestingDoesNotClaimNothingIsConfiguredWhenItNeverLooked is the
// second half of item 2. `pr review` deliberately runs neither validation nor
// verification — that is its contract — but it passed an empty
// TemplateVerification, which renders exactly like a run that looked and found
// nothing configured. The published body therefore stated that this repository
// has no validation commands, minutes after the gate had run four of them.
func TestRenderTestingDoesNotClaimNothingIsConfiguredWhenItNeverLooked(t *testing.T) {
	rendered := renderTesting(TemplateVerification{NotAttempted: true})
	for _, forbidden := range []string{"No validation commands configured", "Tests not run"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("body claims %q although nothing was attempted:\n%s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "does not run") {
		t.Fatalf("body does not say that this command attempts no verification:\n%s", rendered)
	}
}

// TestRenderTestingStillReportsAnEmptyRunHonestly keeps the distinction in both
// directions: a surface that DID attempt verification and found nothing
// configured must still say so, which is the pre-existing golden rule.
func TestRenderTestingStillReportsAnEmptyRunHonestly(t *testing.T) {
	rendered := renderTesting(TemplateVerification{})
	if !strings.Contains(rendered, "No validation commands configured") {
		t.Fatalf("an attempted run that found nothing must still say so:\n%s", rendered)
	}
}

// TestPipelineStepsDoNotClaimNotConfiguredWhenNothingWasAttempted covers the
// machine-readable half of the same claim: the attestation recorded lint, test
// and build as not_configured, which a consumer reads as a fact about the
// repository rather than about what this command chose not to do.
func TestPipelineStepsDoNotClaimNotConfiguredWhenNothingWasAttempted(t *testing.T) {
	steps := pipelineSteps(&BranchResult{}, nil, TemplateVerification{NotAttempted: true}, nil)
	for _, step := range steps {
		switch step.Step {
		case "gate", "lint", "test", "build":
			if step.Status == "not_configured" || step.Status == "not_run" {
				t.Fatalf("step %q reports %q although this command attempts none of them", step.Step, step.Status)
			}
		}
	}
}

// TestEveryProducibleStatusIsPublishable asserts that every status this package
// can emit is one publication accepts.
//
// It no longer guards a drift between two declarations — after the refactor
// both sides read the same one, so no such drift exists. What it guards is
// narrower and still worth having: that the producing code cannot emit a status
// absent from the declaration it is supposed to be constrained by, which is
// reachable through attestationStatusForVerdict returning an unmapped verdict
// verbatim rather than translating it.
//
// The enumeration is the point, so it varies what actually changes a status:
// both NotAttempted states, present and absent commands, all-green and failing
// commands, present and absent intents, and every declared verdict.
func TestEveryProducibleStatusIsPublishable(t *testing.T) {
	green := []VerifiedCommand{{Comando: "go build ./...", Exit: 0}}
	red := []VerifiedCommand{{Comando: "go test ./...", Exit: 0}, {Comando: "go vet ./...", Exit: 1}}
	for _, notAttempted := range []bool{false, true} {
		for verdict := range validVerdicts {
			for _, cmds := range [][]VerifiedCommand{nil, green, red} {
				for _, intents := range [][]IntentLine{nil, {{}}} {
					result := &BranchResult{Records: []Record{{SHA: "a", Revisions: []Revision{{Result: verdict}}}}}
					verification := TemplateVerification{NotAttempted: notAttempted, Comandos: cmds, Validation: cmds}
					for _, step := range pipelineSteps(result, intents, verification, nil) {
						if !AttestationStatusAllowed(step.Step, step.Status) {
							t.Fatalf("step %q emits %q, which publication does not accept (verdict=%s notAttempted=%v)",
								step.Step, step.Status, verdict, notAttempted)
						}
					}
				}
			}
		}
	}
}

// TestPipelineStepsMatchTheDeclaredSteps enforces what the refactor claimed but
// could not remove: pipelineSteps builds each step with its own icon, summary
// and evidence, so it cannot loop over attestationSteps, and its ORDER is
// therefore a second, implicit declaration. Publication validates the stored
// steps positionally against AttestationSteps(), so the two agreeing is the
// contract — asserted here rather than left to whoever edits one of them.
func TestPipelineStepsMatchTheDeclaredSteps(t *testing.T) {
	var emitted []string
	for _, step := range pipelineSteps(&BranchResult{}, nil, TemplateVerification{}, nil) {
		emitted = append(emitted, step.Step)
	}
	declared := AttestationSteps()
	if len(emitted) != len(declared) {
		t.Fatalf("pipelineSteps emits %d steps, AttestationSteps declares %d: %v vs %v", len(emitted), len(declared), emitted, declared)
	}
	for i := range declared {
		if emitted[i] != declared[i] {
			t.Fatalf("step %d is %q but the declaration says %q: publication validates positionally", i, emitted[i], declared[i])
		}
	}
}

// TestAttestationStepsCannotBeMutatedByCallers pins the reason the vocabulary
// is reached through functions: a caller holding the slice must not be able to
// alter what publication validates against.
func TestAttestationStepsCannotBeMutatedByCallers(t *testing.T) {
	taken := AttestationSteps()
	taken[0] = "tampered"
	if AttestationSteps()[0] == "tampered" {
		t.Fatal("a caller rewrote the declared steps")
	}
}
