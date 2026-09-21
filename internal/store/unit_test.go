package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreComputeUnitIDDeterministic(t *testing.T) {
	id1 := ComputeUnitID("base1", "head1", "plan1")
	id2 := ComputeUnitID("base1", "head1", "plan1")
	if id1 != id2 {
		t.Errorf("ComputeUnitID is not deterministic: %s != %s", id1, id2)
	}
	// An empty plan_id is still a valid id, distinct from one with a plan.
	idWithoutPlan := ComputeUnitID("base1", "head1", "")
	if idWithoutPlan == id1 || idWithoutPlan == "" {
		t.Errorf("ComputeUnitID without plan_id should be valid and distinct: %s", idWithoutPlan)
	}
}

func TestStoreSaveAndReadUnit(t *testing.T) {
	s := NewStore(t.TempDir())
	id := ComputeUnitID("base1", "head1", "plan1")
	u := &Unit{ID: id, BaseTree: "base1", HeadTree: "head1", PlanID: "plan1", RunIDs: []string{"r1"}}

	if err := s.SaveUnit(u); err != nil {
		t.Fatalf("SaveUnit: %v", err)
	}
	got, err := s.ReadUnit(id)
	if err != nil {
		t.Fatalf("ReadUnit: %v", err)
	}
	if got == nil || got.BaseTree != "base1" || got.HeadTree != "head1" || len(got.RunIDs) != 1 {
		t.Errorf("read unit = %+v, does not match what was saved", got)
	}
}

func TestStoreReadUnitMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	u, err := s.ReadUnit("missing")
	if err != nil {
		t.Fatalf("ReadUnit: %v", err)
	}
	if u != nil {
		t.Error("ReadUnit should return nil for an id without a unit")
	}
}

func TestStoreUnitCorruptIsError(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	path := filepath.Join(dir, "vcsentinel", subdirUnits, "u1.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadUnit("u1"); err == nil {
		t.Error("a corrupt file should return an error, not nil")
	}
}
