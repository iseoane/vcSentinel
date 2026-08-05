package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
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

// listaYAML serializa items como lista de strings YAML (con comillas dobles,
// como el resto de los fixtures).
func listaYAML(items []string) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString("  - " + strconv.Quote(it) + "\n")
	}
	return b.String()
}

// comprobarMergeComandos verifica el merge global + per-proyecto de una clave
// de comandos (lint/test/build): el resultado debe contener exactamente los
// comandos esperados, sin duplicados ni añadidos. Extraído a helper porque los
// tres tests de precedencia comparten la misma estructura (ADVISORY del
// dogfooding: el patrón no debe copiarse una cuarta vez en S4).
func comprobarMergeComandos(t *testing.T, clave string, obtener func(Config) []string, globales, locales, esperados []string) {
	t.Helper()
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
		fmt.Sprintf("%s:\n%s", clave, listaYAML(globales)))
	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		fmt.Sprintf("%s:\n%s", clave, listaYAML(locales)))

	comandos := obtener(CargarConfiguracionLocal(worktree))
	if len(comandos) != len(esperados) {
		t.Fatalf("%s = %+v, esperado exactamente %d (global + per-proyecto)", clave, comandos, len(esperados))
	}
	for _, esperado := range esperados {
		if !slices.Contains(comandos, esperado) {
			t.Errorf("%s = %+v, falta %q", clave, comandos, esperado)
		}
	}
}

// TestComandosVerificacionPrecedencia verifica el merge global + per-proyecto:
// los comandos per-proyecto se acumulan a los globales (misma semántica que
// lint_commands).
func TestComandosVerificacionPrecedencia(t *testing.T) {
	comprobarMergeComandos(t, "test_commands",
		func(c Config) []string { return c.TestCommands },
		[]string{"go test ./..."},
		[]string{"go test -race ./internal/review"},
		[]string{"go test ./...", "go test -race ./internal/review"})
}

// TestComandosVerificacionPrecedenciaBuild verifica el merge de
// build_commands (ADVISORY 4): los comandos per-proyecto se acumulan a los
// globales, igual que test_commands y lint_commands.
func TestComandosVerificacionPrecedenciaBuild(t *testing.T) {
	comprobarMergeComandos(t, "build_commands",
		func(c Config) []string { return c.BuildCommands },
		[]string{"go build ./..."},
		[]string{"go build -race ./cmd/sentinel"},
		[]string{"go build ./...", "go build -race ./cmd/sentinel"})
}
