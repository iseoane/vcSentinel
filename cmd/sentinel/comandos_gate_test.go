package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
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

// TestAplicarTimeoutSegundos cubre el otro extremo del flag: que el valor
// parseado llegue de verdad a review.Timeout, en segundos y en ese campo.
// Comprobar solo el parser dejaría pasar un override borrado, en la unidad
// equivocada o escrito en otro campo.
func TestAplicarTimeoutSegundos(t *testing.T) {
	base := config.Config{Review: config.ReviewConfig{Timeout: 600 * time.Second}}

	t.Run("sustituye review.timeout en segundos", func(t *testing.T) {
		got := aplicarTimeoutSegundos(base, 1200)
		if got.Review.Timeout != 1200*time.Second {
			t.Errorf("Review.Timeout = %v, esperaba 1200s", got.Review.Timeout)
		}
	})

	t.Run("sin override el yml manda", func(t *testing.T) {
		if got := aplicarTimeoutSegundos(base, 0); got.Review.Timeout != base.Review.Timeout {
			t.Errorf("Review.Timeout = %v, esperaba el valor del yml %v", got.Review.Timeout, base.Review.Timeout)
		}
	})
}

// TestParsearFlagsGate cubre --stage obligatorio con valores fijos y
// --profile opcional con su default.
func TestParsearFlagsGate(t *testing.T) {
	t.Run("stage valido sin profile usa el default", func(t *testing.T) {
		stage, perfil, _, err := parsearFlagsGate([]string{"--stage", "pre-commit"})
		if err != nil {
			t.Fatalf("no se esperaba error: %v", err)
		}
		if stage != "pre-commit" || perfil != perfilGatePorDefecto {
			t.Errorf("obtuve stage=%q perfil=%q", stage, perfil)
		}
	})

	t.Run("stage y profile explicitos", func(t *testing.T) {
		stage, perfil, _, err := parsearFlagsGate([]string{"--stage", "pr", "--profile", "custom"})
		if err != nil {
			t.Fatalf("no se esperaba error: %v", err)
		}
		if stage != "pr" || perfil != "custom" {
			t.Errorf("obtuve stage=%q perfil=%q", stage, perfil)
		}
	})

	t.Run("stage ausente es error", func(t *testing.T) {
		if _, _, _, err := parsearFlagsGate([]string{}); err == nil {
			t.Fatal("se esperaba error por --stage ausente")
		}
	})

	t.Run("timeout valido sustituye review.timeout solo en esta invocacion", func(t *testing.T) {
		_, _, timeout, err := parsearFlagsGate([]string{"--stage", "pre-push", "--timeout", "1200"})
		if err != nil {
			t.Fatalf("no se esperaba error: %v", err)
		}
		if timeout != 1200 {
			t.Errorf("timeout = %d, esperaba 1200", timeout)
		}
	})

	t.Run("sin timeout no hay override", func(t *testing.T) {
		_, _, timeout, err := parsearFlagsGate([]string{"--stage", "pre-push"})
		if err != nil {
			t.Fatalf("no se esperaba error: %v", err)
		}
		if timeout != 0 {
			t.Errorf("timeout = %d, esperaba 0: sin flag el yml manda", timeout)
		}
	})

	t.Run("timeout irrepresentable es error", func(t *testing.T) {
		// Un valor positivo y parseable pero mayor que el máximo desbordaría
		// al pasarlo a time.Duration y quedaría negativo.
		_, _, _, err := parsearFlagsGate([]string{"--stage", "pr", "--timeout", strconv.Itoa(maxSegundosTimeout + 1)})
		if err == nil {
			t.Fatal("se esperaba error por timeout irrepresentable")
		}
		if _, _, timeout, err := parsearFlagsGate([]string{"--stage", "pr", "--timeout", strconv.Itoa(maxSegundosTimeout)}); err != nil || timeout != maxSegundosTimeout {
			t.Errorf("el máximo exacto debe aceptarse, obtuve timeout=%d err=%v", timeout, err)
		}
	})

	t.Run("timeout no numerico o no positivo es error", func(t *testing.T) {
		for _, valor := range []string{"abc", "0", "-5"} {
			if _, _, _, err := parsearFlagsGate([]string{"--stage", "pr", "--timeout", valor}); err == nil {
				t.Errorf("--timeout %q deberia ser error", valor)
			}
		}
		if _, _, _, err := parsearFlagsGate([]string{"--stage", "pr", "--timeout"}); err == nil {
			t.Error("--timeout sin valor deberia ser error")
		}
	})

	t.Run("stage no reconocido es error claro", func(t *testing.T) {
		_, _, _, err := parsearFlagsGate([]string{"--stage", "no-existe"})
		if err == nil {
			t.Fatal("se esperaba error por --stage no reconocido")
		}
		if !strings.Contains(err.Error(), "no-existe") {
			t.Errorf("el error debe citar el valor recibido, obtuve: %v", err)
		}
	})

	t.Run("flag no reconocido es error", func(t *testing.T) {
		if _, _, _, err := parsearFlagsGate([]string{"--stage", "pr", "--otro"}); err == nil {
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
