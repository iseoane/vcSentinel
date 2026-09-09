package git

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/ISeoane-Quental/vas.sentinel/internal/intent"
)

// SelectorMode describes the smallest safe unit that a slice plan can select.
type SelectorMode string

const (
	SelectorWholeFile SelectorMode = "whole_file"
	SelectorHunk      SelectorMode = "hunk"
)

// ChangeKind describes the representation available for a planned change.
type ChangeKind string

const (
	ChangeText   ChangeKind = "text"
	ChangeBinary ChangeKind = "binary"
	ChangeRename ChangeKind = "rename"
	ChangeFile   ChangeKind = "file"
)

const (
	changeAtomHunk   = "hunk"
	changeAtomFile   = "file"
	changeAtomBinary = "binary"
	changeAtomRename = "rename"
)

// DiffHunk is the stable, content-bound identity of one unified diff hunk.
// Ranges use the numbering of the old and new file contents, and PatchHash
// binds the range to the actual changed lines rather than just their location.
type DiffHunk struct {
	OldStart  int    `json:"old_start"`
	OldLines  int    `json:"old_lines"`
	NewStart  int    `json:"new_start"`
	NewLines  int    `json:"new_lines"`
	PatchHash string `json:"patch_hash"`
}

// ChangeAtom is one indivisible change in the draft. Text hunks are atoms when
// Git can represent them precisely. Binary, rename, mode-only, and empty-file
// changes use one file atom and therefore remain whole-file-only.
type ChangeAtom struct {
	ID         string    `json:"id"`
	Index      int       `json:"index"`
	Kind       string    `json:"kind"`
	Hunk       *DiffHunk `json:"hunk,omitempty"`
	Digest     string    `json:"digest"`
	AddedLines int       `json:"added_lines"`
}

// PlannedChange is the complete change draft captured by slice plan. It keeps
// the old path for renames and both index and worktree content fingerprints so
// a later apply cannot mistake a different Git state for the planned one.
type PlannedChange struct {
	Path         string       `json:"path"`
	OldPath      string       `json:"old_path,omitempty"`
	Status       string       `json:"status"`
	Kind         ChangeKind   `json:"kind"`
	AddedLines   int          `json:"added_lines"`
	HeadHash     string       `json:"head_hash"`
	IndexHash    string       `json:"index_hash"`
	WorktreeHash string       `json:"worktree_hash"`
	Atoms        []ChangeAtom `json:"atoms"`
}

// ChangeSelector selects either every atom in a file or one exact text hunk.
// A hunk selector repeats the hunk metadata intentionally: validation can
// reject a corrupt selector without trusting an opaque atom ID alone.
type ChangeSelector struct {
	Path      string       `json:"path"`
	OldPath   string       `json:"old_path,omitempty"`
	Mode      SelectorMode `json:"mode"`
	AtomID    string       `json:"atom_id,omitempty"`
	HunkIndex int          `json:"hunk_index"`
	Hunk      *DiffHunk    `json:"hunk,omitempty"`
}

var (
	// ErrInvalidPlan identifies malformed, incomplete, overlapping, or corrupt
	// selection data before any history mutation is attempted.
	ErrInvalidPlan = errors.New("invalid slice plan")
)

type planIdentity struct {
	State        string            `json:"state"`
	Intent       string            `json:"intent,omitempty"`
	IntentSource intent.Source     `json:"intent_source,omitempty"`
	Batches      []batchIdentity   `json:"batches"`
	Changes      []PlannedChange   `json:"changes,omitempty"`
	Decisions    []PendingDecision `json:"decisions,omitempty"`
}

type batchIdentity struct {
	Number      int              `json:"number"`
	Layer       string           `json:"layer"`
	Paths       []string         `json:"paths"`
	Lines       int              `json:"lines"`
	IsOversized bool             `json:"is_oversized"`
	Selectors   []ChangeSelector `json:"selectors"`
}

