package review

import (
	"fmt"
	"strings"
)

// IntentLine is one branch intent read from a commit trailer. The reader lives
// in Piece 1; this renderer preserves the provided provenance rather than
// inferring or upgrading it.
type IntentLine struct {
	SHA    string
	Text   string
	Source string
}

// RenderPRReviewBody authors the persisted PR judgement. It owns the five
// fixed reader-facing sections and the machine attestation immediately before
// Pipeline; publication deliberately remains outside this function.
func RenderPRReviewBody(res *BranchResult, intents []IntentLine, verification TemplateVerification, attestation Attestation, dispositions []FindingDisposition) (string, error) {
	if res == nil {
		return "", fmt.Errorf("render pr review body: missing branch result")
	}
	comment, err := RenderAttestation(attestation)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("## Intent\n")
	b.WriteString(renderIntentLines(intents, res.SHAs))
	b.WriteString("\n## What Changed\n")
	b.WriteString(renderChanged(res.Overview))
	b.WriteString("\n## Risk Assessment\n")
	b.WriteString(renderRiskAssessment(res, dispositions))
	b.WriteString("\n## Testing\n")
	b.WriteString(renderTesting(verification))
	b.WriteString("\n")
	b.WriteString(comment)
	b.WriteString("\n\n## Pipeline\n")
	b.WriteString(renderPipeline(res, intents, verification))
	return TruncatePRReviewBody(b.String(), PRBodyLimit)
}

func renderIntentLines(intents []IntentLine, shas []string) string {
	if len(intents) == 0 {
		return "_No intent recorded. These commits were not created through `sentinel slice`._\n"
	}
	seenClaims := make(map[string]struct{}, len(intents))
	claimedSHAs := make(map[string]struct{}, len(intents))
	var b strings.Builder
	for _, intent := range intents {
		text := strings.TrimSpace(intent.Text)
		if text == "" {
			continue
		}
		if intent.SHA != "" {
			claimedSHAs[intent.SHA] = struct{}{}
		}
		claim := text + "\x00" + intent.Source
		if _, seen := seenClaims[claim]; seen {
			continue
		}
		seenClaims[claim] = struct{}{}
		source := "recorded without a recognised provenance"
		switch intent.Source {
		case "declared":
			source = "declared by the human"
		case "conversation":
			source = "derived from the working conversation"
		}
		fmt.Fprintf(&b, "- %s _(%s)_\n", sanitizeText(text), source)
	}
	if b.Len() == 0 {
		return "_No intent recorded. These commits were not created through `sentinel slice`._\n"
	}
	var missing []string
	for _, sha := range shas {
		if _, ok := claimedSHAs[sha]; !ok {
			missing = append(missing, shortBranchSHA(sha))
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "_%d of %d commits carry no recorded intent: %s._\n", len(missing), len(shas), strings.Join(missing, ", "))
	}
	return b.String()
}

func renderChanged(overview *OverviewResult) string {
	if overview == nil || len(overview.Changed) == 0 {
		return "_Not summarised: the overview was unavailable._\n"
	}
	var b strings.Builder
	for _, changed := range overview.Changed {
		changed = strings.TrimSpace(changed)
		if changed != "" {
			fmt.Fprintf(&b, "- %s\n", sanitizeText(changed))
		}
	}
	if b.Len() == 0 {
		return "_Not summarised: the overview was unavailable._\n"
	}
	return b.String()
}

