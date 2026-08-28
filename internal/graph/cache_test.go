package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestGuardarCachePurgaEntradasCaducadas cubre código de limpieza que existía
// escrito y probado pero que NADIE llamaba: PurgarCacheGrafo no tenía un solo
// llamante en producción, así que la caché del grafo crecía sin límite.
//
// El patrón es el que ya usa PurgarSnapshots, cableada en
// internal/validation/candidato.go: purgar al usar, para que el residuo quede
// acotado por el uso en vez de crecer hasta que algo se rompa.
func TestGuardarCachePurgaEntradasCaducadas(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}
	directorio := t.TempDir()
	if out, err := exec.Command("git", "-C", directorio, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	raiz, err := raizCache(directorio)
	if err != nil {
		t.Fatal(err)
	}
	raiz.Close()

	base := filepath.Join(directorio, ".git", "vas-sentinel", "graph")
	// La purga solo retira ficheros <oid>.json con OID válido, así que el
	// fixture usa la forma real.
	caducada := filepath.Join(base, "0123456789abcdef0123456789abcdef01234567.json")
	if err := os.WriteFile(caducada, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	antiguo := time.Now().Add(-RetencionCacheGrafo - time.Hour)
	if err := os.Chtimes(caducada, antiguo, antiguo); err != nil {
		t.Fatal(err)
	}

	p := &proveedorNativo{directorio: directorio, identidad: "oid-de-prueba"}
	if err := p.guardarCache(nil); err != nil {
		t.Fatalf("guardarCache: %v", err)
	}

	if _, err := os.Stat(caducada); !os.IsNotExist(err) {
		t.Error("la entrada caducada sigue en la caché: la purga no se ejecutó al guardar")
	}
}
