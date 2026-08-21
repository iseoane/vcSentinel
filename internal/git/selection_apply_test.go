package git

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestApplySelectionCommitsHunksInSeparateBatches(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	if err := writeSelectionTestFile("app.go", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nNINE\nten\n"); err != nil {
		t.Fatal(err)
	}

	plan := requireTwoHunkPlan(t)
	change := plan.Changes[0]
	plan.Lotes = []LoteSerializado{
		{
			Numero:    1,
			Capa:      "backend",
			Rutas:     []string{change.Path},
			Selectors: []ChangeSelector{selectorForHunk(change, 0)},
			Lineas:    change.Atoms[0].AddedLines,
			Mensaje:   "feat(slice): commit the first hunk",
		},
		{
			Numero:    2,
			Capa:      "backend",
			Rutas:     []string{change.Path},
			Selectors: []ChangeSelector{selectorForHunk(change, 1)},
			Lineas:    change.Atoms[1].AddedLines,
			Mensaje:   "feat(slice): commit the second hunk",
		},
	}
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}

	results, err := AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("AplicarPlanAprobado: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("commits = %d, want 2", len(results))
	}
	assertCommitPatchContains(t, results[0].Hash, "app.go", "+TWO")
	assertCommitPatchExcludes(t, results[0].Hash, "app.go", "+NINE")
	assertCommitPatchContains(t, results[1].Hash, "app.go", "+NINE")
	assertCommitPatchExcludes(t, results[1].Hash, "app.go", "+TWO")
	assertSelectionApplyClean(t)
}

func TestApplySelectionKeepsWholeFileAndHunkBoundaries(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	commitEnRepo(t, "notes.txt", "old note\n")
	if err := writeSelectionTestFile("app.go", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nNINE\nten\n"); err != nil {
		t.Fatal(err)
	}
	if err := writeSelectionTestFile("notes.txt", "new note\nsecond line\n"); err != nil {
		t.Fatal(err)
	}

	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("ConstruirPlanParaAgente: %v", err)
	}
	hunkChange := findPlannedChange(t, plan.Changes, "app.go")
	wholeChange := findPlannedChange(t, plan.Changes, "notes.txt")
	if len(hunkChange.Atoms) != 2 {
		t.Fatalf("app.go atoms = %d, want 2", len(hunkChange.Atoms))
	}
	plan.Lotes = []LoteSerializado{
		{
			Numero: 1,
			Capa:   "backend",
			Rutas:  []string{"app.go", "notes.txt"},
			Selectors: []ChangeSelector{
				selectorForHunk(hunkChange, 0),
				{Path: wholeChange.Path, Mode: SelectorWholeFile},
			},
			Lineas:  hunkChange.Atoms[0].AddedLines + wholeChange.AddedLines,
			Mensaje: "feat(slice): commit the selected change set",
		},
		{
			Numero:    2,
			Capa:      "backend",
			Rutas:     []string{"app.go"},
			Selectors: []ChangeSelector{selectorForHunk(hunkChange, 1)},
			Lineas:    hunkChange.Atoms[1].AddedLines,
			Mensaje:   "feat(slice): finish the remaining hunk",
		},
	}
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}

	results, err := AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("AplicarPlanAprobado: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("commits = %d, want 2", len(results))
	}
	assertCommitPatchContains(t, results[0].Hash, "app.go", "+TWO")
	assertCommitPatchContains(t, results[0].Hash, "notes.txt", "+second line")
	assertCommitPatchExcludes(t, results[0].Hash, "app.go", "+NINE")
	assertCommitPatchContains(t, results[1].Hash, "app.go", "+NINE")
	assertCommitPatchExcludes(t, results[1].Hash, "notes.txt", "+second line")
	assertSelectionApplyClean(t)
}

