package git

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/intent"
)

type adapterPlanFake struct {
	message string
	err     error
	diff    string
}

func (a *adapterPlanFake) GetCommitMessage([]string, string, int) (string, error) {
	return a.message, a.err
}

func (a *adapterPlanFake) GetCommitMessageWithDiff(_ []string, _ string, _ int, diff string) (string, error) {
	a.diff = diff
	return a.message, a.err
}

// writeGiantFile creates a code file with more lines than GiantCodeLimit to
// force the pending-decision branch.
func writeGiantFile(t *testing.T, name string) {
	t.Helper()
	content := strings.Repeat("// filler line\n", GiantCodeLimit+50)
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatalf("could not write %s: %v", name, err)
	}
}

// TestBuildPlanForAgentRecordsDecisionWithoutCommitting covers the T0.9
// acceptance: with a giant code file, the plan exposes the pending decision
// and creates no commit.
func TestBuildPlanForAgentRecordsDecisionWithoutCommitting(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	commitsBefore := countCommits(t)

	writeGiantFile(t, "giant.go")

	plan, err := BuildPlanForAgent()
	if err != nil {
		t.Fatalf("BuildPlanForAgent returned error: %v", err)
	}
	if len(plan.PendingDecisions) != 0 {
		t.Fatalf("the indivisible class no longer asks; got %d pending", len(plan.PendingDecisions))
	}
	if len(plan.AutomaticDecisions) != 1 {
		t.Fatalf("expected 1 automatic decision, got %d", len(plan.AutomaticDecisions))
	}
	automatic := plan.AutomaticDecisions[0]
	if automatic.File != "giant.go" {
		t.Errorf("decision file = %q, expected giant.go", automatic.File)
	}
	if automatic.ID == "" {
		t.Error("the automatic decision has no id")
	}
	if automatic.Reason != IndivisibleReason {
		t.Errorf("reason = %q, expected %q", automatic.Reason, IndivisibleReason)
	}
	if commitsBefore != countCommits(t) {
		t.Error("BuildPlanForAgent created commits: it must propose without executing")
	}
}

// TestBuildPlanForAgentIsIdempotent covers the second T0.9 acceptance: two
// consecutive runs over the same tree give the same plan_id and the same
// worktree_state.
func TestBuildPlanForAgentIsIdempotent(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("could not write app.go: %v", err)
	}

	first, err := BuildPlanForAgent()
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	second, err := BuildPlanForAgent()
	if err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if first.PlanID == "" {
		t.Fatal("empty plan_id")
	}
	if first.PlanID != second.PlanID {
		t.Errorf("plan_id not deterministic: %q vs %q", first.PlanID, second.PlanID)
	}
	if first.WorktreeState != second.WorktreeState {
		t.Errorf("worktree_state not deterministic: %q vs %q", first.WorktreeState, second.WorktreeState)
	}
}

func TestBuildPlanForAgentGeneratesMessagesWithStableFallback(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	commitInRepo(t, "app.go", "package app\n")
	if err := os.WriteFile("app.go", []byte("package app\n\nfunc nueva() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	successful := &adapterPlanFake{message: "feat(slice): describe batch"}
	semantic, err := BuildPlanForAgentWithAdapter(successful)
	if err != nil {
		t.Fatalf("semantic plan failed: %v", err)
	}
	failed, err := BuildPlanForAgentWithAdapter(&adapterPlanFake{err: errors.New("agent down")})
	if err != nil {
		t.Fatalf("plan with fallback failed: %v", err)
	}
	if semantic.Batches[0].Message != "feat(slice): describe batch" {
		t.Fatalf("semantic message = %q", semantic.Batches[0].Message)
	}
	if !strings.Contains(successful.diff, "+func nueva()") {
		t.Fatalf("the adapter did not receive the micro-diff: %q", successful.diff)
	}
	if failed.Batches[0].Message != "chore(slice): auto-fragmented backend batch #1" {
		t.Fatalf("fallback = %q", failed.Batches[0].Message)
	}
	if semantic.PlanID != failed.PlanID {
		t.Fatalf("PlanID depends on the message: %q != %q", semantic.PlanID, failed.PlanID)
	}
}

// TestWorktreeStateChangesWithContent protects the binding to the tree that
// T0.10 will use to refuse to apply a plan computed over a different state.
func TestWorktreeStateChangesWithContent(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatalf("could not write app.go: %v", err)
	}
	before, err := HashWorktreeState([]string{"app.go"})
	if err != nil {
		t.Fatalf("HashWorktreeState failed: %v", err)
	}
	if err := os.WriteFile("app.go", []byte("package app\n\nfunc Nuevo() {}\n"), 0644); err != nil {
		t.Fatalf("could not rewrite app.go: %v", err)
	}
	after, err := HashWorktreeState([]string{"app.go"})
	if err != nil {
		t.Fatalf("HashWorktreeState failed: %v", err)
	}
	if before == after {
		t.Error("the tree hash did not change after modifying the content of a file")
	}
}

func countCommits(t *testing.T) string {
	t.Helper()
	output, err := runGitOutput("rev-list", "--count", "HEAD")
	if err != nil {
		t.Fatalf("could not count commits: %v", err)
	}
	return strings.TrimSpace(output)
}

func TestPlanIDIncludesIntentAndSource(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}

	declared, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{
		Intent:       "protect the release",
		IntentSource: intent.SourceDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{
		Intent:       "summarize the release",
		IntentSource: intent.SourceConversation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if declared.Intent != "protect the release" || declared.IntentSource != intent.SourceDeclared {
		t.Fatalf("declared plan intent = %+v", declared)
	}
	if declared.PlanID == conversation.PlanID {
		t.Fatal("different plan intent produced the same PlanID")
	}
}

func TestPlanWarningsAreDisplayMetadataOnly(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{
		Intent:       "protect the release",
		IntentSource: intent.SourceDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	originalID := plan.PlanID
	plan.Warnings = []string{"Transcript was sent under consent."}
	if plan.PlanID != originalID {
		t.Fatalf("display warnings changed PlanID from %q to %q", originalID, plan.PlanID)
	}
	if err := ValidateSerializedPlan(plan); err != nil {
		t.Fatalf("warnings made an otherwise valid plan invalid: %v", err)
	}
}

func TestValidateApplicationRejectsAlteredIntentAndNoIntentAnswers(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	if err := os.WriteFile("app.go", []byte("package app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{
		Intent:       "protect the release",
		IntentSource: intent.SourceDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	originalID := plan.PlanID
	plan.Intent = "altered intent"
	if err := ValidateApplication(plan, PlanAnswers{PlanID: originalID, Answers: map[string]string{}}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("altered intent error = %v, want ErrInvalidPlan", err)
	}

	noIntent, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	withIntent, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{
		Intent:       "protect the release",
		IntentSource: intent.SourceDeclared,
	})
	if err != nil {
		t.Fatal(err)
	}
	if noIntent.PlanID == withIntent.PlanID {
		t.Fatal("no-intent and intent plans unexpectedly share a PlanID")
	}
	if err := ValidateApplication(withIntent, PlanAnswers{PlanID: noIntent.PlanID, Answers: map[string]string{}}); !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("no-intent answer error = %v, want ErrPlanMismatch", err)
	}
}
