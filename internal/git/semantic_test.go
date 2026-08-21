package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSemanticSlicePlanKeepsDirectDependenciesTogether(t *testing.T) {
	t.Chdir(t.TempDir())
	writeSemanticFile(t, "internal/git/staged.go", "package git\nfunc MedirVolumenStaged() {}\n")
	writeSemanticFile(t, "internal/git/staged_test.go", "package git\nfunc TestStaged() { MedirVolumenStaged() }\n")
	writeSemanticFile(t, "cmd/sentinel/staged_check.go", "package main\nimport git \"github.com/ISeoane-Quental/vas.sentinel/internal/git\"\nfunc check() { git.MedirVolumenStaged() }\n")
	writeSemanticFile(t, "cmd/sentinel/staged_check_test.go", "package main\nfunc TestCheck() { check() }\n")

	changes := []PlannedChange{
		semanticFileChange("cmd/sentinel/staged_check.go", 30),
		semanticFileChange("cmd/sentinel/staged_check_test.go", 30),
		semanticFileChange("internal/git/staged.go", 30),
		semanticFileChange("internal/git/staged_test.go", 30),
	}
	plan, err := BuildSemanticSlicePlan(changes, SemanticSliceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lotes) != 1 {
		t.Fatalf("semantic dependency unit was split: %+v", plan.Lotes)
	}
	paths := plan.Lotes[0].Rutas
	if indexOfPath(paths, "internal/git/staged.go") > indexOfPath(paths, "cmd/sentinel/staged_check.go") {
		t.Fatalf("dependency was ordered after its consumer: %v", paths)
	}
	if len(plan.Lotes[0].Selectors) != 4 {
		t.Fatalf("selectors = %+v, want production and focused tests together", plan.Lotes[0].Selectors)
	}
}

func TestConstruirPlanParaAgenteValidatesAccumulatedMultiFileCoverage(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "base.txt", "base\n")
	writeSemanticFile(t, "a.go", "package sample\n\nfunc A() { B() }\n")
	writeSemanticFile(t, "b.go", "package sample\n\nfunc B() {\n\tA()\n}\n")

	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("ConstruirPlanParaAgente failed: %v", err)
	}
	if err := ValidateSerializedPlan(plan); err != nil {
		t.Fatalf("serialized accumulated plan failed validation: %v", err)
	}
	if len(plan.Lotes) != 1 || len(plan.Lotes[0].Selectors) != 2 {
		t.Fatalf("accumulated plan = %+v, want one batch with both files", plan.Lotes)
	}
	for _, lote := range plan.Lotes {
		if got := selectedLinesFromPlan(t, plan, lote); got != lote.Lineas {
			t.Fatalf("batch %d reports %d lines but selects %d", lote.Numero, lote.Lineas, got)
		}
	}
}

func TestBuildSemanticSlicePlanSubdividesSafeExactAtoms(t *testing.T) {
	t.Chdir(t.TempDir())
	writeSemanticFile(t, "notes.txt", strings.Repeat("line\n", 500))
	change := semanticTextChange("notes.txt", 250, 250)
	plan, err := BuildSemanticSlicePlan([]PlannedChange{change}, SemanticSliceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lotes) != 2 {
		t.Fatalf("lotes = %+v, want one exact-atom batch per safe part", plan.Lotes)
	}
	for _, lote := range plan.Lotes {
		if lote.LineasTotales > LimiteLineasRevisables || len(lote.Selectors) != 1 || lote.Selectors[0].Mode != SelectorHunk {
			t.Fatalf("unsafe or oversized exact split: %+v", lote)
		}
	}
	serialized := SerializarPlan(plan, nil, "state")
	if err := ValidatePlanSelections(serialized); err != nil {
		t.Fatalf("exact split failed selection validation: %v", err)
	}
}

