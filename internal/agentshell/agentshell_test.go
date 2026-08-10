package agentshell

import (
	"runtime"
	"strings"
	"testing"
)

// comandoSalidaCombinada devuelve, según el sistema operativo, un comando que
// escribe una línea en stdout y otra en stderr: sirve para comprobar que
// Ejecutar devuelve la salida combinada (stdout+stderr), no solo una de las
// dos.
func comandoSalidaCombinada() string {
	if runtime.GOOS == "windows" {
		return "echo salida-estandar && echo salida-error 1>&2"
	}
	return "echo salida-estandar; echo salida-error >&2"
}

// comandoExitCode devuelve un comando que termina con el exit code dado (0 o
// distinto de cero), válido tanto en cmd como en sh.
func comandoExitCode(codigo int) string {
	if codigo == 0 {
		if runtime.GOOS == "windows" {
			return "exit 0"
		}
		return "exit 0"
	}
	if runtime.GOOS == "windows" {
		return "exit 1"
	}
	return "exit 1"
}

// TestEjecutar_SalidaCombinada: Ejecutar debe devolver stdout+stderr en un
// único string, junto con exit 0 y sin error de ejecución.
func TestEjecutar_SalidaCombinada(t *testing.T) {
	exit, salida, err := Ejecutar("", comandoSalidaCombinada())
	if err != nil {
		t.Fatalf("Ejecutar falló: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, esperado 0", exit)
	}
	if !strings.Contains(salida, "salida-estandar") || !strings.Contains(salida, "salida-error") {
		t.Errorf("salida = %q, esperado que contenga ambas líneas (stdout+stderr)", salida)
	}
}

// TestEjecutar_ExitCodeDistintoDeCero: un comando que falla con exit 1 se
// refleja en el exit code devuelto, sin que eso sea un error de ejecución.
func TestEjecutar_ExitCodeDistintoDeCero(t *testing.T) {
	exit, _, err := Ejecutar("", comandoExitCode(1))
	if err != nil {
		t.Fatalf("Ejecutar no debe tratar un exit code no cero como error de ejecución: %v", err)
	}
	if exit != 1 {
		t.Errorf("exit = %d, esperado 1", exit)
	}
}

// TestParsearContratoTested_VariosComandos: extrae los comandos de la línea
// "tested: ..." separados por ;.
func TestParsearContratoTested_VariosComandos(t *testing.T) {
	comandos, err := ParsearContratoTested("Hecho.\ntested: go test ./...; go build ./...")
	if err != nil {
		t.Fatalf("ParsearContratoTested falló: %v", err)
	}
	if len(comandos) != 2 || comandos[0] != "go test ./..." || comandos[1] != "go build ./..." {
		t.Errorf("comandos = %#v, esperado [go test ./... go build ./...]", comandos)
	}
}

// TestParsearContratoTested_Unavailable: rechaza la salida "unavailable".
func TestParsearContratoTested_Unavailable(t *testing.T) {
	if _, err := ParsearContratoTested("no pude ejecutar nada\nunavailable"); err == nil {
		t.Error("ParsearContratoTested aceptó unavailable")
	}
}

// TestParsearContratoTested_ContratoVacio: rechaza un contrato tested sin
// comandos tras los dos puntos.
func TestParsearContratoTested_ContratoVacio(t *testing.T) {
	if _, err := ParsearContratoTested("tested: "); err == nil {
		t.Error("ParsearContratoTested aceptó un contrato tested vacío")
	}
}

// TestParsearContratoTested_SinContrato: rechaza una salida sin línea tested.
func TestParsearContratoTested_SinContrato(t *testing.T) {
	if _, err := ParsearContratoTested("respuesta sin contrato"); err == nil {
		t.Error("ParsearContratoTested aceptó una salida sin contrato")
	}
}
