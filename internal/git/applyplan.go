package git

import (
	"errors"
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/intent"
)

// Rejection reasons of `slice apply`. They are sentinel errors so the CLI
// can tell "you still have to approve" apart from "this is no longer
// applicable".
var (
	// ErrTreeChanged: the plan was computed over a different worktree state.
	ErrTreeChanged = errors.New("the worktree changed since the plan was computed")
	// ErrPlanMismatch: the responses approve a different plan.
	ErrPlanMismatch = errors.New("responses do not match this plan")
	// ErrDecisionUnanswered: an explicit answer is missing.
	ErrDecisionUnanswered = errors.New("pending decisions without an explicit answer")
	// ErrDecisionAborted: the user answered abort.
	ErrDecisionAborted = errors.New("the user aborted the fragmentation")
	// ErrLegacyRouteOnlyRename rejects a route-only plan that cannot bind both
	// sides of a detected rename or copy.
	ErrLegacyRouteOnlyRename = errors.New("legacy route-only plans cannot safely apply detected renames or copies")
)

// PlanAnswers is the approval artifact: explicit answers bound to one
// specific plan. It admits no default values and no implicit
// "approve everything".
type PlanAnswers struct {
	PlanID  string            `json:"plan_id"`
	Answers map[string]string `json:"answers"`
}

// ApplyApprovedPlan executes a plan emitted by `slice plan` only if the
// three bindings hold: the tree is the same, the answers belong to this
// plan and every pending decision has an explicit answer. Any failure cuts
// off before creating a single commit.
//
// What this removes is the B9 accident: there is no longer a stdin read that
// can confuse "nobody at the keyboard" with "the human approved". What it
// does NOT promise is to stop a deliberate agent from calling `git commit`
// on its own: that stays outside the threat model.
func ApplyApprovedPlan(plan *SerializedPlan, answers PlanAnswers) ([]CommitResult, error) {
	if err := ValidateApplication(plan, answers); err != nil {
		return nil, err
	}
	executable := deserializePlan(plan)
	return executeSelectionPlan(executable)
}

// ValidateApplication checks the three bindings without touching the
// repository beyond re-reading the tree state. Keeping it apart from
// execution makes it possible to verify an approval without risking any
// commit.
func ValidateApplication(plan *SerializedPlan, answers PlanAnswers) error {
	if err := ValidateSerializedPlan(plan); err != nil {
		return err
	}
	if err := rejectLegacyRouteOnlyRenames(plan); err != nil {
		return err
	}
	var currentState string
	var err error
	if len(plan.Changes) > 0 {
		currentState, err = hashDraftStateForChanges(plan.Changes)
	} else {
		currentState, err = HashWorktreeState(PlanPaths(plan))
	}
	if err != nil {
		return err
	}
	if currentState != plan.WorktreeState {
		return fmt.Errorf("%w: run 'sentinel slice plan' again", ErrTreeChanged)
	}
	if answers.PlanID != plan.PlanID {
		return fmt.Errorf("%w: plan %q was approved and this one is %q", ErrPlanMismatch, answers.PlanID, plan.PlanID)
	}
	for _, decision := range plan.PendingDecisions {
		answer, ok := answers.Answers[decision.ID]
		switch {
		case !ok:
			return fmt.Errorf("%w: missing decision %s (%s)", ErrDecisionUnanswered, decision.ID, decision.File)
		case answer == AnswerAbort:
			return fmt.Errorf("%w: %s", ErrDecisionAborted, decision.File)
		case answer != AnswerBypass:
			return fmt.Errorf("%w: answer %q is not accepted for decision %s", ErrDecisionUnanswered, answer, decision.ID)
		}
	}
	return nil
}

func rejectLegacyRouteOnlyRenames(plan *SerializedPlan) error {
	if len(plan.Changes) > 0 {
		return nil
	}

	routes := make(map[string]struct{})
	for _, batch := range plan.Batches {
		for _, route := range batch.Paths {
			routes[route] = struct{}{}
		}
	}
	if len(routes) == 0 {
		return nil
	}

	changes, err := captureGitChangeRecords()
	if err != nil {
		return fmt.Errorf("could not inspect legacy route-only draft: %w", err)
	}
	for _, change := range changes {
		if change.Status != "R" && change.Status != "C" {
			continue
		}
		if _, approved := routes[change.Path]; approved {
			kind := "rename"
			if change.Status == "C" {
				kind = "copy"
			}
			return fmt.Errorf("%w: approved route %q is the destination of a detected %s", ErrLegacyRouteOnlyRename, change.Path, kind)
		}
	}
	return nil
}

// deserializePlan rebuilds the executable plan from its projection. The
// message is already approved, so it is fixed as final.
func deserializePlan(plan *SerializedPlan) *FragmentationPlan {
	executable := &FragmentationPlan{Batches: make([]PlannedBatch, 0, len(plan.Batches))}
	for _, batch := range plan.Batches {
		selectors := cloneSelectors(batch.Selectors)
		if len(selectors) == 0 {
			selectors = wholeFileSelectors(batch.Paths)
		}
		message := batch.Message
		if plan.Intent != "" || plan.IntentSource != "" {
			message = intent.Append(message, intent.Intent{Text: plan.Intent, Source: plan.IntentSource})
		}
		executable.Batches = append(executable.Batches, PlannedBatch{
			Layer:       batch.Layer,
			Number:      batch.Number,
			Paths:       batch.Paths,
			Selectors:   selectors,
			TotalLines:  batch.Lines,
			Message:     message,
			AutoMessage: message,
			IsOversized: batch.IsOversized,
		})
	}
	executable.Changes = cloneChanges(plan.Changes)
	return executable
}

func wholeFileSelectors(routes []string) []ChangeSelector {
	selectors := make([]ChangeSelector, 0, len(routes))
	for _, route := range routes {
		selectors = append(selectors, ChangeSelector{Path: route, Mode: SelectorWholeFile})
	}
	return selectors
}
