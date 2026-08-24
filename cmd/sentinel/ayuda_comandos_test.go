package main

// Tests for the central -h/--help interception (ticket 15): every registered
// command and subcommand answers -h/--help with its dedicated text on stdout,
// nothing on stderr, and the caller stops with exit 0 before any side effect.

import (
	"bytes"
	"strings"
	"testing"
)

// ayudaClavesEsperadas enumerates every help key the dispatcher must serve. It
// deliberately does NOT range over ayudaComandos: deleting a text or forgetting
// a new subcommand must fail this test instead of silently degrading to the
// legacy error paths.
var ayudaClavesEsperadas = []string{
	"version", "help", "init", "uninit", "check",
	"slice", "slice plan", "slice apply",
	"review", "gate", "lint", "rebase", "status", "explain",
	"consentimiento-diff",
	"pr", "pr create", "pr review",
	"install", "upgrade", "uninstall",
	"runs", "runs start", "runs status", "runs logs", "runs respond",
	"runs abort", "runs retry", "runs recover", "runs verify", "runs prune",
}

// invocacionDeClave translates a help key into the dispatcher arguments that
// must produce it: "runs logs --help" comes from (runs, [logs --help]).
func invocacionDeClave(clave string) (string, []string) {
	partes := strings.Fields(clave)
	return partes[0], append(partes[1:], "--help")
}

// gestionarAyudaCapturada runs the interceptor capturing both streams. A true
// result maps one-to-one onto main() returning naturally, i.e. exit code 0.
func gestionarAyudaCapturada(t *testing.T, subcomando string, args []string) (string, string, bool) {
	t.Helper()
	var salida, errores bytes.Buffer
	atendida := gestionarAyuda(&salida, &errores, subcomando, args)
	return salida.String(), errores.String(), atendida
}

// primeraLineaEsUso acepta los textos (runsUsage) que abren directamente con
// la línea de uso en lugar de una sección "Usage:".
func primeraLineaEsUso(salida string) bool {
	primera := strings.SplitN(salida, "\n", 2)[0]
	return strings.HasPrefix(primera, "sentinel ")
}

func TestGestionarAyudaSirveCadaComandoRegistrado(t *testing.T) {
	for _, clave := range ayudaClavesEsperadas {
		for _, flag := range []string{"--help", "-h"} {
			subcomando, resto := invocacionDeClave(clave)
			resto[len(resto)-1] = flag
			salida, errores, atendida := gestionarAyudaCapturada(t, subcomando, resto)

			if !atendida {
				t.Errorf("%s %s: la ayuda no interceptó; el comando seguiría ejecutándose", subcomando, strings.Join(resto, " "))
			}
			if strings.TrimSpace(salida) == "" {
				t.Errorf("%s %s: stdout vacío", clave, flag)
			}
			if !strings.Contains(salida, "Usage") && !primeraLineaEsUso(salida) {
				t.Errorf("%s %s: la ayuda no contiene línea de uso:\n%s", clave, flag, salida)
			}
			if errores != "" {
				t.Errorf("%s %s: stderr debe quedar vacío, recibió %q", clave, flag, errores)
			}
		}
	}
}

// TestGestionarAyudaInterceptaEnCualquierPosicion: en comandos con flags la
// petición de ayuda gane donde aparezca, incluso mezclada con flags válidos.
func TestGestionarAyudaInterceptaEnCualquierPosicion(t *testing.T) {
	casos := []struct {
		subcomando string
		args       []string
	}{
		{"check", []string{"--staged", "--help"}},
		{"check", []string{"--json", "--staged", "--help"}},
		{"slice", []string{"plan", "--json", "--help"}},
		{"slice", []string{"apply", "--plan", "p.json", "--answers", "a.json", "--help"}},
		{"pr", []string{"review", "--base", "main", "--help"}},
		{"pr", []string{"create", "--force", "--reason", "x", "--help"}},
		{"runs", []string{"logs", "--run", "r1", "--limit", "5", "--help"}},
		{"runs", []string{"recover", "--repair", "r1", "--help"}},
		{"review", []string{"HEAD", "--dims", "logic", "--help"}},
		{"gate", []string{"--stage", "pre-push", "--profile", "standard", "--help"}},
	}
	for _, caso := range casos {
		salida, errores, atendida := gestionarAyudaCapturada(t, caso.subcomando, caso.args)
		if !atendida {
			t.Errorf("%s %v: la ayuda debía interceptar", caso.subcomando, caso.args)
		}
		if !strings.Contains(salida, "Usage") {
			t.Errorf("%s %v: falta la línea de uso:\n%s", caso.subcomando, caso.args, salida)
		}
		if errores != "" {
			t.Errorf("%s %v: stderr debe quedar vacío, recibió %q", caso.subcomando, caso.args, errores)
		}
	}
}