// ValidatePlanSelections proves that every captured change atom is selected
// exactly once. Plans without a captured draft retain route-only compatibility,
// but they may only contain whole-file selectors.
func ValidatePlanSelections(plan *SerializedPlan) error {
	if plan == nil {
		return invalidPlan("plan is nil")
	}
	if len(plan.Changes) == 0 {
		return validateLegacyWholeFilePlan(plan)
	}

	changes, byKey, err := indexPlannedChanges(plan.Changes)
	if err != nil {
		return err
	}
	if len(plan.Batches) == 0 {
		return invalidPlan("captured changes are not covered by any batch")
	}

	selected := make(map[string]int)
	batchNumbers := make(map[int]struct{}, len(plan.Batches))
	for i, batch := range plan.Batches {
		if batch.Number <= 0 {
			return invalidPlan("batch %d has an invalid number", i)
		}
		if _, exists := batchNumbers[batch.Number]; exists {
			return invalidPlan("batch number %d is repeated", batch.Number)
		}
		batchNumbers[batch.Number] = struct{}{}
		if len(batch.Paths) == 0 {
			return invalidPlan("batch %d has no routes", batch.Number)
		}
		if len(batch.Selectors) == 0 {
			return invalidPlan("batch %d has no selectors", batch.Number)
		}

		routeSet := make(map[string]struct{}, len(batch.Paths))
		for _, path := range batch.Paths {
			normalized, err := validatePlanPath(path)
			if err != nil {
				return invalidPlan("batch %d: %v", batch.Number, err)
			}
			if _, exists := routeSet[normalized]; exists {
				return invalidPlan("batch %d repeats route %q", batch.Number, path)
			}
			routeSet[normalized] = struct{}{}
		}

		selectedLines := 0
		selectorRoutes := make(map[string]struct{}, len(batch.Selectors))
		for selectorIndex, selector := range batch.Selectors {
			changeKey := changeKey(selector.Path, selector.OldPath)
			change, ok := byKey[changeKey]
			if !ok {
				return invalidPlan("batch %d selector %d references unknown change %q", batch.Number, selectorIndex, selector.Path)
			}
			if _, ok := routeSet[change.Path]; !ok {
				return invalidPlan("batch %d selector %d is missing from routes", batch.Number, selectorIndex)
			}
			selectorRoutes[change.Path] = struct{}{}

			switch selector.Mode {
			case SelectorWholeFile:
				if selector.Hunk != nil || selector.AtomID != "" {
					return invalidPlan("batch %d selector %d has hunk data for a whole-file selection", batch.Number, selectorIndex)
				}
				for _, atom := range change.Atoms {
					key := atomSelectionKey(changeKey, atom.ID)
					selected[key]++
					selectedLines += atom.AddedLines
				}
			case SelectorHunk:
				if change.Kind != ChangeText {
					return invalidPlan("batch %d selector %d cannot select a hunk from %s change %q", batch.Number, selectorIndex, change.Kind, change.Path)
				}
				if selector.Hunk == nil {
					return invalidPlan("batch %d selector %d has no hunk metadata", batch.Number, selectorIndex)
				}
				if selector.HunkIndex < 0 || selector.HunkIndex >= len(change.Atoms) {
					return invalidPlan("batch %d selector %d has hunk index %d outside the draft", batch.Number, selectorIndex, selector.HunkIndex)
				}
				atom := change.Atoms[selector.HunkIndex]
				if atom.Kind != changeAtomHunk || atom.Hunk == nil {
					return invalidPlan("batch %d selector %d does not reference a selectable text hunk", batch.Number, selectorIndex)
				}
				if selector.AtomID == "" || selector.AtomID != atom.ID || !equalHunk(*selector.Hunk, *atom.Hunk) {
					return invalidPlan("batch %d selector %d does not match the captured hunk", batch.Number, selectorIndex)
				}
				key := atomSelectionKey(changeKey, atom.ID)
				selected[key]++
				selectedLines += atom.AddedLines
			default:
				return invalidPlan("batch %d selector %d has unsupported mode %q", batch.Number, selectorIndex, selector.Mode)
			}
		}

		for route := range routeSet {
			if _, ok := selectorRoutes[route]; !ok {
				return invalidPlan("batch %d route %q has no selector", batch.Number, route)
			}
		}
		if batch.Lines != selectedLines {
			return invalidPlan("batch %d reports %d added lines but selects %d", batch.Number, batch.Lines, selectedLines)
		}
	}

	for _, change := range changes {
		for _, atom := range change.Atoms {
			count := selected[atomSelectionKey(changeKey(change.Path, change.OldPath), atom.ID)]
			if count == 0 {
				return invalidPlan("change %q is missing atom %q", change.Path, atom.ID)
			}
			if count > 1 {
				return invalidPlan("change %q selects atom %q %d times", change.Path, atom.ID, count)
			}
		}
	}
	return nil
}

