package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// TestHelperProcess NO es un test real: es el proceso hijo que arrancan los
// tests de exit code de este paquete para ejercer funciones que llaman a
// os.Exit sin matar el proceso `go test` padre (patrón estándar de Go, el
// mismo que usa la librería os/exec para probarse a sí misma). Sin el guard
// de la variable de entorno, una corrida normal de `go test` la trata como un
// test vacío que pasa.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("VAS_SENTINEL_HELPER_PROCESS") != "1" {
		return
	}
	worktree := os.Getenv("VAS_SENTINEL_HELPER_WORKTREE")
	switch os.Getenv("VAS_SENTINEL_HELPER_FN") {
	case "ejecutarLint":
		ejecutarLint(worktree)
	case "ejecutarReview":
		ejecutarReview(worktree, []string{"HEAD"})
	case "ejecutarPrReview":
		ejecutarPrReview(worktree, nil)
	}
	os.Exit(0)
}

// ejecutarComoSubproceso relanza este mismo binario de test para invocar fn
// (una de las ramas de TestHelperProcess) sobre worktree, y captura su salida
// combinada y su exit code. home fija HOME/USERPROFILE del subproceso: el
// yml global del usuario real de la máquina nunca debe filtrarse al test.
// cmd.Dir se fija a worktree (nunca al cwd real del repo de vas.sentinel):
// si el fix bajo prueba regresara y la función siguiera de largo tras el
// error de config, cualquier comando git/agente que intente lanzar debe
// operar sobre el tmpdir aislado, no sobre este repositorio real.
func ejecutarComoSubproceso(t *testing.T, fn, worktree, home string) (salida string, exitCode int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Dir = worktree
	env := []string{
		"VAS_SENTINEL_HELPER_PROCESS=1",
		"VAS_SENTINEL_HELPER_FN=" + fn,
		"VAS_SENTINEL_HELPER_WORKTREE=" + worktree,
		"HOME=" + home,
		"USERPROFILE=" + home,
	}
	for _, kv := range os.Environ() {
		clave := strings.SplitN(kv, "=", 2)[0]
		if clave == "HOME" || clave == "USERPROFILE" || strings.HasPrefix(kv, "VAS_SENTINEL_HELPER_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env

	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return buf.String(), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("no se pudo ejecutar el subproceso de %s: %v", fn, err)
	}
	return buf.String(), exitErr.ExitCode()
}

// escribirYmlConClaveDesconocida escribe un vassentinel.yml per-proyecto con
// una clave fuera del esquema, para los tests de propagación de error de F1.
func escribirYmlConClaveDesconocida(t *testing.T, worktree string) {
	t.Helper()
	ruta := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatalf("no se pudo crear %s: %v", filepath.Dir(ruta), err)
	}
	contenido := "active_agent: \"claude\"\nclave_inexistente: true\n"
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", ruta, err)
	}
}

// TestEjecutarLint_ClaveDesconocidaEnYml_Exit1ConLinea cubre el Fix 1 (F1,
// hallazgo del orquestador): antes de este fix, ejecutarLint usaba
// CargarConfiguracionLocal (sin error), así que una clave desconocida en el
// yml se ignoraba en silencio y, al no quedar lint_commands configurados por
// el yml roto, el comando terminaba con "no hay comandos de lint" y exit 0 —
// exactamente lo contrario de lo que exige la ficha. Con
// CargarConfiguracionLocalEstricta debe cortar con exit 1 y el error visible.
func TestEjecutarLint_ClaveDesconocidaEnYml_Exit1ConLinea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	escribirYmlConClaveDesconocida(t, worktree)

	salida, exit := ejecutarComoSubproceso(t, "ejecutarLint", worktree, home)

	if exit != 1 {
		t.Errorf("exit esperado 1, obtuve %d (salida: %q)", exit, salida)
	}
	if !strings.Contains(salida, "line") {
		t.Errorf("la salida debe incluir la línea del error del yml, obtuve: %q", salida)
	}
}

func TestParsearFlagsAuditoriaDefault(t *testing.T) {
	flags, err := parsearFlagsAuditoria(nil)
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(nil) devolvió error: %v", err)
	}
	if len(flags.targets) != 1 || flags.targets[0] != "HEAD" {
		t.Errorf("targets = %+v, esperado [HEAD]", flags.targets)
	}
	if flags.all || flags.chain || flags.gate || flags.jsonOut || flags.prune {
		t.Error("flags booleanos deberían estar apagados por defecto")
	}
}

