package main

// Dedicated -h/--help coverage for the two durable-runs surfaces that postdate
// ticket 15 (D2/D3): 'runs attach' must answer with its own text instead of
// dying at flag parsing, and 'runs daemon' must answer with its own text
// instead of degrading to the generic runs usage.

import (
	"bytes"
	"strings"
	"testing"
)

// TestGestionarAyudaSirveAttachDedicado: 'runs attach --help' y '-h' resuelven
// el texto dedicado de attach en stdout, stderr vacío, antes de llegar al
// parser de flags que antes lo rechazaba como flag desconocido.
func TestGestionarAyudaSirveAttachDedicado(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		salida, errores, atendida := gestionarAyudaCapturada(t, "runs", []string{"attach", flag})
		if !atendida {
			t.Fatalf("runs attach %s: la ayuda no interceptó; el handler seguiría rechazando el flag", flag)
		}
		if !strings.Contains(salida, usoRunsAttach) || !strings.Contains(salida, "--follow") {
			t.Errorf("runs attach %s: la salida no es la ayuda dedicada de attach:\n%s", flag, salida)
		}
		if errores != "" {
			t.Errorf("runs attach %s: stderr debe quedar vacío, recibió %q", flag, errores)
		}
	}
}

// TestGestionarAyudaSirveDaemonDedicado: tanto 'runs daemon --help' como la
// invocación de tercer nivel 'runs daemon start --help' sirven el texto
// dedicado de daemon (start|status|stop), nunca el encabezado genérico de
// 'sentinel runs'.
func TestGestionarAyudaSirveDaemonDedicado(t *testing.T) {
	casos := [][]string{
		{"daemon", "--help"},
		{"daemon", "-h"},
		{"daemon", "start", "--help"},
		{"daemon", "status", "-h"},
		{"daemon", "stop", "--help"},
	}
	for _, args := range casos {
		salida, errores, atendida := gestionarAyudaCapturada(t, "runs", args)
		if !atendida {
			t.Fatalf("runs %v: la ayuda debía interceptar con el texto de daemon", args)
		}
		if !strings.Contains(salida, usoRunsDaemon) || !strings.Contains(salida, "orphaned-runs") {
			t.Errorf("runs %v: la salida no es la ayuda dedicada de daemon:\n%s", args, salida)
		}
		if strings.Contains(salida, "sentinel runs <subcommand>") || salida == runsUsage {
			t.Errorf("runs %v: sirvió la ayuda genérica de runs en lugar de la de daemon:\n%s", args, salida)
		}
		if errores != "" {
			t.Errorf("runs %v: stderr debe quedar vacío, recibió %q", args, errores)
		}
	}
}

// TestAyudaAttachGanaSobreFlagInvalido: la petición de ayuda gana sobre
// cualquier otra línea de flags, incluidas las que el parser rechazaría.
// Se comprueba en las dos capas: la intercepción central y el guardia propio
// del handler de attach.
func TestAyudaAttachGanaSobreFlagInvalido(t *testing.T) {
	args := []string{"attach", "--older-than", "720h", "--help"}
	salida, errores, atendida := gestionarAyudaCapturada(t, "runs", args)
	if !atendida {
		t.Fatalf("runs %v: la ayuda central debía interceptar", args)
	}
	if !strings.Contains(salida, usoRunsAttach) {
		t.Errorf("runs %v: la intercepción central no sirvió la ayuda de attach:\n%s", args, salida)
	}
	if errores != "" {
		t.Errorf("runs %v: stderr debe quedar vacío, recibió %q", args, errores)
	}

	var salidaHandler bytes.Buffer
	codigo := executeRunsAttach(&salidaHandler, t.TempDir(), []string{"--older-than", "720h", "--help"})
	if codigo != runExitSuccess {
		t.Errorf("executeRunsAttach con --help mezclado: exit %d, want 0", codigo)
	}
	if salidaHandler.String() != textoAyudaRunsAttach {
		t.Errorf("executeRunsAttach con --help mezclado: no sirvió el texto dedicado:\n%s", salidaHandler.String())
	}

	var salidaH bytes.Buffer
	if codigo := executeRunsAttach(&salidaH, t.TempDir(), []string{"-h"}); codigo != runExitSuccess {
		t.Errorf("executeRunsAttach -h: exit %d, want 0", codigo)
	}
}

// TestAttachSinFlagsSigueRutaNormal: sin petición de ayuda, attach entra en
// su ejecución normal. En un directorio temporal sin repositorio el primer
// paso (resolver el git common dir) falla con el error de infraestructura:
// eso prueba que el guardia de ayuda no se disparó y el flujo real arrancó.
func TestAttachSinFlagsSigueRutaNormal(t *testing.T) {
	for _, args := range [][]string{nil, {"--run", "r1"}} {
		var salida, errores bytes.Buffer
		if _, _, atendida := gestionarAyudaCapturada(t, "runs", append([]string{"attach"}, args...)); atendida {
			t.Errorf("runs attach %v: interceptó sin petición de ayuda", args)
		}
		codigo := executeRunsAttach(&salida, t.TempDir(), args)
		if codigo != runExitInfrastructure {
			t.Errorf("runs attach %v: exit %d, want %d (fallo determinista fuera de un repositorio)", args, codigo, runExitInfrastructure)
		}
		if !strings.HasPrefix(salida.String(), "❌") {
			t.Errorf("runs attach %v: la ruta normal debía reportar el fallo de infraestructura, imprimió:\n%s", args, salida.String())
		}
		if strings.Contains(salida.String(), "Purpose:") {
			t.Errorf("runs attach %v: la ruta normal imprimió texto de ayuda:\n%s", args, salida.String())
		}
		if errores.Len() != 0 {
			t.Errorf("runs attach %v: errores capturado sin motivo: %q", args, errores.String())
		}
	}
}

// TestDaemonSubcomandosSinFlagsRechazanResto: los subcomandos de daemon no
// aceptan flags por contrato; solo la petición de ayuda se atiende, y llega
// desde la intercepción central (resuelta a la clave 'runs daemon'), nunca
// desde handlers propios — igual que todos los hermanos del paquete, donde
// ningún handler se auto-intercepta la ayuda.
func TestDaemonSubcomandosSinFlagsRechazanResto(t *testing.T) {
	var salida bytes.Buffer
	if codigo := executeRunsDaemon(&salida, t.TempDir(), []string{"status", "--json"}); codigo != runExitUsage {
		t.Errorf("daemon status --json: exit %d, want %d", codigo, runExitUsage)
	}
	if !strings.Contains(salida.String(), "❌ Unknown flag for 'sentinel runs daemon status': --json") {
		t.Errorf("daemon status --json: el rechazo no es el error de contrato esperado:\n%s", salida.String())
	}
	salida.Reset()
	if codigo := executeRunsDaemon(&salida, t.TempDir(), nil); codigo != runExitUsage {
		t.Errorf("daemon sin subcomando: exit %d, want %d", codigo, runExitUsage)
	}
	if !strings.Contains(salida.String(), usoRunsDaemon) {
		t.Errorf("daemon sin subcomando: el rechazo no cita la línea de uso compartida:\n%s", salida.String())
	}
}
