package review

import (
	"strings"
	"testing"
)

func bulkSteps() []pipelineStep {
	return []pipelineStep{
		{Icon: "✅", Step: "review", Summary: "reviewed", Evidence: strings.Repeat("review evidence ", 300)},
		{Icon: "✅", Step: "test", Summary: "tested", Evidence: strings.Repeat("test evidence ", 300)},
		{Icon: "⚪", Step: "ci", Summary: "not observed by Sentinel", Evidence: "CI placeholder", CI: true},
	}
}

func TestTruncatePipelineKeepsEveryStepAndDropsEvidenceFromTheEnd(t *testing.T) {
	fixed := "## Intent\nintent\n## Pipeline\n"
	pipeline, omitted, err := truncatePipeline(fixed, bulkSteps(), 2500)
	if err != nil {
		t.Fatalf("truncatePipeline() error = %v", err)
	}
	if omitted == 0 {
		t.Fatal("truncatePipeline() omitted nothing on an oversized body")
	}
	if len(fixed)+len(pipeline)+CIStepReserveBytes > 2500 {
		t.Fatalf("result is %d bytes plus the reserve, over the 2500 limit", len(fixed)+len(pipeline))
	}
	for _, step := range []string{"<b>review</b>", "<b>test</b>", "<b>ci</b>"} {
		if !strings.Contains(pipeline, step) {
			t.Fatalf("step %s disappeared instead of losing its evidence:\n%s", step, pipeline)
		}
	}
	if strings.Count(pipeline, "evidence blocks omitted for size") != 1 {
		t.Fatalf("omission must be disclosed exactly once:\n%s", pipeline)
	}
	if strings.Contains(pipeline, "test evidence test evidence") {
		t.Fatal("the last step kept its evidence; dropping starts from the end")
	}
}

// The ci step is what pr create replaces in place, so it must survive
// truncation and keep its reserve however little room is left.
func TestTruncatePipelineNeverDropsTheCIStepEvidence(t *testing.T) {
	steps := []pipelineStep{
		{Icon: "✅", Step: "review", Summary: "reviewed", Evidence: strings.Repeat("x", 4000)},
		{Icon: "⚪", Step: "ci", Summary: "not observed", Evidence: strings.Repeat("c", 500), CI: true},
	}
	pipeline, _, err := truncatePipeline("", steps, 3000)
	if err != nil {
		t.Fatalf("truncatePipeline() error = %v", err)
	}
	if !strings.Contains(pipeline, strings.Repeat("c", 500)) {
		t.Fatal("the ci evidence was dropped")
	}
}

// A step whose own evidence happens to contain the ci marker is not the ci
// step. The old parser decided this by substring search over the serialised
// block and got it wrong.
func TestTruncatePipelineDoesNotMistakeTextForTheCIStep(t *testing.T) {
	steps := []pipelineStep{
		{Icon: "✅", Step: "review", Summary: "reviewed", Evidence: "the log mentions <b>ci</b> " + strings.Repeat("x", 4000)},
		{Icon: "⚪", Step: "ci", Summary: "not observed", Evidence: "placeholder", CI: true},
	}
	pipeline, omitted, err := truncatePipeline("", steps, 2000)
	if err != nil {
		t.Fatalf("truncatePipeline() error = %v", err)
	}
	if omitted != 1 {
		t.Fatalf("omitted = %d, want 1: the review step must be truncatable despite its text", omitted)
	}
	if strings.Contains(pipeline, strings.Repeat("x", 4000)) {
		t.Fatal("the review evidence survived because its text was read as the ci marker")
	}
}

