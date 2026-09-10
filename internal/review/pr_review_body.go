package review

import (
	"errors"
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
	if err := validateAttestation(res, attestation); err != nil {
		return "", err
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
	fixed := b.String()

	pipeline, _, err := truncatePipeline(fixed, pipelineSteps(res, intents, verification, dispositions), PRBodyLimit)
	if err != nil {
		return "", err
	}
	body := fixed + pipeline
	// The reserve is what pr create will expand the ci step into, so the
	// published body must still fit once it does. Checking the returned
	// length here is the only place that can state it about the real result.
	if len(body)+ciStepReserveBytes > PRBodyLimit {
		return "", fmt.Errorf("%w: %d bytes plus the ci reserve exceeds %d", ErrPRReviewBodyTooLarge, len(body), PRBodyLimit)
	}
	return body, nil
}

func validateAttestation(res *BranchResult, attestation Attestation) error {
	verdict := VerdictDeBranch(res.Records)
	head := ""
	if len(res.SHAs) > 0 {
		head = res.SHAs[len(res.SHAs)-1]
	}
	if res.Net != nil {
		verdict, head = res.Net.Audit.Verdict, res.Net.To
	}
	if attestation.Branch != res.Branch || attestation.HeadSHA != head || attestation.Verdict != verdict {
		return errors.New("render pr review body: attestation does not match branch result")
	}
	return nil
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

// pipelineSteps builds the Pipeline as data, in its fixed order. Serialising
// is a separate, later step (renderPipelineSteps): truncation happens on this
// slice, which is what keeps it from ever reaching another section.
func pipelineSteps(res *BranchResult, intents []IntentLine, verification TemplateVerification, dispositions []FindingDisposition) []pipelineStep {
	slice := pipelineStep{Icon: "⚪", Step: "slice", Summary: "not observed", Evidence: "No Sentinel-Intent trailers were present."}
	if len(intents) > 0 {
		slice = pipelineStep{Icon: "✅", Step: "slice", Summary: "intent trailers present", Evidence: renderIntentLines(intents, res.SHAs)}
	}
	return []pipelineStep{
		slice,
		reviewPipelineStep(res, dispositions),
		commandPipelineStep("gate", verification.Validation, "not run"),
		commandPipelineStep("lint", nil, "not configured"),
		commandPipelineStep("test", verification.Comandos, "not configured"),
		commandPipelineStep("build", nil, "not configured"),
		{Icon: "✅", Step: "pr review", Summary: "body and attestation authored", Evidence: "This persisted entry was authored for the reviewed branch head."},
		{Icon: "⚪", Step: "ci", Summary: "not observed by Sentinel", Evidence: "Filled by pr create when CI evidence is available.", CI: true},
	}
}

func reviewPipelineStep(res *BranchResult, dispositions []FindingDisposition) pipelineStep {
	if len(res.Records) == 0 {
		return pipelineStep{Icon: "⚪", Step: "review", Summary: "no records", Evidence: "No commit review records were found."}
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
	pending := len(BranchBlockersWithDispositions(res.Records, dispositions))
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
	return pipelineStep{Icon: icon, Step: "review", Summary: summary, Evidence: evidence.String()}
}

func commandPipelineStep(step string, commands []VerifiedCommand, absent string) pipelineStep {
	if len(commands) == 0 {
		return pipelineStep{Icon: "⚪", Step: step, Summary: absent}
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
	return pipelineStep{Icon: icon, Step: step, Summary: strings.TrimSpace(evidence.String()), Evidence: evidence.String()}
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