// ValidateSerializedPlan validates both selection coverage and the immutable
// plan identity. It is intentionally separate from Git freshness validation so
// corrupt JSON fails before any repository operation that could mutate history.
func ValidateSerializedPlan(plan *SerializedPlan) error {
	if plan == nil {
		return invalidPlan("plan is nil")
	}
	if err := validatePlanIntent(plan); err != nil {
		return err
	}
	if err := ValidatePlanSelections(plan); err != nil {
		return err
	}
	if plan.WorktreeState == "" {
		return invalidPlan("plan has no worktree state fingerprint")
	}
	if plan.PlanID == "" {
		return invalidPlan("plan has no plan ID")
	}
	if expected := calculatePlanID(plan); expected != plan.PlanID {
		return invalidPlan("plan ID does not match its serialized selectors and state")
	}
	return nil
}

// RecalculatePlanID binds a deliberately edited, but otherwise valid, plan to
// its current serialized selectors. It performs no Git operation and never
// changes the index or history.
func RecalculatePlanID(plan *SerializedPlan) error {
	if err := validatePlanIntent(plan); err != nil {
		return err
	}
	if err := ValidatePlanSelections(plan); err != nil {
		return err
	}
	plan.PlanID = calculatePlanID(plan)
	return nil
}

func validateLegacyWholeFilePlan(plan *SerializedPlan) error {
	seen := make(map[string]struct{})
	batchNumbers := make(map[int]struct{}, len(plan.Batches))
	for _, batch := range plan.Batches {
		if batch.Number <= 0 {
			return invalidPlan("batch %d has an invalid number", batch.Number)
		}
		if _, exists := batchNumbers[batch.Number]; exists {
			return invalidPlan("batch number %d is repeated", batch.Number)
		}
		batchNumbers[batch.Number] = struct{}{}
		if len(batch.Paths) == 0 {
			return invalidPlan("batch %d has no routes", batch.Number)
		}
		routes := make(map[string]struct{}, len(batch.Paths))
		for _, path := range batch.Paths {
			normalized, err := validatePlanPath(path)
			if err != nil {
				return err
			}
			if _, exists := routes[normalized]; exists {
				return invalidPlan("batch %d repeats route %q", batch.Number, path)
			}
			routes[normalized] = struct{}{}
		}
		if len(batch.Selectors) == 0 {
			for route := range routes {
				if _, exists := seen[changeKey(route, "")]; exists {
					return invalidPlan("route-only plan selects %q more than once", route)
				}
				seen[changeKey(route, "")] = struct{}{}
			}
			continue
		}
		selectedRoutes := make(map[string]struct{}, len(batch.Selectors))
		for _, selector := range batch.Selectors {
			if selector.Mode != SelectorWholeFile || selector.Hunk != nil || selector.AtomID != "" {
				return invalidPlan("route-only plans support whole-file selectors only")
			}
			normalized, err := validatePlanPath(selector.Path)
			if err != nil {
				return err
			}
			if _, ok := routes[normalized]; !ok {
				return invalidPlan("selector route %q is not in its batch", selector.Path)
			}
			selectedRoutes[normalized] = struct{}{}
			if _, exists := seen[changeKey(normalized, selector.OldPath)]; exists {
				return invalidPlan("route-only plan selects %q more than once", selector.Path)
			}
			seen[changeKey(normalized, selector.OldPath)] = struct{}{}
		}
		for route := range routes {
			if _, ok := selectedRoutes[route]; !ok {
				return invalidPlan("route-only plan has no selector for %q", route)
			}
		}
	}
	return nil
}

func indexPlannedChanges(input []PlannedChange) ([]PlannedChange, map[string]*PlannedChange, error) {
	changes := cloneChanges(input)
	byKey := make(map[string]*PlannedChange, len(changes))
	for i := range changes {
		change := &changes[i]
		if err := validatePlannedChange(change); err != nil {
			return nil, nil, err
		}
		key := changeKey(change.Path, change.OldPath)
		if _, exists := byKey[key]; exists {
			return nil, nil, invalidPlan("draft contains duplicate change %q", change.Path)
		}
		byKey[key] = change
	}
	return changes, byKey, nil
}

