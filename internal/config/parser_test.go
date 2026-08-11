package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setHome(t *testing.T, home string) {
	t.Helper()
	// os.UserHomeDir() usa HOME en Unix y USERPROFILE en Windows.
	claves := []string{"HOME"}
	if runtime.GOOS == "windows" {
		claves = append(claves, "USERPROFILE")
	}
	originales := make(map[string]string, len(claves))
	for _, clave := range claves {
		originales[clave] = os.Getenv(clave)
	}
	for _, clave := range claves {
		if err := os.Setenv(clave, home); err != nil {
			t.Fatalf("no se pudo fijar %s: %v", clave, err)
		}
	}
	t.Cleanup(func() {
		for clave, valor := range originales {
			os.Setenv(clave, valor)
		}
	})
}

func escribirConfig(t *testing.T, ruta string, contenido string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatalf("no se pudo crear el directorio %s: %v", filepath.Dir(ruta), err)
	}
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", ruta, err)
	}
}

// TestPrecedenciaConfig verifica: defaults -> global -> per-proyecto predomina.
func TestPrecedenciaConfig(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	t.Run("solo defaults sin archivos", func(t *testing.T) {
		cfg := CargarConfiguracionLocal(worktree)
		if cfg.ActiveAgent != "auto" {
			t.Errorf("ActiveAgent esperado 'auto', obtuve %q", cfg.ActiveAgent)
		}
		if cfg.Agents["claude"].Model == "" || cfg.Agents["opencode"].Model == "" {
			t.Errorf("defaults de agents incompletos: %+v", cfg.Agents)
		}
	})

	t.Run("global aplica cuando no hay per-proyecto", func(t *testing.T) {
		escribirConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
			"active_agent: \"claude\"\n")
		cfg := CargarConfiguracionLocal(worktree)
		if cfg.ActiveAgent != "claude" {
			t.Errorf("ActiveAgent esperado 'claude' desde global, obtuve %q", cfg.ActiveAgent)
		}
	})

	t.Run("per-proyecto predomina sobre global", func(t *testing.T) {
		escribirConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
			"active_agent: \"claude\"\n")
		escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
			"active_agent: \"opencode\"\n")
		cfg := CargarConfiguracionLocal(worktree)
		if cfg.ActiveAgent != "opencode" {
			t.Errorf("ActiveAgent esperado 'opencode' desde per-proyecto, obtuve %q", cfg.ActiveAgent)
		}
	})

	t.Run("per-proyecto sobreescribe solo los campos que define", func(t *testing.T) {
		escribirConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
			"agents:\n  claude:\n    model: \"claude-global\"\n    reasoning_effort: \"low\"\n")
		escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
			"agents:\n  claude:\n    model: \"claude-local\"\n")
		cfg := CargarConfiguracionLocal(worktree)
		if cfg.Agents["claude"].Model != "claude-local" {
			t.Errorf("Model per-proyecto esperado 'claude-local', obtuve %q", cfg.Agents["claude"].Model)
		}
		if cfg.Agents["claude"].ReasoningEffort != "low" {
			t.Errorf("ReasoningEffort debería heredarse del global 'low', obtuve %q", cfg.Agents["claude"].ReasoningEffort)
		}
	})
}

// TestConfigChangeClasses verifica change.classes: sin config de usuario
// aplican los defaults, y al declararla el usuario la reemplaza preservando
// el orden textual (la precedencia del clasificador).
func TestConfigChangeClasses(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	t.Run("sin config de usuario aplican los defaults", func(t *testing.T) {
		cfg := CargarConfiguracionLocal(worktree)
		if len(cfg.Change) == 0 {
			t.Fatal("Change debería tener los defaults, está vacío")
		}
	})

	t.Run("change.classes de usuario reemplaza los defaults preservando orden", func(t *testing.T) {
		escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
			"change:\n  classes:\n    infra: [\"**/*.tf\"]\n    docs: [\"**/*.md\"]\n")
		cfg := CargarConfiguracionLocal(worktree)
		if len(cfg.Change) != 2 {
			t.Fatalf("Change esperado 2 reglas de usuario, obtuve %d", len(cfg.Change))
		}
		if cfg.Change[0].Clase != "infra" || cfg.Change[1].Clase != "docs" {
			t.Errorf("orden de declaración no preservado: %+v", cfg.Change)
		}
	})

	// change.classes ausente y change.classes explícitamente vacío ("{}") son
	// intenciones distintas del usuario: antes ambas caían en el mismo "sin
	// cambios" porque el código solo miraba len(classes) == 0 (hallazgo de la
	// revisión de T3.1, internal/config/parser.go:598).
	t.Run("change.classes vacio explicito vacia las reglas, a diferencia de no declararla", func(t *testing.T) {
		escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), "change:\n  classes: {}\n")
		cfg := CargarConfiguracionLocal(worktree)
		if cfg.Change == nil || len(cfg.Change) != 0 {
			t.Fatalf("Change esperado vacío pero declarado (no nil), obtuve %#v", cfg.Change)
		}
	})

	// La revisión de los fixes de T3.1 señaló que la distinción nil/vacío solo
	// se probó en la capa per-proyecto: confirma que la capa GLOBAL, que pasa
	// por el mismo aplicarDesdeRuta/aplicarOrdenClases, se comporta igual.
	t.Run("la distincion nil/vacio tambien aplica en la capa global", func(t *testing.T) {
		home2 := t.TempDir()
		worktree2 := t.TempDir()
		setHome(t, home2)
		escribirConfig(t, filepath.Join(home2, ".vas_sentinel", "vassentinel.yml"), "change:\n  classes: {}\n")

		cfg := CargarConfiguracionLocal(worktree2)
		if cfg.Change == nil || len(cfg.Change) != 0 {
			t.Fatalf("Change esperado vacío pero declarado (no nil) desde la config global, obtuve %#v", cfg.Change)
		}
	})
}

// TestRutasConfig verifica las rutas exactas donde se busca cada nivel.
func TestRutasConfig(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	rutaGlobal, err := rutaConfigGlobal()
	if err != nil {
		t.Fatalf("rutaConfigGlobal falló: %v", err)
	}
	esperadaGlobal := filepath.Join(home, ".vas_sentinel", "vassentinel.yml")
	if rutaGlobal != esperadaGlobal {
		t.Errorf("ruta global esperada %q, obtuve %q", esperadaGlobal, rutaGlobal)
	}

	rutaLocal := rutaConfigPerProyecto(worktree)
	esperadaLocal := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	if rutaLocal != esperadaLocal {
		t.Errorf("ruta per-proyecto esperada %q, obtuve %q", esperadaLocal, rutaLocal)
	}
}
