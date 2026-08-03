package config

import (
	"path/filepath"
	"testing"
	"time"
)

// TestSeccionesNuevas verifica el parseo de profiles, review (timeout,
// parallel, dims) y lint_commands.
func TestSeccionesNuevas(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
profiles:
  cheap:
    model: "flash-mini"
    reasoning_effort: "low"
  deep:
    agent: "claude"
    model: "claude-sonnet"
    reasoning_effort: "max"
review:
  timeout: 30
  parallel: 4
  dims:
    spec: cheap
    security: deep
lint_commands:
  - "gofmt -l ."
  - "go vet ./..."
`)

	cfg := CargarConfiguracionLocal(worktree)

	if cfg.Profiles["cheap"].Model != "flash-mini" || cfg.Profiles["cheap"].ReasoningEffort != "low" {
		t.Errorf("perfil cheap = %+v, esperado flash-mini/low", cfg.Profiles["cheap"])
	}
	if cfg.Profiles["deep"].Agent != "claude" || cfg.Profiles["deep"].Model != "claude-sonnet" {
		t.Errorf("perfil deep = %+v, esperado claude/claude-sonnet", cfg.Profiles["deep"])
	}
	if cfg.Review.Timeout != 30*time.Second {
		t.Errorf("Review.Timeout = %v, esperado 30s", cfg.Review.Timeout)
	}
	if cfg.Review.Parallel != 4 {
		t.Errorf("Review.Parallel = %d, esperado 4", cfg.Review.Parallel)
	}
	if cfg.Review.Dims["spec"] != "cheap" || cfg.Review.Dims["security"] != "deep" {
		t.Errorf("Review.Dims = %+v, esperado spec->cheap y security->deep", cfg.Review.Dims)
	}
	// El merge acumula comandos globales + per-proyecto: verificamos que los
	// per-proyecto estén presentes (puede haber más si existe config global).
	encontrados := 0
	for _, cmd := range cfg.LintCommands {
		if cmd == "gofmt -l ." || cmd == "go vet ./..." {
			encontrados++
		}
	}
	if encontrados != 2 {
		t.Errorf("LintCommands = %+v, faltan los comandos per-proyecto", cfg.LintCommands)
	}
}

// TestDefaultsPerfiles verifica que sin config los perfiles y dims por defecto
// existen y son coherentes con la guía.
func TestDefaultsPerfiles(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := CargarConfiguracionLocal(worktree)

	for _, perfil := range []string{"cheap", "normal", "deep"} {
		if _, existe := cfg.Profiles[perfil]; !existe {
			t.Errorf("falta el perfil %q por defecto", perfil)
		}
	}
	if cfg.Review.Dims["spec"] != "cheap" || cfg.Review.Dims["security"] != "deep" {
		t.Errorf("dims por defecto = %+v, esperado spec->cheap y security->deep", cfg.Review.Dims)
	}
	if cfg.Review.Timeout != 120*time.Second || cfg.Review.Parallel != 2 {
		t.Errorf("defaults review = %v/%d, esperado 120s/2", cfg.Review.Timeout, cfg.Review.Parallel)
	}
}

// TestConfigLegacyAgents verifica que la sintaxis antigua de agents sigue
// funcionando tras el refactor del parser.
func TestConfigLegacyAgents(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"active_agent: \"claude\"\nagents:\n  claude:\n    model: \"claude-legacy\"\n    reasoning_effort: \"high\"\n")

	cfg := CargarConfiguracionLocal(worktree)
	if cfg.ActiveAgent != "claude" {
		t.Errorf("ActiveAgent = %q, esperado claude", cfg.ActiveAgent)
	}
	if cfg.Agents["claude"].Model != "claude-legacy" {
		t.Errorf("Model = %q, esperado claude-legacy", cfg.Agents["claude"].Model)
	}
}

// TestTimeoutInvalidoSeIgnora verifica que valores no numéricos de timeout y
// parallel no rompen el parseo y dejan el default.
func TestTimeoutInvalidoSeIgnora(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"review:\n  timeout: \"mucho\"\n  parallel: 0\n")

	cfg := CargarConfiguracionLocal(worktree)
	if cfg.Review.Timeout != 120*time.Second {
		t.Errorf("Timeout = %v, esperado default 120s", cfg.Review.Timeout)
	}
	if cfg.Review.Parallel != 2 {
		t.Errorf("Parallel = %d, esperado default 2", cfg.Review.Parallel)
	}
}