func TestApplySelectionRejectsCorruptionBeforeHistoryMutation(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\n")
	if err := writeSelectionTestFile("app.go", "ONE\ntwo\nthree\nFOUR\n"); err != nil {
		t.Fatal(err)
	}
	plan := requireTwoHunkPlan(t)
	change := plan.Changes[0]
	plan.Lotes[0].Selectors = []ChangeSelector{
		selectorForHunk(change, 0),
		selectorForHunk(change, 1),
	}
	plan.Lotes[0].Lineas = change.AddedLines
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}
	plan.Lotes[0].Selectors[0].Hunk.PatchHash = "corrupt"
	commitsBefore := contarCommits(t)
	statusBefore, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall")
	if err != nil {
		t.Fatalf("git status before apply: %v", err)
	}

	_, err = AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
	if !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("apply error = %v, want ErrInvalidPlan", err)
	}
	if contarCommits(t) != commitsBefore {
		t.Fatal("corrupt selection created history")
	}
	statusAfter, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall")
	if err != nil {
		t.Fatalf("git status after apply: %v", err)
	}
	if statusAfter != statusBefore {
		t.Fatalf("rejected apply changed worktree status: before %q, after %q", statusBefore, statusAfter)
	}
}

func TestApplySelectionSupportsWholeFileDraftKinds(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			name: "untracked file",
			setup: func(t *testing.T) {
				commitEnRepo(t, "base.txt", "base\n")
				if err := writeSelectionTestFile("new.go", "package new\n"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "binary file",
			setup: func(t *testing.T) {
				commitEnRepo(t, "image.bin", "binary base\n")
				if err := os.WriteFile("image.bin", []byte{0, 1, 2, 3, 0xff}, 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "rename",
			setup: func(t *testing.T) {
				commitEnRepo(t, "old.go", "rename me\n")
				ejecutar(t, "mv", "old.go", "new.go")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepararRepoTemp(t)
			tt.setup(t)
			plan, err := ConstruirPlanParaAgente()
			if err != nil {
				t.Fatalf("ConstruirPlanParaAgente: %v", err)
			}
			makeWholeFilePlan(t, plan)
			results, err := AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
			if err != nil {
				t.Fatalf("AplicarPlanAprobado: %v", err)
			}
			if len(results) != len(plan.Lotes) {
				t.Fatalf("commits = %d, want %d", len(results), len(plan.Lotes))
			}
			assertSelectionApplyClean(t)
		})
	}
}

func TestApplySelectionPreservesPartialStagingAndUnrelatedIndex(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	if err := writeSelectionTestFile("app.go", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"); err != nil {
		t.Fatal(err)
	}
	ejecutar(t, "add", "--", "app.go")
	if err := writeSelectionTestFile("app.go", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nNINE\nten\n"); err != nil {
		t.Fatal(err)
	}
	plan := requireTwoHunkPlan(t)
	change := plan.Changes[0]
	plan.Lotes = []LoteSerializado{
		{
			Numero:    1,
			Capa:      "backend",
			Rutas:     []string{"app.go"},
			Selectors: []ChangeSelector{selectorForHunk(change, 0)},
			Lineas:    change.Atoms[0].AddedLines,
			Mensaje:   "feat(slice): commit staged and unstaged first hunk",
		},
		{
			Numero:    2,
			Capa:      "backend",
			Rutas:     []string{"app.go"},
			Selectors: []ChangeSelector{selectorForHunk(change, 1)},
			Lineas:    change.Atoms[1].AddedLines,
			Mensaje:   "feat(slice): commit staged and unstaged second hunk",
		},
	}
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}
	if err := writeSelectionTestFile("unrelated.txt", "must remain staged\n"); err != nil {
		t.Fatal(err)
	}
	ejecutar(t, "add", "--", "unrelated.txt")

	results, err := AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("AplicarPlanAprobado: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("commits = %d, want 2", len(results))
	}
	assertCommitPatchExcludes(t, results[0].Hash, "unrelated.txt", "must remain staged")
	assertCommitPatchExcludes(t, results[1].Hash, "unrelated.txt", "must remain staged")
	staged, err := ejecutarGitSalida("diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if staged != "unrelated.txt\n" {
		t.Fatalf("staged unrelated paths = %q, want unrelated.txt only", staged)
	}
	status, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if status != "A  unrelated.txt\x00" {
		t.Fatalf("status = %q, want unrelated staged file only", status)
	}
}

func TestApplyLegacyRouteOnlyPlanUsesIsolatedIndex(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "planned.txt", "before\n")
	if err := writeSelectionTestFile("planned.txt", "after\n"); err != nil {
		t.Fatal(err)
	}

	plan := &PlanSerializado{
		Lotes: []LoteSerializado{{
			Numero:  1,
			Capa:    "backend",
			Rutas:   []string{"planned.txt"},
			Lineas:  1,
			Mensaje: "feat(slice): apply a legacy route-only plan",
		}},
	}
	state, err := HashEstadoWorktree([]string{"planned.txt"})
	if err != nil {
		t.Fatalf("HashEstadoWorktree: %v", err)
	}
	plan.EstadoWorktree = state
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}

	if err := writeSelectionTestFile("unrelated.txt", "must remain staged\n"); err != nil {
		t.Fatal(err)
	}
	ejecutar(t, "add", "--", "unrelated.txt")

	results, err := AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("AplicarPlanAprobado: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("commits = %d, want 1", len(results))
	}
	assertCommitPatchContains(t, results[0].Hash, "planned.txt", "+after")
	assertCommitPatchExcludes(t, results[0].Hash, "unrelated.txt", "must remain staged")

	staged, err := ejecutarGitSalida("diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if staged != "unrelated.txt\n" {
		t.Fatalf("staged unrelated paths = %q, want unrelated.txt only", staged)
	}
}