func TestBuildSemanticSlicePlanReportsSelectedAtomLines(t *testing.T) {
	t.Chdir(t.TempDir())
	writeSemanticFile(t, "a.txt", "a\n")
	writeSemanticFile(t, "b.txt", "b\n")
	changes := []PlannedChange{
		semanticTextChange("a.txt", 120, 80),
		semanticTextChange("b.txt", 50, 30),
	}

	plan, err := BuildSemanticSlicePlan(changes, SemanticSliceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	serialized := SerializarPlan(plan, nil, "state")
	if err := ValidatePlanSelections(serialized); err != nil {
		t.Fatalf("line-accounting plan failed selection validation: %v", err)
	}
	if len(serialized.Lotes) != 1 {
		t.Fatalf("lotes = %+v, want one accumulated batch", serialized.Lotes)
	}
	if got, want := serialized.Lotes[0].Lineas, 280; got != want {
		t.Fatalf("serialized line count = %d, want %d", got, want)
	}
	if got := selectedLinesFromPlan(t, serialized, serialized.Lotes[0]); got != serialized.Lotes[0].Lineas {
		t.Fatalf("serialized line count = %d, selected atom lines = %d", serialized.Lotes[0].Lineas, got)
	}
}

func TestBuildSemanticSlicePlanSurfacesUnsafeOversizedUnit(t *testing.T) {
	t.Chdir(t.TempDir())
	writeSemanticFile(t, "big.go", "package big\nfunc Big() {}\n")
	called := SemanticOversizedUnit{}
	plan, err := BuildSemanticSlicePlan([]PlannedChange{semanticFileChange("big.go", 401)}, SemanticSliceOptions{
		ConfirmOversized: func(unit SemanticOversizedUnit) (bool, error) {
			called = unit
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called.AddedLines != 401 || len(called.Paths) != 1 || !plan.Lotes[0].EsGigante {
		t.Fatalf("oversized decision = %+v, plan = %+v", called, plan.Lotes)
	}
}

func TestBuildSemanticSlicePlanRejectsBoundaryThatSplitsDependency(t *testing.T) {
	t.Chdir(t.TempDir())
	writeSemanticFile(t, "pkg/producer.go", "package pkg\nfunc Required() {}\n")
	writeSemanticFile(t, "pkg/consumer.go", "package pkg\nfunc Use() { Required() }\n")
	_, err := BuildSemanticSlicePlan([]PlannedChange{
		semanticFileChange("pkg/producer.go", 10),
		semanticFileChange("pkg/consumer.go", 10),
	}, SemanticSliceOptions{Boundaries: []SemanticSliceBoundary{
		{ID: "producer", Paths: []string{"pkg/producer.go"}},
		{ID: "consumer", Paths: []string{"pkg/consumer.go"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "compile dependency") {
		t.Fatalf("boundary validation error = %v", err)
	}
}

func TestBuildSemanticSlicePlanFallsBackFromInvalidExternalProposal(t *testing.T) {
	t.Chdir(t.TempDir())
	writeSemanticFile(t, "a.txt", "a\n")
	writeSemanticFile(t, "b.txt", "b\n")
	changes := []PlannedChange{semanticFileChange("a.txt", 10), semanticFileChange("b.txt", 10)}
	plan, err := BuildSemanticSlicePlan(changes, SemanticSliceOptions{
		Consent: true,
		Proposal: &SemanticSliceProposal{
			State: hashPlannedChangesState(changes),
			Units: []SemanticSliceUnit{{ID: "missing-b", Paths: []string{"a.txt"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Explanation, "External proposal rejected") {
		t.Fatalf("fallback explanation = %q", plan.Explanation)
	}
	if len(plan.Lotes) != 1 || len(plan.Lotes[0].Rutas) != 2 {
		t.Fatalf("fallback plan = %+v", plan.Lotes)
	}
}

func semanticFileChange(path string, lines int) PlannedChange {
	change := PlannedChange{Path: path, Status: "A", Kind: ChangeFile, AddedLines: lines, HeadHash: "head", IndexHash: "index", WorktreeHash: "worktree"}
	atom := ChangeAtom{Index: 0, Kind: changeAtomFile, Digest: "digest-" + path, AddedLines: lines}
	atom.ID = atomID(change, atom)
	change.Atoms = []ChangeAtom{atom}
	return change
}

func semanticTextChange(path string, lines ...int) PlannedChange {
	change := PlannedChange{Path: path, Status: "A", Kind: ChangeText, HeadHash: "head", IndexHash: "index", WorktreeHash: "worktree"}
	for _, added := range lines {
		change.AddedLines += added
	}
	for index, added := range lines {
		hunk := DiffHunk{NewStart: index * 300, NewLines: added, PatchHash: "patch-" + string(rune('a'+index))}
		atom := ChangeAtom{Index: index, Kind: changeAtomHunk, Hunk: &hunk, Digest: hunk.PatchHash, AddedLines: added}
		atom.ID = atomID(change, atom)
		change.Atoms = append(change.Atoms, atom)
	}
	return change
}

func writeSemanticFile(t *testing.T, path, content string) {
	t.Helper()
	fullPath := filepath.FromSlash(path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func indexOfPath(paths []string, path string) int {
	for index, candidate := range paths {
		if candidate == path {
			return index
		}
	}
	return len(paths)
}

func selectedLinesFromPlan(t *testing.T, plan *PlanSerializado, lote LoteSerializado) int {
	t.Helper()
	lines := 0
	for _, selector := range lote.Selectors {
		found := false
		for _, change := range plan.Changes {
			if changeKey(change.Path, change.OldPath) != changeKey(selector.Path, selector.OldPath) {
				continue
			}
			found = true
			switch selector.Mode {
			case SelectorWholeFile:
				lines += sumAtomLines(change.Atoms)
			case SelectorHunk:
				if selector.HunkIndex < 0 || selector.HunkIndex >= len(change.Atoms) {
					t.Fatalf("selector %q has invalid atom index %d", selector.Path, selector.HunkIndex)
				}
				lines += change.Atoms[selector.HunkIndex].AddedLines
			default:
				t.Fatalf("selector %q has unsupported mode %q", selector.Path, selector.Mode)
			}
			break
		}
		if !found {
			t.Fatalf("selector %q does not reference a captured change", selector.Path)
		}
	}
	return lines
}