func TestParsearFlagsAuditoriaCompletas(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{
		"abc123", "--dims", "logic, security", "--profile", "deep",
		"--chain", "--answer", "no aplica aqui", "--gate",
	})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if len(flags.targets) != 1 || flags.targets[0] != "abc123" {
		t.Errorf("targets = %+v, esperado [abc123]", flags.targets)
	}
	if len(flags.dims) != 2 || flags.dims[0] != "logic" || flags.dims[1] != "security" {
		t.Errorf("dims = %+v, esperado [logic security]", flags.dims)
	}
	if flags.profile != "deep" || flags.answer != "no aplica aqui" {
		t.Errorf("profile/answer = %q/%q", flags.profile, flags.answer)
	}
	if !flags.chain || !flags.gate {
		t.Error("chain/gate deberían estar activos")
	}
}

func TestParsearFlagsAuditoriaMultiplesTargets(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"abc123", "def456", "HEAD~2"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if !reflect.DeepEqual(flags.targets, []string{"abc123", "def456", "HEAD~2"}) {
		t.Errorf("targets = %+v, esperado [abc123 def456 HEAD~2]", flags.targets)
	}
}

func TestParsearFlagsAuditoriaErrores(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"flag sin valor", []string{"--profile"}},
		{"opción desconocida", []string{"--nada"}},
	}
	for _, prueba := range pruebas {
		if _, err := parsearFlagsAuditoria(prueba.args); err == nil {
			t.Errorf("%s: debería devolver error", prueba.nombre)
		}
	}
}

func TestParsearFlagsAuditoriaHeadExplicito(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"HEAD"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(HEAD) devolvió error: %v", err)
	}
	if !reflect.DeepEqual(flags.targets, []string{"HEAD"}) {
		t.Errorf("targets = %+v, esperado [HEAD]", flags.targets)
	}
}

func TestStatusRechazaFlagsNoAplicables(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"--dims", []string{"--dims", "logic"}},
		{"--profile", []string{"--profile", "deep"}},
		{"--chain", []string{"--chain"}},
		{"--gate", []string{"--gate"}},
		{"--all", []string{"--all"}},
		{"target", []string{"abc123"}},
	}
	for _, prueba := range pruebas {
		flags, err := parsearFlagsAuditoria(prueba.args)
		if err != nil {
			t.Fatalf("%s: parsearFlagsAuditoria devolvió error: %v", prueba.nombre, err)
		}
		if !flagsNoAplicablesAStatus(flags) {
			t.Errorf("%s: debería detectarse como no aplicable a status", prueba.nombre)
		}
	}
}

func TestParsearFlagsAuditoriaTimeout(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"--timeout", "900"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(--timeout 900) devolvió error: %v", err)
	}
	if flags.timeout != 900*time.Second {
		t.Errorf("timeout = %v, esperado 900s", flags.timeout)
	}
}

func TestParsearFlagsAuditoriaTimeoutAusenteEsCero(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"HEAD"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if flags.timeout != 0 {
		t.Errorf("timeout = %v, esperado 0 (sin override)", flags.timeout)
	}
}

func TestParsearFlagsAuditoriaTimeoutInvalido(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"sin valor", []string{"--timeout"}},
		{"no numérico", []string{"--timeout", "mucho"}},
		{"cero", []string{"--timeout", "0"}},
		{"negativo", []string{"--timeout", "-30"}},
	}
	for _, prueba := range pruebas {
		if _, err := parsearFlagsAuditoria(prueba.args); err == nil {
			t.Errorf("--timeout %s: debería devolver error", prueba.nombre)
		}
	}
}

func TestStatusRechazaTimeout(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"--timeout", "900"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if !flagsNoAplicablesAStatus(flags) {
		t.Error("--timeout debería detectarse como no aplicable a status")
	}
}

func TestAplicarTimeoutFlag(t *testing.T) {
	base := config.Config{Review: config.ReviewConfig{Timeout: 600 * time.Second}}

	sinFlag := aplicarTimeoutFlag(base, flagsAuditoria{})
	if sinFlag.Review.Timeout != 600*time.Second {
		t.Errorf("sin --timeout: Timeout = %v, esperado el de la config (600s)", sinFlag.Review.Timeout)
	}

	conFlag := aplicarTimeoutFlag(base, flagsAuditoria{timeout: 900 * time.Second})
	if conFlag.Review.Timeout != 900*time.Second {
		t.Errorf("con --timeout: Timeout = %v, esperado 900s", conFlag.Review.Timeout)
	}
	if base.Review.Timeout != 600*time.Second {
		t.Errorf("aplicarTimeoutFlag mutó la config original: %v", base.Review.Timeout)
	}
}