func TestApplyLegacyRouteOnlyPlanRejectsDetectedRenameOrCopy(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			name: "rename",
			setup: func(t *testing.T) {
				t.Helper()
				ejecutar(t, "mv", "old.go", "new.go")
			},
		},
		{
			name: "copy",
			setup: func(t *testing.T) {
				t.Helper()
				if err := writeSelectionTestFile("old.go", "rename me\nupdated\n"); err != nil {
					t.Fatal(err)
				}
				if err := writeSelectionTestFile("new.go", "rename me\n"); err != nil {
					t.Fatal(err)
				}
				ejecutar(t, "add", "--", "new.go")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepararRepoTemp(t)
			commitEnRepo(t, "old.go", "rename me\n")
			tt.setup(t)

			plan := &PlanSerializado{
				Lotes: []LoteSerializado{{
					Numero:  1,
					Capa:    "backend",
					Rutas:   []string{"new.go"},
					Lineas:  0,
					Mensaje: "chore(slice): apply a legacy route-only rename or copy",
				}},
			}
			state, err := HashEstadoWorktree([]string{"new.go"})
			if err != nil {
				t.Fatalf("HashEstadoWorktree: %v", err)
			}
			plan.EstadoWorktree = state
			if err := RecalculatePlanID(plan); err != nil {
				t.Fatalf("RecalculatePlanID: %v", err)
			}

			if err := writeSelectionTestFile("unrelated.txt", "must remain staged\n"); err != nil {
				t.Fatal(err)
			}
			ejecutar(t, "add", "--", "unrelated.txt")
			statusBefore, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall")
			if err != nil {
				t.Fatalf("git status before apply: %v", err)
			}
			stagedBefore, err := ejecutarGitSalida("diff", "--cached", "--name-only")
			if err != nil {
				t.Fatalf("git diff --cached before apply: %v", err)
			}
			commitsBefore := contarCommits(t)

			_, err = AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
			if !errors.Is(err, ErrLegacyRouteOnlyRename) {
				t.Fatalf("apply error = %v, want ErrLegacyRouteOnlyRename", err)
			}
			if contarCommits(t) != commitsBefore {
				t.Fatal("rejected legacy rename or copy created history")
			}
			statusAfter, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall")
			if err != nil {
				t.Fatalf("git status after apply: %v", err)
			}
			if statusAfter != statusBefore {
				t.Fatalf("rejected apply changed worktree status: before %q, after %q", statusBefore, statusAfter)
			}
			stagedAfter, err := ejecutarGitSalida("diff", "--cached", "--name-only")
			if err != nil {
				t.Fatalf("git diff --cached after apply: %v", err)
			}
			if stagedAfter != stagedBefore {
				t.Fatalf("rejected apply changed staged paths: before %q, after %q", stagedBefore, stagedAfter)
			}
		})
	}
}

