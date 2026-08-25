package reviewcontract

import (
	"reflect"
	"testing"
)

func TestRegistryDefinesEveryCanonicalDimension(t *testing.T) {
	want := map[string]string{
		DimensionLogic:    "Correctness of behavior: conditions, error handling, edge cases and side effects.",
		DimensionStyle:    "Language and clarity: naming, idiomatic code and adherence to repository conventions.",
		DimensionDesign:   "Structure and coupling: cohesion, open/closed principle, deep modules and dependencies pointing toward the domain.",
		DimensionTests:    "Test value: meaningful coverage, determinism and a failing test implying a real behavior change.",
		DimensionSecurity: "Privilege boundaries, untrusted input handling and exposed data.",
		DimensionSpec:     "The diff does exactly what the commit message claims: no out-of-scope work and no unbacked claims.",
	}

	for name, instructions := range want {
		t.Run(name, func(t *testing.T) {
			contract, err := Lookup(name)
			if err != nil {
				t.Fatalf("Lookup(%q) error = %v", name, err)
			}
			if contract.Name != name || contract.Instructions != instructions {
				t.Fatalf("contract = %#v", contract)
			}
			if !reflect.DeepEqual(contract.OutputSchema, canonicalOutputSchema) {
				t.Errorf("schema = %#v, want %#v", contract.OutputSchema, canonicalOutputSchema)
			}
			if contract.EvidencePolicy != canonicalEvidencePolicy {
				t.Errorf("evidence policy = %#v, want %#v", contract.EvidencePolicy, canonicalEvidencePolicy)
			}
			if contract.ToolPolicy != canonicalToolPolicy {
				t.Errorf("tool policy = %#v, want %#v", contract.ToolPolicy, canonicalToolPolicy)
			}
		})
	}
}

func TestRegistryRejectsUnknownDimension(t *testing.T) {
	if _, err := Lookup("performance"); err == nil {
		t.Fatal("Lookup() error = nil, want invalid dimension rejection")
	}
}

func TestRegistryReturnsCopies(t *testing.T) {
	first, err := Lookup(DimensionLogic)
	if err != nil {
		t.Fatal(err)
	}
	first.OutputSchema.TopLevelFields[0] = "changed"
	second, err := Lookup(DimensionLogic)
	if err != nil {
		t.Fatal(err)
	}
	if second.OutputSchema.TopLevelFields[0] != "dim" {
		t.Fatalf("registry leaked mutable schema: %#v", second.OutputSchema)
	}
}