// TestGestionarAyudaNoInterceptaSinPeticion: sin -h/--help el dispatch sigue
// intacto, incluidas las rutas legacy y los comandos sin argumentos.
func TestGestionarAyudaNoInterceptaSinPeticion(t *testing.T) {
	casos := []struct {
		subcomando string
		args       []string
	}{
		{"check", []string{"--staged"}},
		{"slice", []string{"plan", "--json"}},
		{"pr", nil},
		{"pr", []string{"review", "--base", "main"}},
		{"runs", nil},
		{"runs", []string{"verify", "--run", "r1"}},
		{"init", nil},
		{"explain", []string{"HEAD~1..HEAD"}},
	}
	for _, caso := range casos {
		if _, _, atendida := gestionarAyudaCapturada(t, caso.subcomando, caso.args); atendida {
			t.Errorf("%s %v: interceptó sin petición de ayuda", caso.subcomando, caso.args)
		}
	}
}

// TestPrHelpNoEjecutaLimpieza: 'sentinel pr --help' mataba al passthrough
// legacy de gh, que purga fichas huérfanas ANTES de publicar. La intercepción
// debe cortar antes: texto de pr en stdout, cero rastro del mensaje de
// limpieza ni del passthrough.
func TestPrHelpNoEjecutaLimpieza(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--title", "x", "--help"}} {
		salida, errores, atendida := gestionarAyudaCapturada(t, "pr", args)
		if !atendida {
			t.Fatalf("pr %v: la ayuda debía interceptar antes del passthrough legacy", args)
		}
		if !strings.Contains(salida, "Usage") || !strings.Contains(salida, "sentinel pr create") {
			t.Errorf("pr %v: la salida no es la ayuda dedicada de pr:\n%s", args, salida)
		}
		if strings.Contains(salida, "Limpieza previa") || strings.Contains(salida, "gh pr create terminó") {
			t.Errorf("pr %v: el passthrough legacy dejó rastros de efectos laterales:\n%s", args, salida)
		}
		if errores != "" {
			t.Errorf("pr %v: stderr debe quedar vacío, recibió %q", args, errores)
		}
	}
}

// TestHelpComandoIgualAFlagHelp: 'sentinel help <topic>' resuelve por el mismo
// mapa que '<topic> --help': byte a byte idénticos para cada comando y
// subcomando registrado.
func TestHelpComandoIgualAFlagHelp(t *testing.T) {
	for _, clave := range ayudaClavesEsperadas {
		var porHelp bytes.Buffer
		if !escribirAyudaComando(&porHelp, clave) {
			t.Fatalf("%s: 'sentinel help %s' no resolvió ningún texto", clave, clave)
		}
		subcomando, resto := invocacionDeClave(clave)
		porFlag, _, atendida := gestionarAyudaCapturada(t, subcomando, resto)
		if !atendida {
			t.Fatalf("%s: '%s --help' no interceptó", clave, clave)
		}
		if porHelp.String() != porFlag {
			t.Errorf("%s: 'help %s' y '%s --help' difieren.\n--- help ---\n%s\n--- flag ---\n%s",
				clave, clave, clave, porHelp.String(), porFlag)
		}
	}
}

// TestTextosAyudaDocumentanFlagsDeclarados: las ayudas extraídas deben citar
// la misma línea de uso que los errores de runtime ya emiten, para que la
// extracción a constantes impida la deriva entre ambas vías.
func TestTextosAyudaDocumentanFlagsDeclarados(t *testing.T) {
	casos := map[string]string{
		"slice apply":  usoSliceApply,
		"explain":      usoExplainArgs,
		"runs start":   usoRunsStart,
		"runs status":  usoRunsStatus,
		"runs logs":    usoRunsLogs,
		"runs respond": usoRunsRespond,
		"runs abort":   usoRunsAbort,
		"runs retry":   usoRunsRetry,
		"runs verify":  usoRunsVerify,
		"runs prune":   usoRunsPrune,
	}
	for clave, lineaUso := range casos {
		texto, ok := ayudaComandos[clave]
		if !ok {
			t.Fatalf("%s: falta el texto de ayuda", clave)
		}
		if !strings.Contains(texto, lineaUso) {
			t.Errorf("%s: la ayuda no cita la línea de uso compartida %q:\n%s", clave, lineaUso, texto)
		}
	}
	if ayudaComandos["runs"] != runsUsage {
		t.Error("'runs' debe integrar runsUsage, no duplicarlo")
	}
}