func TestApplySelectionSupportsUnbornHead(t *testing.T) {
	tests := []struct {
		name  string
		stage bool
	}{
		{name: "untracked file"},
		{name: "staged file", stage: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepararRepoTemp(t)
			if err := writeSelectionTestFile("root.go", "package root\n"); err != nil {
				t.Fatal(err)
			}
			if tt.stage {
				ejecutar(t, "add", "--", "root.go")
			}

			plan, err := ConstruirPlanParaAgente()
			if err != nil {
				t.Fatalf("ConstruirPlanParaAgente: %v", err)
			}
			makeWholeFilePlan(t, plan)
			if err := ValidarAplicacion(plan, RespuestasPlan{PlanID: plan.PlanID}); err != nil {
				t.Fatalf("ValidarAplicacion: %v", err)
			}

			results, err := AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
			if err != nil {
				t.Fatalf("AplicarPlanAprobado: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("commits = %d, want 1", len(results))
			}
			parents, err := ejecutarGitSalida("rev-list", "--parents", "-n", "1", "HEAD")
			if err != nil {
				t.Fatalf("git rev-list root commit: %v", err)
			}
			if len(strings.Fields(parents)) != 1 {
				t.Fatalf("HEAD is not a root commit: %q", parents)
			}
			content, err := ejecutarGitSalida("show", "HEAD:root.go")
			if err != nil {
				t.Fatalf("git show root.go: %v", err)
			}
			if content != "package root\n" {
				t.Fatalf("root.go content = %q, want %q", content, "package root\n")
			}
			assertSelectionApplyClean(t)
		})
	}
}

func TestApplySelectionNormalizesIndexAfterMidApplyFailure(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n")
	if err := writeSelectionTestFile("app.go", "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nNINE\nten\n"); err != nil {
		t.Fatal(err)
	}

	plan := requireTwoHunkPlan(t)
	change := plan.Changes[0]
	plan.Lotes = []LoteSerializado{
		{
			Numero:    1,
			Capa:      "backend",
			Rutas:     []string{"app.go"},
			Selectors: []ChangeSelector{selectorForHunk(change, 0)},
			Lineas:    change.Atoms[0].AddedLines,
			Mensaje:   "feat(slice): commit the first hunk before failure",
		},
		{
			Numero:    2,
			Capa:      "backend",
			Rutas:     []string{"app.go"},
			Selectors: []ChangeSelector{selectorForHunk(change, 1)},
			Lineas:    change.Atoms[1].AddedLines,
			Mensaje:   "feat(slice): fail before the second hunk",
		},
	}
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}
	if err := writeSelectionTestFile("unrelated.txt", "must remain staged\n"); err != nil {
		t.Fatal(err)
	}
	ejecutar(t, "add", "--", "unrelated.txt")
	if err := ValidarAplicacion(plan, RespuestasPlan{PlanID: plan.PlanID}); err != nil {
		t.Fatalf("ValidarAplicacion: %v", err)
	}

	commitCalls := 0
	results, err := executeSelectionPlanWithCommit(deserializarPlan(plan), func(index, message string) (string, error) {
		commitCalls++
		if commitCalls == 2 {
			return "", errors.New("injected mid-apply failure")
		}
		return commitWithSelectionIndex(index, message)
	})
	if err == nil || !strings.Contains(err.Error(), "injected mid-apply failure") {
		t.Fatalf("apply error = %v, want deterministic mid-apply failure", err)
	}
	if commitCalls != 2 {
		t.Fatalf("commit calls = %d, want 2", commitCalls)
	}
	if len(results) != 1 {
		t.Fatalf("completed commits = %d, want 1", len(results))
	}
	assertCommitPatchContains(t, results[0].Hash, "app.go", "+TWO")

	staged, err := ejecutarGitSalida("diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if staged != "unrelated.txt\n" {
		t.Fatalf("staged paths = %q, want unrelated.txt only", staged)
	}
	plannedIndex, err := ejecutarGitSalida("diff", "--cached", "--", "app.go")
	if err != nil {
		t.Fatalf("git diff --cached app.go: %v", err)
	}
	if plannedIndex != "" {
		t.Fatalf("planned path remains staged after failure: %q", plannedIndex)
	}
	remainingWorktree, err := ejecutarGitSalida("diff", "--", "app.go")
	if err != nil {
		t.Fatalf("git diff app.go: %v", err)
	}
	if !strings.Contains(remainingWorktree, "+NINE") {
		t.Fatalf("remaining planned change was not left in the worktree: %q", remainingWorktree)
	}
}

