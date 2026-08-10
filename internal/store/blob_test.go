package store

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func TestYaRevisadoBlobNuncaVisto(t *testing.T) {
	s := NuevoStore(t.TempDir())

	revisado, hallazgos, err := s.YaRevisado("blob-nunca-visto")
	if err != nil {
		t.Fatalf("YaRevisado: %v", err)
	}
	if revisado {
		t.Error("YaRevisado = true para un blob que nunca se registró")
	}
	if hallazgos != nil {
		t.Errorf("hallazgos = %v, esperado nil", hallazgos)
	}
}

// TestYaRevisadoBlobLimpioSinHallazgos cubre la distinción clave: un blob
// que sí apareció en un IndiceCommit, pero sin fingerprints asociados (un
// commit limpio, sin hallazgos), debe contar como revisado=true. Si no fuera
// así, un archivo sin problemas se re-auditaría siempre.
func TestYaRevisadoBlobLimpioSinHallazgos(t *testing.T) {
	s := NuevoStore(t.TempDir())
	idx := &IndiceCommit{SHA: "sha-limpio", Blobs: map[string]string{"a.go": "blob-limpio"}}
	if err := s.GuardarIndiceCommit(idx); err != nil {
		t.Fatalf("GuardarIndiceCommit: %v", err)
	}

	revisado, hallazgos, err := s.YaRevisado("blob-limpio")
	if err != nil {
		t.Fatalf("YaRevisado: %v", err)
	}
	if !revisado {
		t.Error("YaRevisado = false para un blob que sí apareció en un IndiceCommit (limpio)")
	}
	if len(hallazgos) != 0 {
		t.Errorf("hallazgos = %+v, esperado vacío (blob limpio)", hallazgos)
	}
}

// TestYaRevisadoBlobConHallazgos cubre el caso con hallazgos v2 reales
// asociados al blob: deben resolverse por fingerprint y devolverse.
func TestYaRevisadoBlobConHallazgos(t *testing.T) {
	s := NuevoStore(t.TempDir())
	h := &review.Hallazgo{
		Fingerprint: "fp-blob-con-hallazgo",
		Title:       "algo",
		Severity:    "high",
		Location:    review.Ubicacion{Archivo: "a.go", Blob: "blob-con-hallazgo"},
	}
	if err := s.GuardarHallazgo(h); err != nil {
		t.Fatalf("GuardarHallazgo: %v", err)
	}
	idx := &IndiceCommit{
		SHA:          "sha-con-hallazgo",
		Fingerprints: []string{"fp-blob-con-hallazgo"},
		Blobs:        map[string]string{"a.go": "blob-con-hallazgo"},
	}
	if err := s.GuardarIndiceCommit(idx); err != nil {
		t.Fatalf("GuardarIndiceCommit: %v", err)
	}

	revisado, hallazgos, err := s.YaRevisado("blob-con-hallazgo")
	if err != nil {
		t.Fatalf("YaRevisado: %v", err)
	}
	if !revisado {
		t.Error("YaRevisado = false para un blob con hallazgo asociado")
	}
	if len(hallazgos) != 1 || hallazgos[0].Fingerprint != "fp-blob-con-hallazgo" {
		t.Errorf("hallazgos = %+v, esperado [fp-blob-con-hallazgo]", hallazgos)
	}
}

// TestRegistrarBlobsCommitPreservaFingerprints: RegistrarBlobsCommit no debe
// pisar los Fingerprints/V1 que ya tuviera el IndiceCommit de ese sha.
func TestRegistrarBlobsCommitPreservaFingerprints(t *testing.T) {
	s := NuevoStore(t.TempDir())
	idx := &IndiceCommit{SHA: "sha1", Fingerprints: []string{"fp1"}, Message: "feat(x)"}
	if err := s.GuardarIndiceCommit(idx); err != nil {
		t.Fatalf("GuardarIndiceCommit: %v", err)
	}

	if err := s.RegistrarBlobsCommit("sha1", map[string]string{"a.go": "blob1"}); err != nil {
		t.Fatalf("RegistrarBlobsCommit: %v", err)
	}

	leido, err := s.LeerIndiceCommit("sha1")
	if err != nil {
		t.Fatalf("LeerIndiceCommit: %v", err)
	}
	if leido == nil || len(leido.Fingerprints) != 1 || leido.Fingerprints[0] != "fp1" {
		t.Errorf("Fingerprints no preservados: %+v", leido)
	}
	if leido.Blobs["a.go"] != "blob1" {
		t.Errorf("Blobs = %+v, esperado a.go->blob1", leido.Blobs)
	}
}
