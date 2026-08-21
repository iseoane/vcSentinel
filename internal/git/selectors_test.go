package git

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSlicePlanSerializesWholeFileCoverage(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "package app\n")
	if err := os.WriteFile("app.go", []byte("package app\n\nfunc New() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("ConstruirPlanParaAgente: %v", err)
	}
	if err := ValidateSerializedPlan(plan); err != nil {
		t.Fatalf("ValidateSerializedPlan: %v", err)
	}
	if len(plan.Changes) != 1 || len(plan.Changes[0].Atoms) != 1 {
		t.Fatalf("captured changes = %+v, want one file atom", plan.Changes)
	}
	if len(plan.Lotes) != 1 || len(plan.Lotes[0].Selectors) != 1 {
		t.Fatalf("serialized batches = %+v, want one whole-file selector", plan.Lotes)
	}
	selector := plan.Lotes[0].Selectors[0]
	if selector.Mode != SelectorWholeFile || selector.Path != "app.go" {
		t.Fatalf("selector = %+v, want whole app.go", selector)
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PlanSerializado
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, &decoded) {
		t.Fatalf("JSON round trip changed the selection-aware plan")
	}
}

func TestSlicePlanCanSelectMultipleHunksInOneFile(t *testing.T) {
	prepararRepoTemp(t)
	base := "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"
	commitEnRepo(t, "app.go", base)
	updated := "one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nNINE\nten\n"
	if err := os.WriteFile("app.go", []byte(updated), 0644); err != nil {
		t.Fatal(err)
	}

	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatalf("ConstruirPlanParaAgente: %v", err)
	}
	change := plan.Changes[0]
	if len(change.Atoms) != 2 {
		t.Fatalf("atoms = %+v, want two non-adjacent hunks", change.Atoms)
	}

	plan.Lotes[0].Selectors = []ChangeSelector{
		selectorForHunk(change, 0),
		selectorForHunk(change, 1),
	}
	plan.Lotes[0].Lineas = change.AddedLines
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}
	if err := ValidateSerializedPlan(plan); err != nil {
		t.Fatalf("ValidateSerializedPlan: %v", err)
	}
	if plan.Lotes[0].Selectors[0].Mode != SelectorHunk || plan.Lotes[0].Selectors[1].HunkIndex != 1 {
		t.Fatalf("selectors = %+v, want two exact hunk selectors", plan.Lotes[0].Selectors)
	}
}

func TestSlicePlanIDIncludesSelectorsAndState(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\n")
	if err := os.WriteFile("app.go", []byte("ONE\ntwo\nthree\nFOUR\n"), 0644); err != nil {
		t.Fatal(err)
	}

	whole, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatal(err)
	}
	second, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatal(err)
	}
	if whole.PlanID != second.PlanID {
		t.Fatalf("repeated plan IDs differ: %s and %s", whole.PlanID, second.PlanID)
	}

	hunks := *whole
	hunks.Lotes = append([]LoteSerializado(nil), whole.Lotes...)
	hunks.Lotes[0].Selectors = []ChangeSelector{
		selectorForHunk(whole.Changes[0], 0),
		selectorForHunk(whole.Changes[0], 1),
	}
	hunks.Lotes[0].Lineas = whole.Changes[0].AddedLines
	if err := RecalculatePlanID(&hunks); err != nil {
		t.Fatalf("RecalculatePlanID: %v", err)
	}
	if whole.PlanID == hunks.PlanID {
		t.Fatal("whole-file and hunk selections must have different plan IDs")
	}

	stateChanged := hunks
	stateChanged.EstadoWorktree = "different-state"
	if err := RecalculatePlanID(&stateChanged); err != nil {
		t.Fatalf("RecalculatePlanID with changed state: %v", err)
	}
	if whole.PlanID == stateChanged.PlanID {
		t.Fatal("plan ID must include the bound state fingerprint")
	}
}

