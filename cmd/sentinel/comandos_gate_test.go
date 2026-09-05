package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
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
		// Por encima del máximo el valor desbordaría time.Duration y quedaría
		// negativo. En 32 bits ni siquiera llega ahí, porque strconv.Atoi lo
		// rechaza antes por rango; ambos caminos son un error y eso es lo que
		// se afirma aquí, sin atarse a la anchura de int de la plataforma.
		_, _, _, err := parsearFlagsGate([]string{"--stage", "pr", "--timeout", strconv.FormatInt(maxSegundosTimeout+1, 10)})
		if err == nil {
			t.Fatal("se esperaba error por timeout irrepresentable")
		}

		// El mayor valor ACEPTABLE depende de la plataforma: donde int es de
		// 32 bits, la cota de duración queda fuera de alcance y el techo real
		// es el del propio int. Fijar el valor de 64 bits haría fallar la
		// suite justo en los objetivos que el arreglo de producción protege.
		maximo := maxSegundosTimeout
		if int64(math.MaxInt) < maximo {
			maximo = int64(math.MaxInt)
		}
		if _, _, timeout, err := parsearFlagsGate([]string{"--stage", "pr", "--timeout", strconv.FormatInt(maximo, 10)}); err != nil || int64(timeout) != maximo {
			t.Errorf("el máximo aceptable debe aceptarse, obtuve timeout=%d err=%v", timeout, err)
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

func TestGateEventPersistsOnlyOperationalMetadata(t *testing.T) {

	worktree := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", worktree).CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, output)
	}
	const rawMessage = "raw provider evidence must remain console-only"
	var output bytes.Buffer
	finalizeGateWithDetails(
		&output,
		worktree,
		"pre-push",
		gate.EstadoReviewInfrastructureError,
		[]string{rawMessage},
		"codegraph context skipped: dirty_worktree",
		[]gate.ReviewerFailure{{
			Bundle:    "correctness",
			Dimension: "logic",
			Reason:    "provider reported: ripgrep execution failed",
		}},
	)
	if !strings.Contains(output.String(), rawMessage) {
		t.Fatalf("console output = %q, want raw diagnostic", output.String())
	}

	events, err := ops.UltimosEventos(filepath.Join(worktree, ".git"), 1)
	if err != nil {
		t.Fatalf("read gate event: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want one", len(events))
	}
	detail, ok := events[0].Detail.(map[string]any)
	if !ok {
		t.Fatalf("detail = %#v, want object", events[0].Detail)
	}
	if _, exists := detail["messages"]; exists {
		t.Fatalf("gate event persisted console messages: %#v", detail["messages"])
	}
	if strings.Contains(fmt.Sprint(detail), rawMessage) {
		t.Fatalf("gate event leaked raw console message: %#v", detail)
	}
	if detail["context_skip_reason"] != "codegraph context skipped: dirty_worktree" {
		t.Errorf("context skip reason = %#v", detail["context_skip_reason"])
	}
	failures, ok := detail["reviewer_failures"].([]any)
	if !ok || len(failures) != 1 {
		t.Fatalf("reviewer failures = %#v, want one", detail["reviewer_failures"])
	}
	failure, ok := failures[0].(map[string]any)
	if !ok {
		t.Fatalf("reviewer failure = %#v, want object", failures[0])
	}
	if failure["bundle"] != "correctness" || failure["dimension"] != "logic" || failure["reason"] != "provider reported: ripgrep execution failed" {
		t.Fatalf("reviewer failure = %#v", failure)
	}
}

// TestEjecutarGateFallaCerradoSinAtributos exercises the fail-closed path that
// the review of ce8316d found untested, and it is the one that matters: the
// repository attributes decide route classification and therefore whether the
// security and concurrency bundles are scheduled at all, so a gate that
// continued with empty evidence would succeed on an under-classified plan.
//
// git.Attributes already returns "" with no error when the tree has no
// .gitattributes, so reaching this branch always means a real read failure.
func TestEjecutarGateFallaCerradoSinAtributos(t *testing.T) {
	worktree := t.TempDir()
	correr := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		// Built from empty, not appended to os.Environ: appending keeps
		// GIT_CONFIG_COUNT and the GIT_CONFIG_KEY_*/VALUE_* pairs, which
		// override the neutralisation the other variables state, and keeps a
		// global commit.gpgSign or hook that would break setup on some hosts.
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
		}
		if salida, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, salida)
		}
	}
	correr("init", "-q", "-b", "main")
	correr("config", "core.hooksPath", "")

	// A configured validation profile and a real HEAD are both required: the
	// gate stops before the attribute read without them, and a test that never
	// reaches the branch it names proves nothing.
	if err := os.MkdirAll(filepath.Join(worktree, ".vas_sentinel"), 0o755); err != nil {
		t.Fatal(err)
	}
	configuracion := "validation:\n  capabilities:\n    format:\n      command: \"true\"\n  profiles:\n    standard: [format]\n"
	if err := os.WriteFile(filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), []byte(configuracion), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "-A")
	correr("commit", "-qm", "first")

	previo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previo) })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	original := leerAtributosGate
	t.Cleanup(func() { leerAtributosGate = original })
	llamado := false
	leerAtributosGate = func(string) (string, error) {
		llamado = true
		return "", errors.New("simulated attribute read failure")
	}

	var salida bytes.Buffer
	exit := ejecutarGate(&salida, worktree, []string{"--stage", "pre-commit"})

	// The gate stops before this seam when configuration is missing, so a run
	// that never reached it would prove nothing about the fail-closed branch.
	if !llamado {
		t.Fatalf("the gate never reached the attribute read, so this test proves nothing about the fail-closed branch; output: %q", salida.String())
	}
	// The exact code matters: accepting any non-zero would pass if the attribute
	// failure were misreported as a validation or review failure, which would
	// send the operator looking in the wrong place.
	quiere := gate.CodigoSalida(gate.EstadoReviewInfrastructureError)
	if exit != quiere {
		t.Errorf("gate exited %d after an attribute read failure, want %d (review infrastructure error). Output: %q", exit, quiere, salida.String())
	}
	if !strings.Contains(salida.String(), "atributos") {
		t.Errorf("the output must name the attribute failure, got: %q", salida.String())
	}
}

// FU-6: the gate must surface corrupt human-answer history as review
// infrastructure failure rather than running without the disposition overlay.
func TestEjecutarGateFailsClosedOnCorruptHumanDisposition(t *testing.T) {
	worktree := worktreeWithCorruptHumanDisposition(t)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previous) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	var output bytes.Buffer
	exit := ejecutarGate(&output, worktree, []string{"--stage", "pre-commit"})
	want := gate.CodigoSalida(gate.EstadoReviewInfrastructureError)
	if exit != want {
		t.Fatalf("gate exit = %d, want %d; output: %q", exit, want, output.String())
	}
	if !strings.Contains(output.String(), "reading human dispositions") {
		t.Fatalf("gate did not report the disposition read failure: %q", output.String())
	}
}