func validatePlannedChange(change *PlannedChange) error {
	path, err := validatePlanPath(change.Path)
	if err != nil {
		return err
	}
	change.Path = path
	if change.OldPath != "" {
		oldPath, err := validatePlanPath(change.OldPath)
		if err != nil {
			return err
		}
		change.OldPath = oldPath
	}
	if change.Status == "" || change.Kind == "" {
		return invalidPlan("change %q has no status or kind", change.Path)
	}
	switch change.Status {
	case "A", "D", "M", "R", "C", "T":
	default:
		return invalidPlan("change %q has unsupported status %q", change.Path, change.Status)
	}
	if (change.Status == "R" || change.Status == "C") != (change.OldPath != "") {
		return invalidPlan("change %q has inconsistent rename paths", change.Path)
	}
	if change.Kind == ChangeRename && change.OldPath == "" {
		return invalidPlan("rename change %q has no old path", change.Path)
	}
	if change.AddedLines < 0 || change.HeadHash == "" || change.IndexHash == "" || change.WorktreeHash == "" {
		return invalidPlan("change %q has incomplete state fingerprints", change.Path)
	}
	if len(change.Atoms) == 0 {
		return invalidPlan("change %q has no change atoms", change.Path)
	}

	atomIDs := make(map[string]struct{}, len(change.Atoms))
	addedLines := 0
	for i := range change.Atoms {
		atom := &change.Atoms[i]
		if atom.Index != i {
			return invalidPlan("change %q has non-deterministic atom ordering", change.Path)
		}
		if atom.Digest == "" || atom.AddedLines < 0 {
			return invalidPlan("change %q atom %d has incomplete identity", change.Path, i)
		}
		if atom.Kind == changeAtomHunk {
			if atom.Hunk == nil || atom.Hunk.PatchHash == "" || !validHunk(*atom.Hunk) || atom.Digest != atom.Hunk.PatchHash {
				return invalidPlan("change %q atom %d has invalid hunk metadata", change.Path, i)
			}
		} else if atom.Hunk != nil {
			return invalidPlan("change %q atom %d has hunk metadata for a file atom", change.Path, i)
		}
		switch change.Kind {
		case ChangeText:
			if atom.Kind != changeAtomHunk && atom.Kind != changeAtomFile {
				return invalidPlan("text change %q has unsupported atom kind %q", change.Path, atom.Kind)
			}
		case ChangeBinary:
			if atom.Kind != changeAtomBinary {
				return invalidPlan("binary change %q has unsupported atom kind %q", change.Path, atom.Kind)
			}
		case ChangeRename:
			if atom.Kind != changeAtomRename {
				return invalidPlan("rename change %q has unsupported atom kind %q", change.Path, atom.Kind)
			}
		case ChangeFile:
			if atom.Kind != changeAtomFile {
				return invalidPlan("file change %q has unsupported atom kind %q", change.Path, atom.Kind)
			}
		default:
			return invalidPlan("change %q has unsupported kind %q", change.Path, change.Kind)
		}
		if expected := atomID(*change, *atom); expected != atom.ID {
			return invalidPlan("change %q atom %d has a corrupt atom ID", change.Path, i)
		}
		if _, exists := atomIDs[atom.ID]; exists {
			return invalidPlan("change %q repeats atom %q", change.Path, atom.ID)
		}
		atomIDs[atom.ID] = struct{}{}
		addedLines += atom.AddedLines
	}
	if addedLines != change.AddedLines {
		return invalidPlan("change %q reports %d added lines but its atoms contain %d", change.Path, change.AddedLines, addedLines)
	}
	return nil
}

func validHunk(hunk DiffHunk) bool {
	return hunk.OldStart >= 0 && hunk.OldLines >= 0 && hunk.NewStart >= 0 && hunk.NewLines >= 0
}

func equalHunk(left, right DiffHunk) bool {
	return left == right
}

func validatePlanPath(value string) (string, error) {
	if value == "" {
		return "", invalidPlan("empty change path")
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if normalized == "." || filepath.IsAbs(filepath.FromSlash(normalized)) || normalized == ".." || len(normalized) >= 3 && normalized[:3] == "../" {
		return "", invalidPlan("invalid change path %q", value)
	}
	if normalized != value {
		return "", invalidPlan("change path %q is not normalized", value)
	}
	return normalized, nil
}

func validatePlanIntent(plan *SerializedPlan) error {
	if plan == nil {
		return invalidPlan("plan is nil")
	}
	normalized, err := intent.Normalize(plan.Intent, plan.IntentSource)
	if err != nil {
		return invalidPlan("plan intent: %v", err)
	}
	if normalized.Text != plan.Intent || normalized.Source != plan.IntentSource {
		return invalidPlan("plan intent is not normalized")
	}
	return nil
}

func invalidPlan(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPlan, fmt.Sprintf(format, args...))
}

