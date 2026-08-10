package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreGuardarYLeerIndiceCommit(t *testing.T) {
	s := NuevoStore(t.TempDir())
	idx := &IndiceCommit{SHA: "abc123", Message: "feat(x)", Fingerprints: []string{"fp1", "fp2"}}

	if err := s.GuardarIndiceCommit(idx); err != nil {
		t.Fatalf("GuardarIndiceCommit: %v", err)
	}
	leido, err := s.LeerIndiceCommit("abc123")
	if err != nil {
		t.Fatalf("LeerIndiceCommit: %v", err)
	}
	if leido == nil || leido.Message != "feat(x)" || len(leido.Fingerprints) != 2 {
		t.Errorf("indice leído = %+v, no coincide con lo guardado", leido)
	}
}

func TestStoreLeerIndiceCommitInexistente(t *testing.T) {
	s := NuevoStore(t.TempDir())
	idx, err := s.LeerIndiceCommit("noexiste")
	if err != nil {
		t.Fatalf("LeerIndiceCommit: %v", err)
	}
	if idx != nil {
		t.Error("LeerIndiceCommit debería devolver nil para un sha sin indice")
	}
}

func TestStoreIndiceCommitCorruptoEsError(t *testing.T) {
	dir := t.TempDir()
	s := NuevoStore(dir)
	ruta := filepath.Join(dir, "vas-sentinel", subdirCommits, "abc123.json")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("no es json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeerIndiceCommit("abc123"); err == nil {
		t.Error("un archivo corrupto debería devolver error, no nil")
	}
}
