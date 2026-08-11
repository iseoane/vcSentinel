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

// TestRegistrarBlobsCommitFusionaConjuntosDistintos cubre la corrección de
// T2.7: dos llamadas sobre el mismo sha con conjuntos de blobs SIN
// solapamiento deben acumularse (unión), no que la segunda pise a la
// primera. Antes del fix, idx.Blobs = blobs sustituía el mapa completo y la
// entrada de la primera llamada quedaba huérfana en blobs/<blob>.json.
func TestRegistrarBlobsCommitFusionaConjuntosDistintos(t *testing.T) {
	s := NuevoStore(t.TempDir())

	if err := s.RegistrarBlobsCommit("sha1", map[string]string{"a.go": "blobA"}); err != nil {
		t.Fatalf("primera llamada: %v", err)
	}
	if err := s.RegistrarBlobsCommit("sha1", map[string]string{"b.go": "blobB"}); err != nil {
		t.Fatalf("segunda llamada: %v", err)
	}

	leido, err := s.LeerIndiceCommit("sha1")
	if err != nil {
		t.Fatalf("LeerIndiceCommit: %v", err)
	}
	if leido == nil || len(leido.Blobs) != 2 {
		t.Fatalf("Blobs = %+v, esperado la unión de ambas llamadas (2 entradas)", leido)
	}
	if leido.Blobs["a.go"] != "blobA" || leido.Blobs["b.go"] != "blobB" {
		t.Errorf("Blobs = %+v, esperado {a.go:blobA, b.go:blobB}", leido.Blobs)
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
