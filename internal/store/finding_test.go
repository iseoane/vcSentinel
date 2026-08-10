package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func TestStoreGuardarYLeerHallazgo(t *testing.T) {
	s := NuevoStore(t.TempDir())
	h := &review.Hallazgo{Fingerprint: "fp1", Title: "algo", Severity: "critical"}

	if err := s.GuardarHallazgo(h); err != nil {
		t.Fatalf("GuardarHallazgo: %v", err)
	}
	leido, err := s.LeerHallazgo("fp1")
	if err != nil {
		t.Fatalf("LeerHallazgo: %v", err)
	}
	if leido == nil || leido.Title != "algo" || leido.Severity != "critical" {
		t.Errorf("hallazgo leído = %+v, no coincide con lo guardado", leido)
	}
}

func TestStoreLeerHallazgoInexistente(t *testing.T) {
	s := NuevoStore(t.TempDir())
	h, err := s.LeerHallazgo("noexiste")
	if err != nil {
		t.Fatalf("LeerHallazgo: %v", err)
	}
	if h != nil {
		t.Error("LeerHallazgo debería devolver nil para un fingerprint sin hallazgo")
	}
}

func TestStoreHallazgoCorruptoEsError(t *testing.T) {
	dir := t.TempDir()
	s := NuevoStore(dir)
	ruta := filepath.Join(dir, "vas-sentinel", subdirFindings, "fp1.json")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("no es json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeerHallazgo("fp1"); err == nil {
		t.Error("un archivo corrupto debería devolver error, no nil")
	}
}