func changeKey(path, oldPath string) string {
	return path + "\x00" + oldPath
}

func atomSelectionKey(changeKey, atomID string) string {
	return changeKey + "\x00" + atomID
}

func atomID(change PlannedChange, atom ChangeAtom) string {
	identity := struct {
		Path       string     `json:"path"`
		OldPath    string     `json:"old_path,omitempty"`
		Status     string     `json:"status"`
		ChangeKind ChangeKind `json:"change_kind"`
		AtomKind   string     `json:"atom_kind"`
		Index      int        `json:"index"`
		Hunk       *DiffHunk  `json:"hunk,omitempty"`
		Digest     string     `json:"digest"`
		AddedLines int        `json:"added_lines"`
	}{change.Path, change.OldPath, change.Status, change.Kind, atom.Kind, atom.Index, atom.Hunk, atom.Digest, atom.AddedLines}
	encoded, _ := json.Marshal(identity)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func calculatePlanID(plan *SerializedPlan) string {
	identity := planIdentity{
		State:        plan.WorktreeState,
		Intent:       plan.Intent,
		IntentSource: plan.IntentSource,
		Batches:      canonicalBatches(plan.Batches),
		Changes:      canonicalChanges(plan.Changes),
		Decisions:    canonicalDecisions(plan.PendingDecisions),
	}
	encoded, _ := json.Marshal(identity)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func canonicalBatches(input []SerializedBatch) []batchIdentity {
	batches := make([]batchIdentity, 0, len(input))
	for _, batch := range input {
		batches = append(batches, batchIdentity{
			Number:      batch.Number,
			Layer:       batch.Layer,
			Paths:       sortedUnique(batch.Paths),
			Lines:       batch.Lines,
			IsOversized: batch.IsOversized,
			Selectors:   canonicalSelectors(batch.Selectors),
		})
	}
	sort.Slice(batches, func(i, j int) bool {
		return batches[i].Number < batches[j].Number
	})
	return batches
}

func canonicalSelectors(input []ChangeSelector) []ChangeSelector {
	selectors := cloneSelectors(input)
	sort.Slice(selectors, func(i, j int) bool {
		left, right := selectors[i], selectors[j]
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.OldPath != right.OldPath {
			return left.OldPath < right.OldPath
		}
		if left.Mode != right.Mode {
			return left.Mode < right.Mode
		}
		if left.HunkIndex != right.HunkIndex {
			return left.HunkIndex < right.HunkIndex
		}
		return left.AtomID < right.AtomID
	})
	return selectors
}

func canonicalChanges(input []PlannedChange) []PlannedChange {
	changes := cloneChanges(input)
	sort.Slice(changes, func(i, j int) bool {
		left, right := changeKey(changes[i].Path, changes[i].OldPath), changeKey(changes[j].Path, changes[j].OldPath)
		return left < right
	})
	for i := range changes {
		sort.Slice(changes[i].Atoms, func(left, right int) bool {
			return changes[i].Atoms[left].Index < changes[i].Atoms[right].Index
		})
	}
	return changes
}

func canonicalDecisions(input []PendingDecision) []PendingDecision {
	decisions := append([]PendingDecision(nil), input...)
	sort.Slice(decisions, func(i, j int) bool {
		return decisions[i].ID < decisions[j].ID
	})
	return decisions
}

func sortedUnique(values []string) []string {
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	result := copyOfValues[:0]
	for _, value := range copyOfValues {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func cloneSelectors(input []ChangeSelector) []ChangeSelector {
	result := make([]ChangeSelector, len(input))
	for i, selector := range input {
		result[i] = selector
		if selector.Hunk != nil {
			hunk := *selector.Hunk
			result[i].Hunk = &hunk
		}
	}
	return result
}

func cloneChanges(input []PlannedChange) []PlannedChange {
	result := make([]PlannedChange, len(input))
	for i, change := range input {
		result[i] = change
		result[i].Atoms = make([]ChangeAtom, len(change.Atoms))
		for j, atom := range change.Atoms {
			result[i].Atoms[j] = atom
			if atom.Hunk != nil {
				hunk := *atom.Hunk
				result[i].Atoms[j].Hunk = &hunk
			}
		}
	}
	return result
}
