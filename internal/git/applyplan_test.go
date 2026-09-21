package git

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/intent"
)

// preparePlanWithGiant leaves the repo with one normal file and one giant
// one, and returns the emitted plan (with its automatic bypass for the
// indivisible unit).
func preparePlanWithGiant(t *testing.T) *SerializedPlan {
	t.Helper()
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("could not write app.go: %v", err)
	}
	writeGiantFile(t, "giant.go")

	plan, err := BuildPlanForAgent()
	if err != nil {
		t.Fatalf("BuildPlanForAgent failed: %v", err)
	}
	if len(plan.PendingDecisions) != 0 {
		t.Fatalf("the indivisible class is no longer human; got %d pending", len(plan.PendingDecisions))
	}
	if len(plan.AutomaticDecisions) != 1 {
		t.Fatalf("expected 1 automatic decision, got %d", len(plan.AutomaticDecisions))
	}
	if plan.AutomaticDecisions[0].Reason != IndivisibleReason {
		t.Fatalf("reason = %q, expected %q", plan.AutomaticDecisions[0].Reason, IndivisibleReason)
	}
	return plan
}

// injectHumanDecision adds a synthetic pending decision (giant file class)
// to exercise the human answers over an already computed plan.
func injectHumanDecision(t *testing.T, plan *SerializedPlan) string {
	t.Helper()
	id := "human-decision-test"
	plan.PendingDecisions = append(plan.PendingDecisions, PendingDecision{
		ID: id, File: "giant.go", Lines: 600,
		Question: "test", Options: []string{AnswerBypass, AnswerAbort},
	})
	plan.PlanID = calculatePlanID(plan)
	return id
}

func bypassAnswers(plan *SerializedPlan) PlanAnswers {
	answers := map[string]string{}
	for _, d := range plan.PendingDecisions {
		answers[d.ID] = AnswerBypass
	}
	return PlanAnswers{PlanID: plan.PlanID, Answers: answers}
}

// TestApplyApprovedPlanRejectsChangedTree: binding to the tree. A plan
// computed over another state is not executed.
func TestApplyApprovedPlanRejectsChangedTree(t *testing.T) {
	plan := preparePlanWithGiant(t)
	commitsBefore := countCommits(t)

	if err := os.WriteFile("app.go", []byte("package app\n\nfunc Nuevo() {}\n"), 0644); err != nil {
		t.Fatalf("could not modify app.go: %v", err)
	}

	_, err := ApplyApprovedPlan(plan, bypassAnswers(plan))
	if !errors.Is(err, ErrTreeChanged) {
		t.Fatalf("error = %v, expected ErrTreeChanged", err)
	}
	const wantRecoveryError = "the worktree changed since the plan was computed: run 'vcsentinel slice plan' again"
	if err.Error() != wantRecoveryError {
		t.Fatalf("error = %q, want %q", err.Error(), wantRecoveryError)
	}
	if countCommits(t) != commitsBefore {
		t.Error("commits were created despite the rejection")
	}
}

// TestApplyApprovedPlanRejectsAnswersForAnotherPlan: binding to the plan. An
// approval of a previous plan is not valid for a new one.
func TestApplyApprovedPlanRejectsAnswersForAnotherPlan(t *testing.T) {
	plan := preparePlanWithGiant(t)
	commitsBefore := countCommits(t)

	answers := bypassAnswers(plan)
	answers.PlanID = "previous-plan"

	_, err := ApplyApprovedPlan(plan, answers)
	if !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("error = %v, expected ErrPlanMismatch", err)
	}
	if countCommits(t) != commitsBefore {
		t.Error("commits were created despite the rejection")
	}
}