func TestSlicePlanRejectsMissingOverlappingAndCorruptSelections(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\n")
	if err := os.WriteFile("app.go", []byte("ONE\ntwo\nthree\nFOUR\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatal(err)
	}
	change := plan.Changes[0]

	tests := []struct {
		name      string
		selectors []ChangeSelector
		want      string
	}{
		{
			name:      "missing hunk",
			selectors: []ChangeSelector{selectorForHunk(change, 0)},
			want:      "reports",
		},
		{
			name: "overlapping hunk",
			selectors: []ChangeSelector{
				selectorForHunk(change, 0),
				selectorForHunk(change, 0),
			},
			want: "selects atom",
		},
		{
			name: "corrupt hunk",
			selectors: []ChangeSelector{
				func() ChangeSelector {
					selector := selectorForHunk(change, 0)
					selector.Hunk.PatchHash = "corrupt"
					return selector
				}(),
				selectorForHunk(change, 1),
			},
			want: "does not match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := *plan
			candidate.Lotes = append([]LoteSerializado(nil), plan.Lotes...)
			candidate.Lotes[0].Selectors = tt.selectors
			candidate.Lotes[0].Lineas = change.AddedLines
			if err := ValidatePlanSelections(&candidate); !errors.Is(err, ErrInvalidPlan) || !containsError(err, tt.want) {
				t.Fatalf("validation error = %v, want ErrInvalidPlan containing %q", err, tt.want)
			}
		})
	}
}

func TestSlicePlanRejectsStaleWorktreeAndIndexState(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\n")
	if err := os.WriteFile("app.go", []byte("ONE\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ejecutarGitSalida("add", "--", "app.go"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("app.go", []byte("ONE\ntwo\nTHREE\n"), 0644); err != nil {
		t.Fatal(err)
	}

	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatal(err)
	}
	if plan.Changes[0].IndexHash == plan.Changes[0].WorktreeHash {
		t.Fatal("partially staged plan must preserve different index and worktree fingerprints")
	}
	if err := ejecutarGitExit("reset", "HEAD", "--", "app.go"); err != nil {
		t.Fatal(err)
	}

	err = ValidarAplicacion(plan, RespuestasPlan{PlanID: plan.PlanID})
	if !errors.Is(err, ErrArbolCambiado) {
		t.Fatalf("stale index validation error = %v, want ErrArbolCambiado", err)
	}

	if err := os.WriteFile("app.go", []byte("ONE\nTWO\nTHREE\n"), 0644); err != nil {
		t.Fatal(err)
	}
	err = ValidarAplicacion(plan, RespuestasPlan{PlanID: plan.PlanID})
	if !errors.Is(err, ErrArbolCambiado) {
		t.Fatalf("stale worktree validation error = %v, want ErrArbolCambiado", err)
	}
}

func TestSliceApplyRejectsHunkPlanBeforeHistoryMutation(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "app.go", "one\ntwo\nthree\nfour\n")
	if err := os.WriteFile("app.go", []byte("ONE\ntwo\nthree\nFOUR\n"), 0644); err != nil {
		t.Fatal(err)
	}
	plan, err := ConstruirPlanParaAgente()
	if err != nil {
		t.Fatal(err)
	}
	change := plan.Changes[0]
	plan.Lotes[0].Selectors = []ChangeSelector{
		selectorForHunk(change, 0),
		selectorForHunk(change, 1),
	}
	plan.Lotes[0].Lineas = change.AddedLines
	if err := RecalculatePlanID(plan); err != nil {
		t.Fatal(err)
	}
	commitsBefore := contarCommits(t)

	_, err = AplicarPlanAprobado(plan, RespuestasPlan{PlanID: plan.PlanID})
	if !errors.Is(err, ErrSelectionApplyUnsupported) {
		t.Fatalf("apply error = %v, want ErrSelectionApplyUnsupported", err)
	}
	if commitsBefore != contarCommits(t) {
		t.Fatal("rejecting a hunk-aware plan must not create history")
	}
}

