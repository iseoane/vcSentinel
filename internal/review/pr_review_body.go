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

const NoRecordedIntentForPRRange = "No intent recorded for this PR range."

// IntentText combines recorded intent claims for the net reviewer prompt.
func IntentText(intents []IntentLine) string {
	claims := make([]string, 0, len(intents))
	for _, intent := range intents {
		if strings.TrimSpace(intent.Text) != "" {
			claims = append(claims, strings.TrimSpace(intent.Text))
		}
	}
	return strings.Join(claims, "\n")
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

	steps := pipelineSteps(res, intents, verification, dispositions)
	pipeline, _, err := truncatePipeline(fixed, steps, PRBodyLimit)
	if err != nil {
		return "", err
	}
	body := fixed + pipeline
	// Restate the guarantee about the real returned bytes, using the same
	// arithmetic the loop used: what must fit is the body pr create will
	// publish, with the ci placeholder replaced by at most its reserve.
	if size := publishedSize(fixed, pipeline, "", steps); size > PRBodyLimit {
		return "", fmt.Errorf("%w: %d published bytes exceeds %d", ErrPRReviewBodyTooLarge, size, PRBodyLimit)
	}
	return body, nil
}

// BuildPRReviewAttestation creates the machine attestation from the same Pipeline
// data used by RenderPRReviewBody, keeping their fixed step order and statuses in
// lockstep.
func BuildPRReviewAttestation(res *BranchResult, intents []IntentLine, verification TemplateVerification, dispositions []FindingDisposition) Attestation {
	verdict := VerdictDeBranch(res.Records, dispositions)
	head := ""
	if len(res.SHAs) > 0 {
		head = res.SHAs[len(res.SHAs)-1]
	}
	if res.Net != nil {
		verdict, head = res.Net.Audit.Verdict, res.Net.To
	}
	steps := pipelineSteps(res, intents, verification, dispositions)
	attestationSteps := make([]AttestationStep, 0, len(steps))
	for _, step := range steps {
		attestationSteps = append(attestationSteps, AttestationStep{Step: step.Step, Status: step.Status})
	}
	return Attestation{Branch: res.Branch, HeadSHA: head, Verdict: verdict, Steps: attestationSteps}
}

