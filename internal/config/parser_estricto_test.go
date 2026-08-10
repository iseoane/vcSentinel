package config

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestClaveDesconocidaFallaConLinea verifica que una clave que no pertenece
// al esquema (p. ej. "comand" en vez de "command") no se ignora en silencio:
// la decodificación estricta (yaml.Decoder.KnownFields) debe devolver un
// error que incluya la línea exacta donde aparece la clave inválida. Para un
// gate de guardián, un fallo silencioso es el peor modo de fallo posible.
func TestClaveDesconocidaFallaConLinea(t *testing.T) {
	worktree := t.TempDir()
	ruta := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	// La clave inventada "comand" está en la línea 4 (1-based, contando desde
	// la primera línea del archivo).
	escribirConfig(t, ruta, `active_agent: "claude"
lint_commands:
  - "gofmt -l ."
comand: "typo"
`)

	cfg := configuracionPorDefecto()
	err := aplicarDesdeRuta(&cfg, ruta)
	if err == nil {
		t.Fatal("se esperaba un error por la clave desconocida 'comand', obtuve nil")
	}

	patronLinea := regexp.MustCompile(`line 4\b`)
	if !patronLinea.MatchString(err.Error()) {
		t.Errorf("el error no menciona la línea 4 esperada: %v", err)
	}
	if !filepath.IsAbs(ruta) {
		t.Fatalf("ruta de prueba mal construida: %q", ruta)
	}
}
