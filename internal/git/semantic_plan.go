package git

import (
	"fmt"
	pathpkg "path"
	"path/filepath"
	"strings"
)

// BuildSemanticSlicePlan builds a plan without staging, writing the index, or
// creating history. An untrusted proposal is accepted only with consent and
// after all deterministic validations succeed.
func BuildSemanticSlicePlan(changes []PlannedChange, options SemanticSliceOptions) (*FragmentationPlan, error) {
	canonical, _, err := indexPlannedChanges(changes)
	if err != nil {
		return nil, err
	}
	if len(canonical) == 0 {
		return &FragmentationPlan{Changes: canonical, Explanation: mechanicalSemanticFallback}, nil
	}
	files, opaque := readSemanticFiles(canonical)
	graph := semanticDependencies(files)
	boundaries, err := validateSemanticBoundaries(options.Boundaries, files, graph)
	if err != nil {
		return nil, err
	}
	ranks, explanation := semanticCohesion(files, runGitOutput)
	if opaque {
		explanation += " Some changed files could not be parsed, so the fallback keeps all changed files in a conservative dependency unit."
	}
	if options.Proposal != nil {
		if options.Consent {
			if plan, proposalErr := acceptSemanticProposal(canonical, files, graph, boundaries, options.Proposal); proposalErr == nil {
				plan.Explanation = "Accepted consented external proposal after deterministic validation."
				return plan, nil
			} else {
				explanation += fmt.Sprintf(" External proposal rejected: %v. Falling back deterministically.", proposalErr)
			}
		} else {
			explanation += " External proposal ignored because explicit consent was not supplied."
		}
	}
	components := semanticComponents(files, graph, boundaries, ranks)
	plan := &FragmentationPlan{Changes: canonical, Explanation: explanation + " " + mechanicalSemanticFallback}
	number := 1
	var grouped *semanticComponent
	flushGrouped := func() {
		if grouped == nil {
			return
		}
		plan.Batches = append(plan.Batches, semanticBatch(*grouped, files, number))
		number++
		grouped = nil
	}
	for _, component := range components {
		if component.lines <= ReviewableLinesLimit {
			if grouped != nil && sameSemanticGroup(*grouped, component) && grouped.lines+component.lines <= ReviewableLinesLimit {
				mergeSemanticComponents(grouped, component)
			} else {
				flushGrouped()
				copyOfComponent := component
				grouped = &copyOfComponent
			}
			continue
		}
		flushGrouped()
		parts, safe := exactAtomParts(component, files)
		if safe {
			for _, part := range parts {
				plan.Batches = append(plan.Batches, semanticBatch(part, files, number))
				number++
			}
			continue
		}
		unit := SemanticOversizedUnit{ID: semanticID(component, files), Paths: componentPaths(component, files), AddedLines: component.lines, Reason: "compile-dependent source or focused tests cannot be safely split by exact diff atoms"}
		if options.ConfirmOversized == nil {
			return nil, fmt.Errorf("semantic unit %q exceeds the %d-line budget and requires an explicit decision", unit.ID, ReviewableLinesLimit)
		}
		approved, err := options.ConfirmOversized(unit)
		if err != nil {
			return nil, err
		}
		if !approved {
			return nil, fmt.Errorf("semantic slicing aborted: unit %q exceeds the %d-line budget", unit.ID, ReviewableLinesLimit)
		}
		batch := semanticBatch(component, files, number)
		batch.IsOversized = true
		batch.Message = semanticOversizedMessage(unit)
		batch.AutoMessage = batch.Message
		batch.DeterministicMessage = true
		plan.Batches = append(plan.Batches, batch)
		number++
	}
	flushGrouped()
	if err := validateSemanticPlan(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func semanticBatch(component semanticComponent, files []semanticFile, number int) PlannedBatch {
	selectors := append([]ChangeSelector(nil), component.selectors...)
	routes := []string{}
	if len(selectors) == 0 {
		for _, i := range component.ordered {
			change := files[i].change
			selectors = append(selectors, ChangeSelector{Path: change.Path, OldPath: change.OldPath, Mode: SelectorWholeFile})
		}
	}
	for _, selector := range selectors {
		routes = appendUniquePath(routes, selector.Path)
	}
	layer := files[component.ordered[0]].layer
	for _, i := range component.ordered[1:] {
		if files[i].layer != layer {
			layer = "semantic"
		}
	}
	return PlannedBatch{
		Layer:       layer,
		Number:      number,
		Paths:       routes,
		Selectors:   selectors,
		TotalLines:  semanticSelectedLines(selectors, files),
		AutoMessage: fmt.Sprintf("chore(slice): auto-fragmented %s batch #%d", layer, number),
	}
}

func semanticSelectedLines(selectors []ChangeSelector, files []semanticFile) int {
	lines := 0
	for _, selector := range selectors {
		selectorKey := changeKey(selector.Path, selector.OldPath)
		for _, file := range files {
			if changeKey(file.change.Path, file.change.OldPath) != selectorKey {
				continue
			}
			if selector.Mode == SelectorWholeFile {
				lines += sumAtomLines(file.change.Atoms)
			} else if selector.HunkIndex >= 0 && selector.HunkIndex < len(file.change.Atoms) {
				lines += file.change.Atoms[selector.HunkIndex].AddedLines
			}
			break
		}
	}
	return lines
}

func validateSemanticPlan(plan *FragmentationPlan) error {
	serialized := &SerializedPlan{Changes: plan.Changes}
	for _, batch := range plan.Batches {
		serialized.Batches = append(serialized.Batches, SerializedBatch{
			Number:    batch.Number,
			Layer:     batch.Layer,
			Paths:     batch.Paths,
			Selectors: batch.Selectors,
			Lines:     batch.TotalLines,
		})
	}
	if err := ValidatePlanSelections(serialized); err != nil {
		return fmt.Errorf("semantic plan failed exact selection validation: %w", err)
	}
	return nil
}

func sameSemanticGroup(left, right semanticComponent) bool {
	return left.boundary == right.boundary && left.rank == right.rank && left.class == right.class
}

func mergeSemanticComponents(target *semanticComponent, source semanticComponent) {
	target.ids = append(target.ids, source.ids...)
	target.ordered = append(target.ordered, source.ordered...)
	target.lines += source.lines
	if source.first < target.first {
		target.first = source.first
	}
}

func exactAtomParts(component semanticComponent, files []semanticFile) ([]semanticComponent, bool) {
	if len(component.ids) != 1 || strings.HasSuffix(strings.ToLower(files[component.ids[0]].path), ".go") {
		return nil, false
	}
	change := files[component.ids[0]].change
	if len(change.Atoms) < 2 {
		return nil, false
	}
	var parts []semanticComponent
	for index, atom := range change.Atoms {
		if atom.AddedLines > ReviewableLinesLimit {
			return nil, false
		}
		part := component
		part.ids = []int{component.ids[0]}
		part.ordered = part.ids
		part.lines = atom.AddedLines
		part.first = change.Path
		part.selectors = []ChangeSelector{{Path: change.Path, OldPath: change.OldPath, Mode: SelectorHunk, AtomID: atom.ID, HunkIndex: index, Hunk: atom.Hunk}}
		parts = append(parts, part)
	}
	return parts, len(parts) > 0
}

func acceptSemanticProposal(changes []PlannedChange, files []semanticFile, graph semanticGraph, boundaries map[int]int, proposal *SemanticSliceProposal) (*FragmentationPlan, error) {
	if proposal.State == "" || proposal.State != hashPlannedChangesState(changes) {
		return nil, fmt.Errorf("proposal is missing or has a stale state fingerprint")
	}
	if len(proposal.Units) == 0 {
		return nil, fmt.Errorf("proposal has no units")
	}
	pathToFile := map[string]int{}
	for i, file := range files {
		pathToFile[changeKey(file.change.Path, file.change.OldPath)] = i
	}
	seenAtoms, seenUnits := map[string]bool{}, map[string]bool{}
	plan := &FragmentationPlan{Changes: changes}
	for number, unit := range proposal.Units {
		if unit.ID == "" || seenUnits[unit.ID] {
			return nil, fmt.Errorf("proposal has a missing or repeated unit ID")
		}
		seenUnits[unit.ID] = true
		selectors, err := proposalSelectors(unit, files, pathToFile)
		if err != nil {
			return nil, err
		}
		indices := []int{}
		lines := 0
		for _, selector := range selectors {
			i, ok := pathToFile[changeKey(selector.Path, selector.OldPath)]
			if !ok {
				return nil, fmt.Errorf("proposal references a missing change %q", selector.Path)
			}
			if boundary, ok := boundaries[i]; ok {
				for _, other := range indices {
					if otherBoundary, exists := boundaries[other]; exists && otherBoundary != boundary {
						return nil, fmt.Errorf("proposal unit %q crosses explicit boundaries", unit.ID)
					}
				}
			}
			indices = append(indices, i)
			atoms, err := proposalAtoms(selector, files[i].change)
			if err != nil {
				return nil, err
			}
			for _, atom := range atoms {
				key := atomSelectionKey(changeKey(selector.Path, selector.OldPath), atom.ID)
				if seenAtoms[key] {
					return nil, fmt.Errorf("proposal selects an overlapping atom")
				}
				seenAtoms[key] = true
				lines += atom.AddedLines
			}
		}
		if lines > ReviewableLinesLimit || breaksDeps(indices, graph) {
			return nil, fmt.Errorf("proposal unit %q is oversized or breaks a compile dependency", unit.ID)
		}
		component := semanticComponent{ordered: uniqueInts(indices), lines: lines}
		batch := semanticBatch(component, files, number+1)
		batch.Selectors, batch.Paths, batch.TotalLines = selectors, selectorPaths(selectors), lines
		plan.Batches = append(plan.Batches, batch)
	}
	for _, change := range changes {
		for _, atom := range change.Atoms {
			if !seenAtoms[atomSelectionKey(changeKey(change.Path, change.OldPath), atom.ID)] {
				return nil, fmt.Errorf("proposal has missing selections")
			}
		}
	}
	serialized := &SerializedPlan{Changes: changes}
	for _, batch := range plan.Batches {
		serialized.Batches = append(serialized.Batches, SerializedBatch{Number: batch.Number, Layer: batch.Layer, Paths: batch.Paths, Selectors: batch.Selectors, Lines: batch.TotalLines})
	}
	if err := ValidatePlanSelections(serialized); err != nil {
		return nil, fmt.Errorf("proposal failed exact selection validation: %w", err)
	}
	return plan, nil
}

func proposalSelectors(unit SemanticSliceUnit, files []semanticFile, paths map[string]int) ([]ChangeSelector, error) {
	selectors := append([]ChangeSelector(nil), unit.Selectors...)
	for _, path := range unit.Paths {
		normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		found := false
		for key, i := range paths {
			if strings.HasPrefix(key, normalized+"\x00") {
				change := files[i].change
				selectors = append(selectors, ChangeSelector{Path: change.Path, OldPath: change.OldPath, Mode: SelectorWholeFile})
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("proposal references missing path %q", path)
		}
	}
	if len(selectors) == 0 {
		return nil, fmt.Errorf("proposal unit %q has no selections", unit.ID)
	}
	return selectors, nil
}

func proposalAtoms(selector ChangeSelector, change PlannedChange) ([]ChangeAtom, error) {
	if selector.Mode == SelectorWholeFile {
		if selector.AtomID != "" || selector.Hunk != nil {
			return nil, fmt.Errorf("whole-file proposal selector contains hunk data")
		}
		return change.Atoms, nil
	}
	if selector.Mode != SelectorHunk || selector.HunkIndex < 0 || selector.HunkIndex >= len(change.Atoms) {
		return nil, fmt.Errorf("proposal contains an invalid selector")
	}
	atom := change.Atoms[selector.HunkIndex]
	if atom.Kind != changeAtomHunk || atom.Hunk == nil || atom.ID != selector.AtomID || !equalHunk(*atom.Hunk, *selector.Hunk) {
		return nil, fmt.Errorf("proposal hunk does not match the captured atom")
	}
	return []ChangeAtom{atom}, nil
}

func breaksDeps(indices []int, graph semanticGraph) bool {
	selected := map[int]bool{}
	for _, i := range indices {
		selected[i] = true
	}
	for _, i := range indices {
		for dependency := range graph.deps[i] {
			if !selected[dependency] {
				return true
			}
		}
	}
	return false
}

func semanticID(component semanticComponent, files []semanticFile) string {
	return DecisionID(strings.Join(componentPaths(component, files), "\x00"))
}

func semanticOversizedMessage(unit SemanticOversizedUnit) string {
	if len(unit.Paths) == 1 {
		return fmt.Sprintf(giantBypassMessage, pathpkg.Base(unit.Paths[0]))
	}
	return fmt.Sprintf("chore(slice): bypass AI for semantic unit %s", unit.ID)
}

func componentPaths(component semanticComponent, files []semanticFile) []string {
	paths := []string{}
	for _, i := range component.ordered {
		paths = append(paths, files[i].path)
	}
	return paths
}

func selectorPaths(selectors []ChangeSelector) []string {
	paths := []string{}
	for _, selector := range selectors {
		paths = appendUniquePath(paths, selector.Path)
	}
	return paths
}

func appendUniquePath(paths []string, path string) []string {
	for _, existing := range paths {
		if existing == path {
			return paths
		}
	}
	return append(paths, path)
}

func containsInt(values []int, value int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func uniqueInts(values []int) []int {
	result := []int{}
	for _, value := range values {
		if !containsInt(result, value) {
			result = append(result, value)
		}
	}
	return result
}
