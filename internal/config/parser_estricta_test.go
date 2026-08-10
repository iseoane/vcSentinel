package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCargarConfiguracionLocalEstricta_ClaveDesconocida_DevuelveErrorConLinea
// cubre el requisito añadido por el orquestador a T1.7 (fuera del texto
// original de la ficha): una clave desconocida en el yml per-proyecto ya no
// se descarta en silencio como hace CargarConfiguracionLocal, y el error
// incluye la línea donde ocurre (T1.1).
func TestCargarConfiguracionLocalEstricta_ClaveDesconocida_DevuelveErrorConLinea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"active_agent: \"claude\"\nclave_inexistente: true\n")

	_, err := CargarConfiguracionLocalEstricta(worktree)
	if err == nil {
		t.Fatal("se esperaba un error por la clave desconocida, obtuve nil")
	}
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("el error debe incluir la línea del yml, obtuve: %v", err)
	}
}

// TestCargarConfiguracionLocalEstricta_ConfigValida_NoRompeNada verifica que,
// sin errores en el yml, el resultado es idéntico al de CargarConfiguracionLocal.
func TestCargarConfiguracionLocalEstricta_ConfigValida_NoRompeNada(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"active_agent: \"opencode\"\n")

	cfg, err := CargarConfiguracionLocalEstricta(worktree)
	if err != nil {
		t.Fatalf("no se esperaba error: %v", err)
	}
	if cfg.ActiveAgent != "opencode" {
		t.Errorf("ActiveAgent esperado 'opencode', obtuve %q", cfg.ActiveAgent)
	}
}