// TestApplyApprovedPlanRequiresExplicitAnswer: no default values. Not even
// "approve everything" is implicit.
func TestApplyApprovedPlanRequiresExplicitAnswer(t *testing.T) {
	plan := preparePlanWithGiant(t)
	injectHumanDecision(t, plan)
	commitsBefore := countCommits(t)

	_, err := ApplyApprovedPlan(plan, PlanAnswers{PlanID: plan.PlanID, Answers: map[string]string{}})
	if !errors.Is(err, ErrDecisionUnanswered) {
		t.Fatalf("error = %v, expected ErrDecisionUnanswered", err)
	}
	if countCommits(t) != commitsBefore {
		t.Error("commits were created despite the rejection")
	}
}

// TestApplyApprovedPlanRespectsAbort: answering "abort" commits nothing.
func TestApplyApprovedPlanRespectsAbort(t *testing.T) {
	plan := preparePlanWithGiant(t)
	commitsBefore := countCommits(t)

	id := injectHumanDecision(t, plan)

	answers := bypassAnswers(plan)
	answers.Answers[id] = AnswerAbort

	_, err := ApplyApprovedPlan(plan, answers)
	if !errors.Is(err, ErrDecisionAborted) {
		t.Fatalf("error = %v, expected ErrDecisionAborted", err)
	}
	if countCommits(t) != commitsBefore {
		t.Error("commits were created despite the abort")
	}
}

// TestApplyApprovedPlanHappyPath: the expected commits, with the plan's
// messages.
func TestApplyApprovedPlanHappyPath(t *testing.T) {
	plan := preparePlanWithGiant(t)

	results, err := ApplyApprovedPlan(plan, bypassAnswers(plan))
	if err != nil {
		t.Fatalf("ApplyApprovedPlan returned error: %v", err)
	}
	if len(results) != len(plan.Batches) {
		t.Fatalf("commits = %d, expected %d", len(results), len(plan.Batches))
	}
	for i, result := range results {
		if result.Message != plan.Batches[i].Message {
			t.Errorf("commit %d: message %q, expected %q", i, result.Message, plan.Batches[i].Message)
		}
		actual, err := runGitOutput("show", "-s", "--format=%B", result.Hash)
		if err != nil {
			t.Fatalf("reading commit %s: %v", result.Hash, err)
		}
		if strings.TrimRight(actual, "\n") != plan.Batches[i].Message {
			t.Errorf("commit %d message = %q, expected %q", i, strings.TrimRight(actual, "\n"), plan.Batches[i].Message)
		}
		if got, err := CommitIntent(result.Hash); err != nil {
			t.Fatalf("CommitIntent(%s) error = %v", result.Hash, err)
		} else if got != (intent.Intent{}) {
			t.Fatalf("no-intent apply wrote trailer %+v", got)
		}
	}
	clean, err := WorktreeClean()
	if err != nil {
		t.Fatalf("WorktreeClean failed: %v", err)
	}
	if !clean {
		t.Error("pending changes remained after applying the full plan")
	}
}

func TestApplyApprovedPlanRemovesForgedReservedTrailersWithoutIntent(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	adapter := &adapterPlanFake{message: "subject\n\nbody\n\nExisting: keep\nSentinel-Intent: forged\nSentinel-Intent-Source: declared"}
	plan, err := BuildPlanForAgentWithAdapter(adapter)
	if err != nil {
		t.Fatalf("BuildPlanForAgentWithAdapter() error: %v", err)
	}
	if strings.Contains(plan.Batches[0].Message, intent.IntentKey) || strings.Contains(plan.Batches[0].Message, intent.SourceKey) {
		t.Fatalf("forged reserved trailers survived plan creation: %q", plan.Batches[0].Message)
	}

	results, err := ApplyApprovedPlan(plan, PlanAnswers{PlanID: plan.PlanID, Answers: map[string]string{}})
	if err != nil {
		t.Fatalf("ApplyApprovedPlan() error: %v", err)
	}
	for _, result := range results {
		message, err := runGitOutput("show", "-s", "--format=%B", result.Hash)
		if err != nil {
			t.Fatalf("reading commit message: %v", err)
		}
		if strings.Contains(message, intent.IntentKey) || strings.Contains(message, intent.SourceKey) {
			t.Fatalf("forged reserved trailers survived no-intent apply: %q", message)
		}
		if !strings.Contains(message, "Existing: keep") || !strings.Contains(message, "body") {
			t.Fatalf("apply did not preserve body and other trailers: %q", message)
		}
	}
}