func validateAttestation(res *BranchResult, attestation Attestation) error {
	verdict := VerdictDeBranch(res.Records, nil)
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
	verdict := VerdictDeBranch(res.Records, dispositions)
	line := riskLine(res.Records, dispositions)
	if res.Net != nil {
		verdict = res.Net.Audit.Verdict
		line = VerdictLine(res, dispositions)
	}
	reason := "No overview risk sentence was available."
	if res.Overview != nil && strings.TrimSpace(res.Overview.Risk) != "" {
		reason = sanitizeText(res.Overview.Risk)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n", line, reason)
	if verdict == VerdictBlock {
		for _, finding := range BranchBlockers(res.Records, dispositions) {
			b.WriteString(renderMergedFinding("branch", findingFromReviewFinding(finding.Dimension, finding)) + "\n")
		}
	}
	return b.String()
}

// AttestationSteps is the single declaration of the Pipeline steps: their
// identity and their order. internal/app/pr validates a stored attestation
// against it positionally rather than restating the list.
//
// AttestationStatusAllowed answers whether a step may carry a status. Both the
// steps and their vocabularies were declared independently from the publication
// validator until 2026-09-15, when adding "not_attempted" on the producing side
// alone would have made pr create reject every entry pr review writes — with
// every package test green, since pr review authors the attestation and pr
// create validates it and nothing exercised the round trip.
//
// The vocabulary is reached through a function rather than an exported map: a
// map is a mutable reference, so exporting one would let any importing package
// rewrite the sole guardian of what publication accepts. That is a worse
// failure than the duplication this replaced.
//
// The "ci" step is filled by pr create after this package has rendered, so its
// vocabulary is wider than what this package emits.
func AttestationSteps() []string {
	return append([]string(nil), attestationSteps...)
}

func AttestationStatusAllowed(step, status string) bool {
	return allowedAttestationStatuses[step][status]
}

var attestationSteps = []string{"slice", "review", "gate", "lint", "test", "build", "pr review", "ci"}

var allowedAttestationStatuses = map[string]map[string]bool{
	"slice":     {statusPassed: true, statusNotObserved: true},
	"review":    {statusPassed: true, statusWarning: true, statusBlocked: true, VerdictQuestion: true, VerdictUnavailable: true, statusNotObserved: true},
	"gate":      {statusPassed: true, statusFailed: true, statusNotRun: true, statusNotAttempted: true},
	"lint":      {statusPassed: true, statusFailed: true, statusNotConfigured: true, statusNotAttempted: true},
	"test":      {statusPassed: true, statusFailed: true, statusNotConfigured: true, statusNotAttempted: true},
	"build":     {statusPassed: true, statusFailed: true, statusNotConfigured: true, statusNotAttempted: true},
	"pr review": {statusAuthored: true},
	"ci":        {statusPassed: true, statusFailed: true, statusWarning: true, "pending": true, statusNotObserved: true},
}

const (
	statusPassed        = "passed"
	statusFailed        = "failed"
	statusWarning       = "warning"
	statusBlocked       = "blocked"
	statusAuthored      = "authored"
	statusNotObserved   = "not_observed"
	statusNotRun        = "not_run"
	statusNotConfigured = "not_configured"
	statusNotAttempted  = "not_attempted"
)

func renderTesting(verification TemplateVerification) string {
	var b strings.Builder
	// Stated once, plainly, instead of rendering two lines that read as
	// findings about the repository's configuration. What this command did
	// not do is not evidence about what is configured.
	if verification.NotAttempted {
		b.WriteString("- ⚪ This command does not run validation or verification; see `gate` and the commit reviews for that evidence.\n")
		return b.String()
	}
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
	absentStatus, absentSummary := statusNotConfigured, "not configured"
	gateStatus, gateSummary := statusNotRun, "not run"
	if verification.NotAttempted {
		absentStatus, absentSummary = statusNotAttempted, "not attempted by this command"
		gateStatus, gateSummary = absentStatus, absentSummary
	}
	slice := pipelineStep{Icon: "⚪", Step: "slice", Status: statusNotObserved, Summary: "not observed", Evidence: "No Sentinel-Intent trailers were present."}
	if len(intents) > 0 {
		slice = pipelineStep{Icon: "✅", Step: "slice", Status: statusPassed, Summary: "intent trailers present", Evidence: renderIntentLines(intents, res.SHAs)}
	}
	return []pipelineStep{
		slice,
		reviewPipelineStep(res, dispositions),
		// A surface that attempts none of these must not report them as
		// not configured or not run: both read as facts about the
		// repository, and a consumer of the attestation cannot tell them
		// apart from a real absence.
		commandPipelineStep("gate", verification.Validation, gateStatus, gateSummary),
		commandPipelineStep("lint", nil, absentStatus, absentSummary),
		commandPipelineStep("test", verification.Comandos, absentStatus, absentSummary),
		commandPipelineStep("build", nil, absentStatus, absentSummary),
		{Icon: "✅", Step: "pr review", Status: statusAuthored, Summary: "body and attestation authored", Evidence: "This persisted entry was authored for the reviewed branch head."},
		{Icon: "⚪", Step: "ci", Status: statusNotObserved, Summary: "not observed by Sentinel", Evidence: "Filled by pr create when CI evidence is available.", CI: true},
	}
}

func reviewPipelineStep(res *BranchResult, dispositions []FindingDisposition) pipelineStep {
	if len(res.Records) == 0 {
		return pipelineStep{Icon: "⚪", Step: "review", Status: statusNotObserved, Summary: "no records", Evidence: "No commit review records were found."}
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
	pending := len(BranchBlockers(res.Records, dispositions))
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
	return pipelineStep{Icon: icon, Step: "review", Status: attestationStatusForVerdict(VerdictDeBranch(res.Records, dispositions)), Summary: summary, Evidence: evidence.String()}
}

func attestationStatusForVerdict(verdict string) string {
	switch verdict {
	case VerdictOK:
		return statusPassed
	case VerdictWarn:
		return statusWarning
	case VerdictBlock:
		return statusBlocked
	default:
		return verdict
	}
}

func commandPipelineStep(step string, commands []VerifiedCommand, absentStatus, absentSummary string) pipelineStep {
	if len(commands) == 0 {
		return pipelineStep{Icon: "⚪", Step: step, Status: absentStatus, Summary: absentSummary}
	}
	failed := false
	failedCount := 0
	var evidence strings.Builder
	for _, command := range commands {
		icon := "✅"
		if command.Exit != 0 {
			icon, failed = "⚠️", true
			failedCount++
		}
		fmt.Fprintf(&evidence, "- %s `%s` (exit %d)\n", icon, sanitizeText(command.Comando), command.Exit)
	}
	icon := "✅"
	summary := fmt.Sprintf("%d commands, all green", len(commands))
	if failed {
		icon = "⚠️"
		summary = fmt.Sprintf("%d commands, %d failed", len(commands), failedCount)
	}
	// The summary is the collapsed line a reader sees without expanding, so it
	// stays short. Putting the whole command list here as well left truncation
	// nothing to remove: omitted() replaces Evidence, and an oversized copy in
	// Summary would survive it and still push the body over the limit.
	status := statusPassed
	if failed {
		status = statusFailed
	}
	return pipelineStep{Icon: icon, Step: step, Status: status, Summary: summary, Evidence: evidence.String()}
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