func requireTwoHunkPlan(t *testing.T) *PlanSerializado {
	t.Helper()
	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("ConstruirPlanParaAgente: %v", err)
	}
	if len(plan.Changes) != 1 || len(plan.Changes[0].Atoms) != 2 {
		t.Fatalf("plan changes = %+v, want one two-hunk change", plan.Changes)
	}
	return plan
}

func makeWholeFilePlan(t *testing.T, plan *PlanSerializado) {
	t.Helper()
	for batchIndex := range plan.Lotes {
		batch := &plan.Lotes[batchIndex]
		batch.Selectors = nil
		batch.Lineas = 0
		for _, path := range batch.Rutas {
			change := findPlannedChange(t, plan.Changes, path)
			batch.Selectors = append(batch.Selectors, ChangeSelector{Path: change.Path, OldPath: change.OldPath, Mode: SelectorWholeFile})
			batch.Lineas += change.AddedLines
		}
	}
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}
}

func writeSelectionTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func assertCommitPatchContains(t *testing.T, commit, path, fragment string) {
	t.Helper()
	patch, err := ejecutarGitSalida("show", "--format=", "--no-ext-diff", commit, "--", path)
	if err != nil {
		t.Fatalf("git show %s: %v", commit, err)
	}
	if !strings.Contains(patch, fragment) {
		t.Fatalf("commit %s patch does not contain %q:\n%s", commit, fragment, patch)
	}
}

func assertCommitPatchExcludes(t *testing.T, commit, path, fragment string) {
	t.Helper()
	patch, err := ejecutarGitSalida("show", "--format=", "--no-ext-diff", commit, "--", path)
	if err != nil {
		t.Fatalf("git show %s: %v", commit, err)
	}
	if strings.Contains(patch, fragment) {
		t.Fatalf("commit %s patch unexpectedly contains %q:\n%s", commit, fragment, patch)
	}
}

func assertSelectionApplyClean(t *testing.T) {
	t.Helper()
	status, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if status != "" {
		t.Fatalf("worktree is not clean: %q", status)
	}
	staged, err := ejecutarGitSalida("diff", "--cached", "--name-only")
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if staged != "" {
		t.Fatalf("real index contains staged paths: %q", staged)
	}
}

func findPlannedChange(t *testing.T, changes []PlannedChange, path string) PlannedChange {
	t.Helper()
	for _, change := range changes {
		if change.Path == path {
			return change
		}
	}
	t.Fatalf("planned change %q not found in %+v", path, changes)
	return PlannedChange{}
}