func TestApplyApprovedPlanRoundTripsIntentTrailers(t *testing.T) {
	for _, source := range []string{intent.SourceDeclared, intent.SourceConversation} {
		t.Run(source, func(t *testing.T) {
			prepareTempRepo(t)
			commitInRepo(t, "base.txt", "base\n")
			if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
				t.Fatal(err)
			}
			plan, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{
				Intent:       "protect the release",
				IntentSource: source,
			})
			if err != nil {
				t.Fatalf("BuildPlanForAgentWithOptions() error = %v", err)
			}

			results, err := ApplyApprovedPlan(plan, PlanAnswers{PlanID: plan.PlanID, Answers: map[string]string{}})
			if err != nil {
				t.Fatalf("ApplyApprovedPlan() error = %v", err)
			}
			for _, result := range results {
				got, err := CommitIntent(result.Hash)
				if err != nil {
					t.Fatalf("CommitIntent(%s) error = %v", result.Hash, err)
				}
				want := intent.Intent{Text: "protect the release", Source: source}
				if got != want {
					t.Fatalf("CommitIntent(%s) = %+v, want %+v", result.Hash, got, want)
				}
			}
		})
	}
}

func TestApplyApprovedPlanRestoresIntentForLegacySerializedPlan(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{
		Intent:       "protect the release",
		IntentSource: intent.SourceConversation,
	})
	if err != nil {
		t.Fatalf("BuildPlanForAgentWithOptions() error = %v", err)
	}
	legacyMessage := "feat: legacy serialized plan"
	for i := range plan.Batches {
		plan.Batches[i].Message = legacyMessage
		if got := intent.Parse(plan.Batches[i].Message); got != (intent.Intent{}) {
			t.Fatalf("legacy batch %d unexpectedly has intent: %+v", i, got)
		}
	}
	plan.PlanID = calculatePlanID(plan)

	results, err := ApplyApprovedPlan(plan, PlanAnswers{PlanID: plan.PlanID, Answers: map[string]string{}})
	if err != nil {
		t.Fatalf("ApplyApprovedPlan() error = %v", err)
	}
	wantIntent := intent.Intent{Text: plan.Intent, Source: plan.IntentSource}
	wantMessage := intent.Append(legacyMessage, wantIntent)
	for i, result := range results {
		if result.Message != wantMessage {
			t.Errorf("commit %d message = %q, want %q", i, result.Message, wantMessage)
		}
		actual, err := runGitOutput("show", "-s", "--format=%B", result.Hash)
		if err != nil {
			t.Fatalf("reading commit %s: %v", result.Hash, err)
		}
		if strings.TrimRight(actual, "\n") != wantMessage {
			t.Errorf("commit %d message = %q, want %q", i, strings.TrimRight(actual, "\n"), wantMessage)
		}
		got, err := CommitIntent(result.Hash)
		if err != nil {
			t.Fatalf("CommitIntent(%s) error = %v", result.Hash, err)
		}
		if got != wantIntent {
			t.Errorf("CommitIntent(%s) = %+v, want %+v", result.Hash, got, wantIntent)
		}
	}
}

func TestValidateApplicationRejectsMalformedIntentPair(t *testing.T) {
	cases := []struct {
		name   string
		intent string
		source string
	}{
		{name: "invalid source", intent: "protect the release", source: "unknown"},
		{name: "text only", intent: "protect the release"},
		{name: "source only", source: intent.SourceDeclared},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			plan := preparePlanWithGiant(t)
			plan.Intent = tt.intent
			plan.IntentSource = tt.source
			if err := ValidateApplication(plan, PlanAnswers{PlanID: plan.PlanID, Answers: map[string]string{}}); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("ValidateApplication() error = %v, want ErrInvalidPlan", err)
			}
		})
	}
}