func renderRiskAssessment(res *BranchResult, dispositions []FindingDisposition) string {
	verdict := VerdictDeBranch(res.Records)
	line := riskLine(res.Records)
	if res.Net != nil {
		verdict = res.Net.Audit.Verdict
		line = VerdictLine(res)
	}
	reason := "No overview risk sentence was available."
	if res.Overview != nil && strings.TrimSpace(res.Overview.Risk) != "" {
		reason = sanitizeText(res.Overview.Risk)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n", line, reason)
	if verdict == VerdictBlock {
		for _, finding := range BranchBlockersWithDispositions(res.Records, dispositions) {
			b.WriteString(renderMergedFinding("branch", findingFromReviewFinding(finding.Dimension, finding)) + "\n")
		}
	}
	return b.String()
}

func renderTesting(verification TemplateVerification) string {
	var b strings.Builder
	b.WriteString("Before the review:\n")
	if len(verification.Validation) == 0 {
		b.WriteString("- ⚪ No validation commands configured.\n")
	} else {
		b.WriteString(validationSection(verification.Validation))
	}
	b.WriteString("After the review:\n")
	b.WriteString(verificationSection(verification))
	return b.String()
}

func renderPipeline(res *BranchResult, intents []IntentLine, verification TemplateVerification) string {
	var b strings.Builder
	if len(intents) == 0 {
		b.WriteString(pipelineDetails("⚪", "slice", "not observed", "No Sentinel-Intent trailers were present."))
	} else {
		b.WriteString(pipelineDetails("✅", "slice", "intent trailers present", renderIntentLines(intents, res.SHAs)))
	}
	b.WriteString(renderReviewPipeline(res))
	b.WriteString(renderCommandPipeline("gate", verification.Validation, "not run"))
	b.WriteString(renderCommandPipeline("lint", nil, "not configured"))
	b.WriteString(renderCommandPipeline("test", verification.Comandos, "not configured"))
	b.WriteString(renderCommandPipeline("build", nil, "not configured"))
	b.WriteString(pipelineDetails("✅", "pr review", "body and attestation authored", "This persisted entry was authored for the reviewed branch head."))
	b.WriteString(pipelineDetails("⚪", "ci", "not observed by Sentinel", "Filled by pr create when CI evidence is available."))
	return b.String()
}

func renderReviewPipeline(res *BranchResult) string {
	if len(res.Records) == 0 {
		return pipelineDetails("⚪", "review", "no records", "No commit review records were found.")
	}
	summary := fmt.Sprintf("%d commits audited", len(res.Records))
	var evidence strings.Builder
	retired := false
	for _, record := range res.Records {
		if record.FixedIn != "" {
			retired = true
			fmt.Fprintf(&evidence, "- `%s` blocked → `%s` fix commit → re-audited\n", shortBranchSHA(record.SHA), shortBranchSHA(record.FixedIn))
		}
	}
	pending := len(BranchBlockers(res.Records))
	icon := "✅"
	switch {
	case pending > 0:
		summary += fmt.Sprintf(", %d pending blocks", pending)
		icon = "🚨"
	case retired:
		summary += ", blocks retired by later re-audited fixes"
		icon = "🔧"
	default:
		summary += ", no pending blocks"
	}
	evidence.WriteString(RenderMatrix(res.Records))
	return pipelineDetails(icon, "review", summary, evidence.String())
}

func renderCommandPipeline(step string, commands []VerifiedCommand, absent string) string {
	if len(commands) == 0 {
		return pipelineDetails("⚪", step, absent, "")
	}
	failed := false
	var evidence strings.Builder
	for _, command := range commands {
		icon := "✅"
		if command.Exit != 0 {
			icon, failed = "⚠️", true
		}
		fmt.Fprintf(&evidence, "- %s `%s` (exit %d)\n", icon, command.Comando, command.Exit)
	}
	icon := "✅"
	if failed {
		icon = "⚠️"
	}
	return pipelineDetails(icon, step, strings.TrimSpace(evidence.String()), evidence.String())
}

func pipelineDetails(icon, step, summary, evidence string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary>%s <b>%s</b> — %s</summary>\n\n", icon, step, sanitizeText(summary))
	if strings.TrimSpace(evidence) != "" {
		b.WriteString(evidence)
		if !strings.HasSuffix(evidence, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("</details>\n\n")
	return b.String()
}
