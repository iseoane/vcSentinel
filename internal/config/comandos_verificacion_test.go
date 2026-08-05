package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestComandosVerificacion verifica el parseo de test_commands y
// build_commands (secciones hermanas de lint_commands) con la misma
// semántica de lista.
func TestComandosVerificacion(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
test_commands:
  - "go test ./..."
  - "go test -race ./internal/review"
build_commands:
  - "go build ./..."
`)

	cfg := CargarConfiguracionLocal(worktree)

	esperadoTest := []string{"go test ./...", "go test -race ./internal/review"}
	if !reflect.DeepEqual(cfg.TestCommands, esperadoTest) {
		t.Errorf("TestCommands = %+v, esperado %+v", cfg.TestCommands, esperadoTest)
	}
	esperadoBuild := []string{"go build ./..."}
	if !reflect.DeepEqual(cfg.BuildCommands, esperadoBuild) {
		t.Errorf("BuildCommands = %+v, esperado %+v", cfg.BuildCommands, esperadoBuild)
	}
	if len(cfg.LintCommands) != 0 {
		t.Errorf("LintCommands = %+v, esperado vacío (sin lint configurado)", cfg.LintCommands)
	}
}

// TestComandosVerificacionDefaults verifica que sin configuración las listas
// de verificación están vacías (nada se ejecuta por defecto).
func TestComandosVerificacionDefaults(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := CargarConfiguracionLocal(worktree)
	if len(cfg.TestCommands) != 0 || len(cfg.BuildCommands) != 0 {
		t.Errorf("defaults de verificación = test:%v build:%v, esperado vacíos",
			cfg.TestCommands, cfg.BuildCommands)
	}
}

// TestComandosVerificacionPrecedencia verifica el merge global + per-proyecto:
// los comandos per-proyecto se acumulan a los globales (misma semántica que
// lint_commands).
func TestComandosVerificacionPrecedencia(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"), `
test_commands:
  - "go test ./..."
`)
	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
test_commands:
  - "go test -race ./internal/review"
`)

	cfg := CargarConfiguracionLocal(worktree)
	encontrados := 0
	for _, cmd := range cfg.TestCommands {
		if cmd == "go test ./..." || cmd == "go test -race ./internal/review" {
			encontrados++
		}
	}
	if encontrados != 2 {
		t.Errorf("TestCommands = %+v, esperado merge global + per-proyecto", cfg.TestCommands)
	}
}
