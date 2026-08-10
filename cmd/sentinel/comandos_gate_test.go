package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// escribirYmlGateTest escribe un vassentinel.yml per-proyecto para los tests
// de este archivo (mínimo propio: no depende de helpers de internal/config).
func escribirYmlGateTest(t *testing.T, ruta, contenido string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatalf("no se pudo crear %s: %v", filepath.Dir(ruta), err)
	}
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", ruta, err)
	}
}

// TestParsearFlagsGate cubre --stage obligatorio con valores fijos y
// --profile opcional con su default.
func TestParsearFlagsGate(t *testing.T) {
	t.Run("stage valido sin profile usa el default", func(t *testing.T) {
		stage, perfil, err := parsearFlagsGate([]string{"--stage", "pre-commit"})
		if err != nil {
			t.Fatalf("no se esperaba error: %v", err)
		}
		if stage != "pre-commit" || perfil != perfilGatePorDefecto {
			t.Errorf("obtuve stage=%q perfil=%q", stage, perfil)
		}
	})

	t.Run("stage y profile explicitos", func(t *testing.T) {
		stage, perfil, err := parsearFlagsGate([]string{"--stage", "pr", "--profile", "custom"})
		if err != nil {
			t.Fatalf("no se esperaba error: %v", err)
		}
		if stage != "pr" || perfil != "custom" {
			t.Errorf("obtuve stage=%q perfil=%q", stage, perfil)
		}
	})

	t.Run("stage ausente es error", func(t *testing.T) {
		if _, _, err := parsearFlagsGate([]string{}); err == nil {
			t.Fatal("se esperaba error por --stage ausente")
		}
	})

	t.Run("stage no reconocido es error claro", func(t *testing.T) {
		_, _, err := parsearFlagsGate([]string{"--stage", "no-existe"})
		if err == nil {
			t.Fatal("se esperaba error por --stage no reconocido")
		}
		if !strings.Contains(err.Error(), "no-existe") {
			t.Errorf("el error debe citar el valor recibido, obtuve: %v", err)
		}
	})

	t.Run("flag no reconocido es error", func(t *testing.T) {
		if _, _, err := parsearFlagsGate([]string{"--stage", "pr", "--otro"}); err == nil {
			t.Fatal("se esperaba error por flag no reconocido")
		}
	})
}

// TestEjecutarGate_StageInvalido_NoEjecutaNada cubre la aceptación: --stage
// con un valor no reconocido corta con error claro antes de tocar
// configuración, git o agentes.
func TestEjecutarGate_StageInvalido_NoEjecutaNada(t *testing.T) {
	var salida bytes.Buffer
	exit := ejecutarGate(&salida, t.TempDir(), []string{"--stage", "no-existe"})
	if exit != 1 {
		t.Errorf("exit esperado 1, obtuve %d", exit)
	}
	if !strings.Contains(salida.String(), "no-existe") {
		t.Errorf("la salida debe explicar el valor inválido, obtuve: %q", salida.String())
	}
}

// TestEjecutarGate_ConfigInvalidaConLinea cubre la aceptación: un yml con
// clave desconocida termina gate con exit 4 y el mensaje incluye la línea del
// error (config.CargarConfiguracionLocalEstricta, requisito añadido por el
// orquestador).
func TestEjecutarGate_ConfigInvalidaConLinea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	rutaConfig := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	escribirYmlGateTest(t, rutaConfig, "active_agent: \"claude\"\nclave_inexistente: true\n")

	var salida bytes.Buffer
	exit := ejecutarGate(&salida, worktree, []string{"--stage", "pre-commit"})

	if exit != 4 {
		t.Errorf("exit esperado 4, obtuve %d", exit)
	}
	if !strings.Contains(salida.String(), "line") {
		t.Errorf("la salida debe incluir la línea del error del yml, obtuve: %q", salida.String())
	}
}

// TestEjecutarGate_PerfilNoConfigurado_Exit4 cubre la decisión de diseño de
// T1.7: si --profile (o su default "standard") no existe en
// validation.profiles, gate corta con un error explícito en vez de asumir un
// perfil arbitrario.
func TestEjecutarGate_PerfilNoConfigurado_Exit4(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var salida bytes.Buffer
	exit := ejecutarGate(&salida, worktree, []string{"--stage", "pre-commit"})

	if exit != 4 {
		t.Errorf("exit esperado 4 (perfil %q no configurado), obtuve %d", perfilGatePorDefecto, exit)
	}
	if !strings.Contains(salida.String(), perfilGatePorDefecto) {
		t.Errorf("la salida debe citar el perfil que falta, obtuve: %q", salida.String())
	}
}