// Evidence containing </details>, or nested collapsible markup, used to end a
// block early and leave its remainder behind. Truncating over data cannot.
func TestTruncatePipelineHandlesNestedMarkupInEvidence(t *testing.T) {
	nested := "<details><summary>inner</summary>\n" + strings.Repeat("n", 3000) + "\n</details>\n"
	steps := []pipelineStep{
		{Icon: "✅", Step: "review", Summary: "reviewed", Evidence: nested},
		{Icon: "⚪", Step: "ci", Summary: "not observed", Evidence: "placeholder", CI: true},
	}
	pipeline, omitted, err := truncatePipeline("", steps, 2000)
	if err != nil {
		t.Fatalf("truncatePipeline() error = %v", err)
	}
	if omitted != 1 {
		t.Fatalf("omitted = %d, want 1", omitted)
	}
	if strings.Contains(pipeline, strings.Repeat("n", 3000)) {
		t.Fatal("nested evidence was left behind")
	}
	if strings.Count(pipeline, "<details>") != strings.Count(pipeline, "</details>") {
		t.Fatalf("markup left unbalanced:\n%s", pipeline)
	}
}

// Evidence that literally contains the omission notice must not confuse the
// count, and must not delete anything around it.
func TestTruncatePipelineIsNotConfusedByEvidenceQuotingTheNotice(t *testing.T) {
	quoted := "a log quoting _1 evidence blocks omitted for size; the full logs are under .vas_sentinel/evidence/._\nkeep me\n"
	steps := []pipelineStep{
		{Icon: "✅", Step: "review", Summary: "reviewed", Evidence: quoted},
		{Icon: "✅", Step: "test", Summary: "tested", Evidence: strings.Repeat("t", 4000)},
		{Icon: "⚪", Step: "ci", Summary: "not observed", Evidence: "placeholder", CI: true},
	}
	pipeline, omitted, err := truncatePipeline("", steps, 2500)
	if err != nil {
		t.Fatalf("truncatePipeline() error = %v", err)
	}
	if omitted != 1 {
		t.Fatalf("omitted = %d, want 1", omitted)
	}
	if !strings.Contains(pipeline, "keep me") {
		t.Fatal("content around the quoted notice was deleted")
	}
}

// Sections 1-3 are never truncated. If they alone do not fit, that is an
// error, not something to trim.
func TestTruncatePipelineRefusesWhenTheFixedSectionsDoNotFit(t *testing.T) {
	fixed := strings.Repeat("f", 5000)
	if _, _, err := truncatePipeline(fixed, bulkSteps(), 2000); err == nil {
		t.Fatal("truncatePipeline() accepted a body whose fixed sections exceed the limit")
	}
}

// pr create replaces the ci block, so the placeholder does not survive into
// the published body. Counting both the placeholder and its reserve rejected
// bodies that fit perfectly well once expanded.
func TestTruncatePipelineCountsTheCIReserveInsteadOfThePlaceholder(t *testing.T) {
	placeholder := strings.Repeat("p", 900)
	steps := []pipelineStep{
		{Icon: "✅", Step: "review", Summary: "reviewed", Evidence: strings.Repeat("r", 400)},
		{Icon: "⚪", Step: "ci", Summary: "not observed", Evidence: placeholder, CI: true},
	}
	// Room for the fixed part, the review step and the reserve, but NOT for
	// the reserve plus the placeholder on top of it.
	limit := len(renderPipelineSteps(steps[:1])) + CIStepReserveBytes + 200
	_, omitted, err := truncatePipeline("", steps, limit)
	if err != nil {
		t.Fatalf("truncatePipeline() error = %v", err)
	}
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0: the body fits once the ci placeholder is replaced", omitted)
	}
}

// A step's collapsed summary must not carry a second copy of its evidence:
// omitted() replaces Evidence, so anything duplicated in Summary survives
// truncation and can still push the body over the limit.
func TestCommandPipelineStepKeepsItsSummaryShort(t *testing.T) {
	commands := make([]VerifiedCommand, 0, 40)
	for i := 0; i < 40; i++ {
		commands = append(commands, VerifiedCommand{Comando: strings.Repeat("c", 60), Exit: 0})
	}
	step := commandPipelineStep("test", commands, "not_configured", "not configured")
	if len(step.Summary) > 120 {
		t.Fatalf("summary is %d bytes; it must stay a short collapsed line, not a copy of the evidence", len(step.Summary))
	}
	if strings.Contains(step.Summary, strings.Repeat("c", 60)) {
		t.Fatal("summary duplicates the command list that lives in Evidence")
	}
}
