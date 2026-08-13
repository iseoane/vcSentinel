package planning

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
)

func TestPlanProducesStableSerializablePlan(t *testing.T) {
	profileA := change.ChangeProfile{
		Base: "base", Head: "head", Kind: "feature",
		Size:        change.ChangeSize{Files: 2, Added: 20, Deleted: 3, Hunks: 4},
		Symbols:     change.ChangeSymbols{Added: 1, Modified: 2, ExportedTouched: 1, Complete: true},
		Modules:     []string{"internal/risk", "internal/change"},
		FileClasses: map[string]int{"test": 1, "source": 1},
	}
	profileB := profileA
	profileB.Modules = []string{"internal/change", "internal/risk"}
	profileB.FileClasses = map[string]int{"source": 1, "test": 1}
	modelA := CodeModel{
		SnapshotID: "tree", Paths: []string{"z.go", "a.go"},
		Packages: []string{"./internal/risk", "./internal/change"}, Tests: []string{"./internal/risk", "./internal/change"}, Complete: true,
	}
	modelB := modelA
	modelB.Paths = []string{"a.go", "z.go"}
	modelB.Packages = []string{"./internal/change", "./internal/risk"}
	modelB.Tests = []string{"./internal/change", "./internal/risk"}
	policyA := Policy{Name: "standard", Capabilities: []string{"test", "lint"}, Options: map[string]string{"timeout": "2m", "mode": "worktree"}}
	policyB := Policy{Name: "standard", Capabilities: []string{"lint", "test"}, Options: map[string]string{"mode": "worktree", "timeout": "2m"}}
	riskProfile := risk.Resultado{Nivel: risk.NivelStandard, Explicacion: "default risk"}

	first := Plan(profileA, riskProfile, modelA, policyA, StagePrePush)
	second := Plan(profileB, riskProfile, modelB, policyB, StagePrePush)

	if first.PlanID == "" || first.PlanID != second.PlanID {
		t.Fatalf("expected one stable non-empty plan ID, got %q and %q", first.PlanID, second.PlanID)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("expected equivalent canonical plans:\nfirst:  %#v\nsecond: %#v", first, second)
	}
	wantExplanation := `Plan for stage pre-push uses policy "standard" at risk standard: default risk.`
	if first.Explain != wantExplanation {
		t.Fatalf("expected explanation %q, got %q", wantExplanation, first.Explain)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	var decoded ExecutionPlan
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	if !reflect.DeepEqual(first, decoded) {
		t.Fatalf("serialization changed plan:\nfirst:   %#v\ndecoded: %#v", first, decoded)
	}
}

func TestPlanCanonicalizesEmptyMapsAsNil(t *testing.T) {
	riskProfile := risk.Resultado{Nivel: risk.NivelLow, Explicacion: "low risk"}
	model := CodeModel{SnapshotID: "tree", Complete: true}

	withNilMaps := Plan(
		change.ChangeProfile{Base: "base", Head: "head"},
		riskProfile,
		model,
		Policy{Name: "standard"},
		StagePreCommit,
	)
	withEmptyMaps := Plan(
		change.ChangeProfile{Base: "base", Head: "head", FileClasses: map[string]int{}},
		riskProfile,
		model,
		Policy{Name: "standard", Options: map[string]string{}},
		StagePreCommit,
	)

	if !reflect.DeepEqual(withNilMaps, withEmptyMaps) {
		t.Fatalf("expected nil and empty maps to produce identical plans:\nnil:   %#v\nempty: %#v", withNilMaps, withEmptyMaps)
	}
	for name, plan := range map[string]ExecutionPlan{"nil maps": withNilMaps, "empty maps": withEmptyMaps} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(plan)
			if err != nil {
				t.Fatalf("marshal plan: %v", err)
			}
			var decoded ExecutionPlan
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal plan: %v", err)
			}
			if !reflect.DeepEqual(plan, decoded) {
				t.Fatalf("serialization changed plan:\nplan:    %#v\ndecoded: %#v", plan, decoded)
			}
		})
	}
}

func TestPlanDoesNotMutateCallerInputs(t *testing.T) {
	profile := change.ChangeProfile{Modules: []string{"z", "a"}, FileClasses: map[string]int{"source": 1}}
	model := CodeModel{Paths: []string{"z.go", "a.go"}, Packages: []string{"./z", "./a"}, Tests: []string{"./z", "./a"}}
	policy := Policy{Capabilities: []string{"test", "lint"}, Options: map[string]string{"mode": "worktree"}}
	wantProfile := change.ChangeProfile{Modules: []string{"z", "a"}, FileClasses: map[string]int{"source": 1}}
	wantModel := CodeModel{Paths: []string{"z.go", "a.go"}, Packages: []string{"./z", "./a"}, Tests: []string{"./z", "./a"}}
	wantPolicy := Policy{Capabilities: []string{"test", "lint"}, Options: map[string]string{"mode": "worktree"}}

	plan := Plan(profile, risk.Resultado{}, model, policy, StagePreCommit)
	plan.ChangeProfile.Modules[0] = "changed"
	plan.ChangeProfile.FileClasses["source"] = 2
	plan.CodeModel.Paths[0] = "changed.go"
	plan.CodeModel.Packages[0] = "./changed"
	plan.CodeModel.Tests[0] = "./changed"
	plan.Policy.Capabilities[0] = "changed"
	plan.Policy.Options["mode"] = "changed"

	if !reflect.DeepEqual(profile, wantProfile) {
		t.Fatalf("Plan mutated caller profile: got %#v, want %#v", profile, wantProfile)
	}
	if !reflect.DeepEqual(model, wantModel) {
		t.Fatalf("Plan mutated caller code model: got %#v, want %#v", model, wantModel)
	}
	if !reflect.DeepEqual(policy, wantPolicy) {
		t.Fatalf("Plan mutated caller policy: got %#v, want %#v", policy, wantPolicy)
	}
}

func TestPlanIDChangesWithEachInputDomain(t *testing.T) {
	baseProfile := change.ChangeProfile{Base: "base", Head: "head", Kind: "feature"}
	baseRisk := risk.Resultado{Nivel: risk.NivelLow, Explicacion: "low risk"}
	baseModel := CodeModel{SnapshotID: "tree", Complete: true}
	basePolicy := Policy{Name: "standard"}
	base := Plan(baseProfile, baseRisk, baseModel, basePolicy, StagePreCommit).PlanID

	tests := []struct {
		name string
		plan ExecutionPlan
	}{
		{"change profile", Plan(change.ChangeProfile{Base: "base", Head: "other", Kind: "feature"}, baseRisk, baseModel, basePolicy, StagePreCommit)},
		{"risk profile", Plan(baseProfile, risk.Resultado{Nivel: risk.NivelHigh, Explicacion: "high risk"}, baseModel, basePolicy, StagePreCommit)},
		{"code model", Plan(baseProfile, baseRisk, CodeModel{SnapshotID: "other", Complete: true}, basePolicy, StagePreCommit)},
		{"policy", Plan(baseProfile, baseRisk, baseModel, Policy{Name: "strict"}, StagePreCommit)},
		{"stage", Plan(baseProfile, baseRisk, baseModel, basePolicy, StagePR)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.plan.PlanID == base {
				t.Fatalf("expected %s to affect plan ID", tt.name)
			}
		})
	}
}
