package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fichaV1 es una ficha escrita antes de T0.2: sin agent, model ni effort en
// la revisión. Debe seguir leyéndose sin error.
const fichaV1 = `{
  "sha": "6c079a8",
  "message": "feat: algo",
  "bucket": "backend",
  "model": "default",
  "revisions": [
    {"at": "2026-01-15T10:00:00Z", "result": "ok", "dims": [{"dim": "seguridad", "verdict": "ok"}]}
  ]
}`

// TestLeerFichaV1SinCamposDeAutoria cubre la aceptación de compatibilidad de
// T0.2: los campos nuevos son opcionales.
func TestLeerFichaV1SinCamposDeAutoria(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "vas-sentinel")
	if err := os.MkdirAll(ruta, 0755); err != nil {
		t.Fatalf("no se pudo crear el directorio: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ruta, "6c079a8.json"), []byte(fichaV1), 0644); err != nil {
		t.Fatalf("no se pudo escribir la ficha: %v", err)
	}

	ledger := NuevoLedger(dir)
	ficha, err := ledger.LeerFicha("6c079a8")
	if err != nil {
		t.Fatalf("una ficha v1 debe leerse sin error: %v", err)
	}
	if ficha == nil {
		t.Fatal("no se leyó la ficha")
	}
	if len(ficha.Revisions) != 1 {
		t.Fatalf("revisiones = %d, esperado 1", len(ficha.Revisions))
	}
	revision := ficha.Revisions[0]
	if revision.Result != "ok" {
		t.Errorf("result = %q, esperado ok", revision.Result)
	}
	if revision.Agent != "" || revision.Model != "" || revision.Effort != "" {
		t.Errorf("una ficha v1 no debe inventar autoría: %+v", revision)
	}
}

// TestRevisionSinAutoriaNoEscribeLosCampos: los campos vacíos no aparecen en
// el JSON, así que una ficha nueva sin autor sigue siendo idéntica a una v1.
func TestRevisionSinAutoriaNoEscribeLosCampos(t *testing.T) {
	datos, err := json.Marshal(Revision{At: time.Now(), Result: "ok"})
	if err != nil {
		t.Fatalf("no se pudo serializar: %v", err)
	}
	for _, campo := range []string{"agent", "model", "effort"} {
		if strings.Contains(string(datos), `"`+campo+`"`) {
			t.Errorf("el campo %q vacío no debería serializarse: %s", campo, datos)
		}
	}
}

// TestRevisionConAutoriaSeSerializa: cuando hay autor, se guarda.
func TestRevisionConAutoriaSeSerializa(t *testing.T) {
	datos, err := json.Marshal(Revision{Result: "ok", Agent: "opencode", Model: "sonnet", Effort: "high"})
	if err != nil {
		t.Fatalf("no se pudo serializar: %v", err)
	}
	for _, esperado := range []string{`"agent":"opencode"`, `"model":"sonnet"`, `"effort":"high"`} {
		if !strings.Contains(string(datos), esperado) {
			t.Errorf("falta %s en: %s", esperado, datos)
		}
	}
}
