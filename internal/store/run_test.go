package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreGuardarYLeerRun(t *testing.T) {
	s := NuevoStore(t.TempDir())
	at := time.Now().UTC()
	id := CalcularRunID("unit1", at)
	r := &Run{ID: id, UnitID: "unit1", At: at, Fingerprints: []string{"fp1"}}

	if err := s.GuardarRun(r); err != nil {
		t.Fatalf("GuardarRun: %v", err)
	}
	leido, err := s.LeerRun(id)
	if err != nil {
		t.Fatalf("LeerRun: %v", err)
	}
	if leido == nil || leido.UnitID != "unit1" || len(leido.Fingerprints) != 1 {
		t.Errorf("run leído = %+v, no coincide con lo guardado", leido)
	}
}

func TestStoreLeerRunInexistente(t *testing.T) {
	s := NuevoStore(t.TempDir())
	r, err := s.LeerRun("noexiste")
	if err != nil {
		t.Fatalf("LeerRun: %v", err)
	}
	if r != nil {
		t.Error("LeerRun debería devolver nil para un id sin run")
	}
}

func TestStoreRunCorruptoEsError(t *testing.T) {
	dir := t.TempDir()
	s := NuevoStore(dir)
	ruta := filepath.Join(dir, "vas-sentinel", subdirRuns, "r1.json")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("no es json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeerRun("r1"); err == nil {
		t.Error("un archivo corrupto debería devolver error, no nil")
	}
}
