package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidacionCapabilitiesFormaInvalida verifica las tres validaciones de
// forma de validation.capabilities/profiles que deben fallar en el momento de
// la carga con un error explícito (nunca con un valor por defecto silencioso):
// perfil que referencia una capability inexistente, supports_scope sin
// scoped_command y scoped_command sin el marcador {packages}.
func TestValidacionCapabilitiesFormaInvalida(t *testing.T) {
	casos := []struct {
		nombre   string
		yml      string
		contiene string
	}{
		{
			nombre: "perfil referencia capability inexistente",
			yml: `
validation:
  capabilities:
    format:
      command: "gofmt -l ."
  profiles:
    fast: [format, lint]
`,
			contiene: "lint",
		},
		{
			nombre: "supports_scope sin scoped_command",
			yml: `
validation:
  capabilities:
    lint:
      command: "go vet ./..."
      supports_scope: true
`,
			contiene: "scoped_command",
		},
		{
			nombre: "scoped_command sin marcador {packages}",
			yml: `
validation:
  capabilities:
    lint:
      command: "go vet ./..."
      supports_scope: true
      scoped_command: "go vet ./internal/..."
`,
			contiene: "{packages}",
		},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			worktree := t.TempDir()
			ruta := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
			escribirConfig(t, ruta, c.yml)

			cfg := configuracionPorDefecto()
			err := aplicarDesdeRuta(&cfg, ruta)
			if err == nil {
				t.Fatalf("se esperaba un error, obtuve nil")
			}
			if !strings.Contains(err.Error(), c.contiene) {
				t.Errorf("error = %v, esperado que contenga %q", err, c.contiene)
			}
		})
	}
}

// TestCapabilitiesImplicitasDesdeComandosLegado verifica que una config sin
// sección validation, con solo lint_commands/test_commands/build_commands
// (esquema anterior a T1.2), sigue produciendo capabilities utilizables: la
// traducción implícita es la que permite que un consumidor futuro de "las
// capabilities configuradas" no dependa de que el usuario reescriba su yml.
func TestCapabilitiesImplicitasDesdeComandosLegado(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
lint_commands:
  - "gofmt -l ."
  - "go vet ./..."
test_commands:
  - "go test ./..."
build_commands:
  - "go build ./..."
`)

	cfg := CargarConfiguracionLocal(worktree)

	lint, existe := cfg.Validation.Capabilities["lint"]
	if !existe {
		t.Fatalf("Validation.Capabilities = %+v, esperado capability implícita 'lint'", cfg.Validation.Capabilities)
	}
	if lint.FailsWhen != FailsWhenExitCode {
		t.Errorf("lint.FailsWhen = %q, esperado %q", lint.FailsWhen, FailsWhenExitCode)
	}
	if !strings.Contains(lint.Command, "gofmt -l .") || !strings.Contains(lint.Command, "go vet ./...") {
		t.Errorf("lint.Command = %q, esperado que incluya ambos comandos de lint_commands", lint.Command)
	}

	unitTest, existe := cfg.Validation.Capabilities["unit_test"]
	if !existe || unitTest.Command != "go test ./..." {
		t.Errorf("Validation.Capabilities[unit_test] = %+v, esperado go test ./...", unitTest)
	}

	build, existe := cfg.Validation.Capabilities["build"]
	if !existe || build.Command != "go build ./..." {
		t.Errorf("Validation.Capabilities[build] = %+v, esperado go build ./...", build)
	}
}

// TestValidacionCompletaSeParsea verifica que una sección validation completa
// (capabilities con sus cuatro campos, profiles y mode) se parsea y queda
// accesible en la estructura de negocio. Respecto al ejemplo del enunciado se
// añaden las capabilities build/static_analysis/security (con un comando
// mínimo) porque el perfil "full" las referencia y la validación de forma
// exige que toda capability nombrada en un perfil esté declarada.
func TestValidacionCompletaSeParsea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
validation:
  capabilities:
    format:
      command: "gofmt -l ."
      fails_when: output_not_empty
    lint:
      command: "go vet ./..."
      supports_scope: true
      scoped_command: "go vet {packages}"
    unit_test:
      command: "go test ./..."
      supports_scope: true
      scoped_command: "go test {packages}"
      timeout: 300
    build:
      command: "go build ./..."
    static_analysis:
      command: "staticcheck ./..."
    security:
      command: "gosec ./..."
  profiles:
    fast: [format, lint]
    standard: [format, lint, build, unit_test]
    full: [format, lint, build, unit_test, static_analysis, security]
  mode: worktree
`)

	cfg := CargarConfiguracionLocal(worktree)

	if cfg.Validation.Mode != ModeWorktree {
		t.Errorf("Validation.Mode = %q, esperado %q", cfg.Validation.Mode, ModeWorktree)
	}
	formato := cfg.Validation.Capabilities["format"]
	if formato.Command != "gofmt -l ." || formato.FailsWhen != FailsWhenOutputNotEmpty {
		t.Errorf("capability format = %+v, esperado gofmt -l . / output_not_empty", formato)
	}
	lint := cfg.Validation.Capabilities["lint"]
	if !lint.SupportsScope || lint.ScopedCommand != "go vet {packages}" {
		t.Errorf("capability lint = %+v, esperado supports_scope true y scoped_command con {packages}", lint)
	}
	unitTest := cfg.Validation.Capabilities["unit_test"]
	if unitTest.Timeout != 300 {
		t.Errorf("capability unit_test.Timeout = %d, esperado 300", unitTest.Timeout)
	}
	if len(cfg.Validation.Profiles["standard"]) != 4 {
		t.Errorf("profiles.standard = %+v, esperado 4 capabilities", cfg.Validation.Profiles["standard"])
	}
	if len(cfg.Validation.Profiles["full"]) != 6 {
		t.Errorf("profiles.full = %+v, esperado 6 capabilities", cfg.Validation.Profiles["full"])
	}
}