func TestSlicePlanRepresentsRequestedGitChangeKinds(t *testing.T) {
	tests := []struct {
		name string
		make func(t *testing.T, dir string)
		want func(t *testing.T, changes []PlannedChange)
	}{
		{
			name: "tracked",
			make: func(t *testing.T, _ string) {
				if err := os.WriteFile("tracked.go", []byte("changed\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, changes []PlannedChange) {
				assertChangeKind(t, changes, "tracked.go", "M", ChangeText)
			},
		},
		{
			name: "untracked",
			make: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package new\n"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, changes []PlannedChange) {
				assertChangeKind(t, changes, "new.go", "A", ChangeText)
			},
		},
		{
			name: "deleted",
			make: func(t *testing.T, _ string) {
				if err := os.Remove("deleted.go"); err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, changes []PlannedChange) {
				change := assertChangeKind(t, changes, "deleted.go", "D", ChangeText)
				if change.WorktreeHash != absentFingerprint || change.HeadHash == absentFingerprint {
					t.Fatalf("deleted fingerprints = %+v", change)
				}
			},
		},
		{
			name: "renamed",
			make: func(t *testing.T, _ string) {
				if _, err := ejecutarGitSalida("mv", "old.go", "new-name.go"); err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, changes []PlannedChange) {
				change := assertChangeKind(t, changes, "new-name.go", "R", ChangeRename)
				if change.OldPath != "old.go" || len(change.Atoms) != 1 {
					t.Fatalf("rename = %+v", change)
				}
			},
		},
		{
			name: "binary",
			make: func(t *testing.T, _ string) {
				if err := os.WriteFile("image.bin", []byte{0, 1, 2, 3, 0xff}, 0644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(t *testing.T, changes []PlannedChange) {
				change := assertChangeKind(t, changes, "image.bin", "M", ChangeBinary)
				if len(change.Atoms) != 1 || change.Atoms[0].Kind != changeAtomBinary {
					t.Fatalf("binary atoms = %+v", change.Atoms)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := prepararRepoTemp(t)
			commitEnRepo(t, "tracked.go", "original\n")
			commitEnRepo(t, "deleted.go", "to delete\n")
			commitEnRepo(t, "old.go", "rename me\n")
			commitEnRepo(t, "image.bin", "binary base\n")
			tt.make(t, dir)
			changes, err := CaptureDraftChanges()
			if err != nil {
				t.Fatalf("CaptureDraftChanges: %v", err)
			}
			tt.want(t, changes)
		})
	}
}

func TestSlicePlanRejectsHunkSelectionForRenames(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "old.go", "rename me\n")
	if _, err := ejecutarGitSalida("mv", "old.go", "new.go"); err != nil {
		t.Fatal(err)
	}
	changes, err := CaptureDraftChanges()
	if err != nil {
		t.Fatal(err)
	}
	plan := &PlanSerializado{
		EstadoWorktree: "state",
		Changes:        changes,
		Lotes: []LoteSerializado{{
			Numero: 1, Rutas: []string{"new.go"}, Lineas: 0,
			Selectors: []ChangeSelector{{Path: "new.go", OldPath: "old.go", Mode: SelectorHunk, HunkIndex: 0, AtomID: changes[0].Atoms[0].ID}},
		}},
	}
	if err := ValidatePlanSelections(plan); !errors.Is(err, ErrInvalidPlan) || !containsError(err, "cannot select a hunk") {
		t.Fatalf("rename hunk validation error = %v", err)
	}
}

func selectorForHunk(change PlannedChange, index int) ChangeSelector {
	atom := change.Atoms[index]
	hunk := *atom.Hunk
	return ChangeSelector{
		Path:      change.Path,
		OldPath:   change.OldPath,
		Mode:      SelectorHunk,
		AtomID:    atom.ID,
		HunkIndex: index,
		Hunk:      &hunk,
	}
}

func assertChangeKind(t *testing.T, changes []PlannedChange, path, status string, kind ChangeKind) PlannedChange {
	t.Helper()
	for _, change := range changes {
		if change.Path == path {
			if change.Status != status || change.Kind != kind {
				t.Fatalf("change %q = %+v, want status %s and kind %s", path, change, status, kind)
			}
			return change
		}
	}
	t.Fatalf("change %q not found in %+v", path, changes)
	return PlannedChange{}
}

func containsError(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}

func ejecutarGitExit(args ...string) error {
	_, err := ejecutarGitSalida(args...)
	return err
}
