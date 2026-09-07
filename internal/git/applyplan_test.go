package git

import (
	"errors"
	"os"
	"testing"
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
	}
	clean, err := WorktreeClean()
	if err != nil {
		t.Fatalf("WorktreeClean failed: %v", err)
	}
	if !clean {
		t.Error("pending changes remained after applying the full plan")
	}
}
