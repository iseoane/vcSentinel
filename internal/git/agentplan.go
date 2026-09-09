package git

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/intent"
)

// Answers accepted for a pending plan decision. There is no default value:
// T0.10 requires an explicit answer for every decision.
const (
	AnswerBypass = "bypass"
	AnswerAbort  = "abort"
)

// PendingDecision is a question that interactive mode would ask over stdin
// and that plan mode records instead, so an orchestrator can relay it to the
// user. The decision still belongs to the human: recording is not approving.
type PendingDecision struct {
	ID       string   `json:"id"`
	File     string   `json:"file"`
	Lines    int      `json:"lines"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

// AutomaticDecision records a bypass the plan grants by policy without
// relaying it to the human: the unit is indivisible by diff atoms, so no
// safer split than asking exists. It is announced in the plan and in every
// apply so the exemption is never silent.
type AutomaticDecision struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	Lines  int    `json:"lines"`
	Reason string `json:"reason"`
}

// IndivisibleReason is the only automatic bypass reason at present.
const IndivisibleReason = "unit not divisible by diff atoms"

// SerializedBatch is the stable projection of a planned batch in the emitted
// plan: only what an external consumer needs in order to review it.
type SerializedBatch struct {
	Number      int              `json:"number"`
	Layer       string           `json:"layer"`
	Paths       []string         `json:"paths"`
	Selectors   []ChangeSelector `json:"selectors,omitempty"`
	Lines       int              `json:"lines"`
	Message     string           `json:"message"`
	IsOversized bool             `json:"is_oversized"`
}

// SerializedPlan is the full plan emitted by `sentinel slice plan`. It
// commits nothing and is idempotent: over the same tree it produces the same
// PlanID and the same WorktreeState.
type SerializedPlan struct {
	PlanID             string              `json:"plan_id"`
	WorktreeState      string              `json:"worktree_state"`
	Intent             string              `json:"intent,omitempty"`
	IntentSource       intent.Source       `json:"intent_source,omitempty"`
	Warnings           []string            `json:"warnings,omitempty"`
	Batches            []SerializedBatch   `json:"batches"`
	Changes            []PlannedChange     `json:"changes,omitempty"`
	PendingDecisions   []PendingDecision   `json:"pending_decisions"`
	AutomaticDecisions []AutomaticDecision `json:"automatic_decisions"`
	Explanation        string              `json:"explanation,omitempty"`
}

// decisionRecorder implements the second path of the decision callback:
// instead of asking over stdin, it records the question and lets the
// construction continue so the full plan can be emitted. Returning true here
// does NOT approve anything: the batch stays marked as oversized and `slice
// apply` refuses while the decision has no explicit answer.
type decisionRecorder struct {
	pending    []PendingDecision
	automatics []AutomaticDecision
}

func (r *decisionRecorder) semanticCallback(unit SemanticOversizedUnit) (bool, error) {
	// Indivisible by exact diff atoms: asking a human to choose between two
	// identical outcomes (bypass the only possible slice, or abort) adds a
	// roundtrip without adding a decision. The plan grants the bypass itself
	// and records it as an automatic decision so apply announces it.
	r.automatics = append(r.automatics, AutomaticDecision{
		ID:     unit.ID,
		File:   strings.Join(unit.Paths, ", "),
		Lines:  unit.AddedLines,
		Reason: IndivisibleReason,
	})
	return true, nil
}

func (r *decisionRecorder) callback(f ModifiedFile) (bool, error) {
	r.pending = append(r.pending, PendingDecision{
		ID:    DecisionID(f.Path),
		File:  f.Path,
		Lines: f.Lines,
		Question: fmt.Sprintf(
			"%s has %d lines and exceeds the suggested maximum of %d. Slice it as-is (bypass) or abort?",
			f.Path, f.Lines, GiantCodeLimit),
		Options: []string{AnswerBypass, AnswerAbort},
	})
	return true, nil
}

// BuildPlanForAgent computes the fragmentation plan and emits it without
// creating any commit or reading stdin. It is safe to run as many times as
// needed.
func BuildPlanForAgent() (*SerializedPlan, error) {
	return BuildPlanForAgentWithAdapter(nil)
}

// CommitMessageGenerator is the message-generation slice of the agent
// adapter: trying to generate semantic messages before serializing; a
// missing or failing adapter preserves the plan fallback. The method name
// mirrors agentadapter.AgentAdapter until that package is translated.
type CommitMessageGenerator interface {
	GetCommitMessage(paths []string, layer string, batchNum int) (string, error)
}

func BuildPlanForAgentWithAdapter(adapter CommitMessageGenerator) (*SerializedPlan, error) {
	return BuildPlanForAgentWithOptions(adapter, SemanticSliceOptions{})
}

// BuildPlanForAgentWithOptions exposes validated ticket boundaries and
// optional proposal input without changing the non-committing contract.
func BuildPlanForAgentWithOptions(adapter CommitMessageGenerator, options SemanticSliceOptions) (*SerializedPlan, error) {
	normalizedIntent, err := intent.Normalize(options.Intent, options.IntentSource)
	if err != nil {
		return nil, invalidPlan("plan intent: %v", err)
	}
	options.Intent = normalizedIntent.Text
	options.IntentSource = normalizedIntent.Source

	changes, err := CaptureDraftChanges()
	if err != nil {
		return nil, err
	}
	recorder := &decisionRecorder{}
	if options.ConfirmOversized == nil {
		options.ConfirmOversized = recorder.semanticCallback
	}
	plan, err := BuildSemanticSlicePlan(changes, options)
	if err != nil {
		return nil, err
	}
	if adapter != nil {
		GenerateBatchMessages(plan, adapter)
	}
	plan.Changes = changes
	serialized := serializePlanWithAutomatics(plan, recorder.pending, recorder.automatics, "")
	serialized.Intent = options.Intent
	serialized.IntentSource = options.IntentSource
	serialized.WorktreeState = hashPlannedChangesState(changes)
	if err := RecalculatePlanID(serialized); err != nil {
		return nil, err
	}
	return serialized, nil
}

func filesForPlan(changes []PlannedChange) []ModifiedFile {
	files := make([]ModifiedFile, 0, len(changes))
	for _, change := range changes {
		files = append(files, ModifiedFile{
			Path:  change.Path,
			Lines: change.AddedLines,
			Layer: ClassifyLayer(change.Path),
		})
	}
	return files
}

func assignFileSelectors(plan *FragmentationPlan) {
	for i := range plan.Batches {
		batch := &plan.Batches[i]
		if len(batch.Selectors) > 0 {
			continue
		}
		for _, path := range batch.Paths {
			selector := ChangeSelector{Path: path, Mode: SelectorWholeFile}
			for _, change := range plan.Changes {
				if change.Path == path {
					selector.Path = change.Path
					selector.OldPath = change.OldPath
					break
				}
			}
			batch.Selectors = append(batch.Selectors, selector)
		}
	}
}

// PlanPaths returns, sorted, all the paths the plan would commit.
func PlanPaths(plan *SerializedPlan) []string {
	var paths []string
	for _, batch := range plan.Batches {
		if len(batch.Selectors) == 0 {
			paths = append(paths, batch.Paths...)
			continue
		}
		for _, selector := range batch.Selectors {
			paths = append(paths, selector.Path)
			if selector.OldPath != "" {
				paths = append(paths, selector.OldPath)
			}
		}
	}
	return sortedUnique(paths)
}

// SerializePlan projects the plan and computes its PlanID from the batches.
func SerializePlan(plan *FragmentationPlan, pending []PendingDecision, state string) *SerializedPlan {
	return serializePlanWithAutomatics(plan, pending, nil, state)
}

func serializePlanWithAutomatics(plan *FragmentationPlan, pending []PendingDecision, automatics []AutomaticDecision, state string) *SerializedPlan {
	batches := make([]SerializedBatch, 0, len(plan.Batches))
	for _, batch := range plan.Batches {
		message := batch.Message
		if strings.TrimSpace(message) == "" {
			message = batch.AutoMessage
		}
		batches = append(batches, SerializedBatch{
			Number:      batch.Number,
			Layer:       batch.Layer,
			Paths:       append([]string(nil), batch.Paths...),
			Selectors:   cloneSelectors(batch.Selectors),
			Lines:       batch.TotalLines,
			Message:     message,
			IsOversized: batch.IsOversized,
		})
	}
	serialized := &SerializedPlan{
		WorktreeState:      state,
		Batches:            batches,
		Changes:            cloneChanges(plan.Changes),
		PendingDecisions:   append([]PendingDecision(nil), pending...),
		AutomaticDecisions: append([]AutomaticDecision(nil), automatics...),
		Explanation:        plan.Explanation,
	}
	assignSerializedSelectors(serialized)
	if pending == nil {
		serialized.PendingDecisions = []PendingDecision{}
	}
	if automatics == nil {
		serialized.AutomaticDecisions = []AutomaticDecision{}
	}
	serialized.PlanID = calculatePlanID(serialized)
	return serialized
}

func assignSerializedSelectors(plan *SerializedPlan) {
	changes := make(map[string]PlannedChange, len(plan.Changes))
	for _, change := range plan.Changes {
		changes[changeKey(change.Path, change.OldPath)] = change
	}
	for i := range plan.Batches {
		batch := &plan.Batches[i]
		if len(batch.Selectors) > 0 {
			continue
		}
		for _, path := range batch.Paths {
			selector := ChangeSelector{Path: path, Mode: SelectorWholeFile}
			if change, ok := changes[changeKey(path, "")]; ok {
				selector.Path = change.Path
				selector.OldPath = change.OldPath
			} else {
				for _, change := range plan.Changes {
					if change.Path == path {
						selector.OldPath = change.OldPath
						break
					}
				}
			}
			batch.Selectors = append(batch.Selectors, selector)
		}
	}
}

// DecisionID identifies a pending decision by the file that causes it,
// stable across runs over the same tree.
func DecisionID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

// HashWorktreeState freezes the exact state of the paths the plan would
// commit: their git status line and the hash of their content. T0.10 uses it
// to refuse to apply a plan computed over a different state.
//
// It is bound to the plan's paths and not to the whole worktree on purpose:
// the flow itself writes plan.json and the answers file, and with a global
// hash any approval artifact would invalidate the very plan it approves. The
// guarantee still holds, because apply only commits the plan's paths and
// each one is verified; a new file outside the plan is not committed and
// therefore does not invalidate it.
func HashWorktreeState(paths []string) (string, error) {
	identity := make([]map[string]string, 0, len(paths))
	for _, path := range sortedUnique(paths) {
		status, err := runGitOutput("status", "--porcelain", "-z", "-uall", "--", literalPathspec(path))
		if err != nil {
			return "", err
		}
		head, err := hashHeadPath(path)
		if err != nil {
			return "", err
		}
		index, err := hashIndexPath(path)
		if err != nil {
			return "", err
		}
		worktree, err := hashWorktreePath(path)
		if err != nil {
			return "", err
		}
		identity = append(identity, map[string]string{
			"path":     path,
			"status":   strings.TrimSpace(status),
			"head":     head,
			"index":    index,
			"worktree": worktree,
		})
	}
	encoded := mustMarshal(identity)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
