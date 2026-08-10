package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreCalcularUnitIDDeterminista(t *testing.T) {
	id1 := CalcularUnitID("base1", "head1", "plan1")
	id2 := CalcularUnitID("base1", "head1", "plan1")
	if id1 != id2 {
		t.Errorf("CalcularUnitID no es determinista: %s != %s", id1, id2)
	}
	// plan_id vacío sigue siendo un id válido y distinto de uno con plan.
	idSinPlan := CalcularUnitID("base1", "head1", "")
	if idSinPlan == id1 || idSinPlan == "" {
		t.Errorf("CalcularUnitID sin plan_id debería ser válido y distinto: %s", idSinPlan)
	}
}

func TestStoreGuardarYLeerUnit(t *testing.T) {
	s := NuevoStore(t.TempDir())
	id := CalcularUnitID("base1", "head1", "plan1")
	u := &Unit{ID: id, BaseTree: "base1", HeadTree: "head1", PlanID: "plan1", RunIDs: []string{"r1"}}

	if err := s.GuardarUnit(u); err != nil {
		t.Fatalf("GuardarUnit: %v", err)
	}
	leido, err := s.LeerUnit(id)
	if err != nil {
		t.Fatalf("LeerUnit: %v", err)
	}
	if leido == nil || leido.BaseTree != "base1" || leido.HeadTree != "head1" || len(leido.RunIDs) != 1 {
		t.Errorf("unit leída = %+v, no coincide con lo guardado", leido)
	}
}

func TestStoreLeerUnitInexistente(t *testing.T) {
	s := NuevoStore(t.TempDir())
	u, err := s.LeerUnit("noexiste")
	if err != nil {
		t.Fatalf("LeerUnit: %v", err)
	}
	if u != nil {
		t.Error("LeerUnit debería devolver nil para un id sin unit")
	}
}

func TestStoreUnitCorruptaEsError(t *testing.T) {
	dir := t.TempDir()
	s := NuevoStore(dir)
	ruta := filepath.Join(dir, "vas-sentinel", subdirUnits, "u1.json")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("no es json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeerUnit("u1"); err == nil {
		t.Error("un archivo corrupto debería devolver error, no nil")
	}
}
