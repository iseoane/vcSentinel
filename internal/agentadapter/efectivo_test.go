package agentadapter

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// adaptadorFalso simula un hijo de la cadena que responde o falla siempre.
type adaptadorFalso struct {
	nombre   string
	modelo   string
	esfuerzo string
	falla    bool
}

func (a *adaptadorFalso) EjecutarPrompt(string) (string, error) {
	if a.falla {
		return "", errors.New("no disponible")
	}
	return "ok", nil
}

func (a *adaptadorFalso) ObtenerMensajeCommit([]string, string, int) (string, error) {
	if a.falla {
		return "", errors.New("no disponible")
	}
	return "chore: algo", nil
}

func (a *adaptadorFalso) AgenteEfectivo() (AgenteEfectivo, bool) {
	return AgenteEfectivo{Binario: a.nombre, Modelo: a.modelo, Esfuerzo: a.esfuerzo}, true
}

func (a *adaptadorFalso) String() string { return a.nombre }

// TestCadenaRegistraElAdaptadorQueRespondio cubre la aceptación de T0.2: si el
// primero falla y contesta el segundo, el autor registrado debe ser el segundo.
func TestCadenaRegistraElAdaptadorQueRespondio(t *testing.T) {
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{
		&adaptadorFalso{nombre: "claude", modelo: "opus", falla: true},
		&adaptadorFalso{nombre: "opencode", modelo: "sonnet", esfuerzo: "high"},
	}}

	if _, err := cadena.EjecutarPrompt("hola"); err != nil {
		t.Fatalf("la cadena debería haber respondido con el segundo: %v", err)
	}

	efectivo, ok := cadena.AgenteEfectivo()
	if !ok {
		t.Fatal("la cadena no reportó agente efectivo tras responder")
	}
	if efectivo.Binario != "opencode" {
		t.Errorf("binario = %q, esperado opencode (el que respondió)", efectivo.Binario)
	}
	if efectivo.Modelo != "sonnet" || efectivo.Esfuerzo != "high" {
		t.Errorf("modelo/esfuerzo = %q/%q, esperado sonnet/high", efectivo.Modelo, efectivo.Esfuerzo)
	}
}

// TestCadenaSinRespuestaNoReportaAgente: si nadie respondió, no hay autor que
// registrar. Inventar uno sería el mismo defecto que T0.2 corrige.
func TestCadenaSinRespuestaNoReportaAgente(t *testing.T) {
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{
		&adaptadorFalso{nombre: "claude", falla: true},
		&adaptadorFalso{nombre: "opencode", falla: true},
	}}

	if _, err := cadena.EjecutarPrompt("hola"); err == nil {
		t.Fatal("se esperaba error: ningún adaptador responde")
	}
	if _, ok := cadena.AgenteEfectivo(); ok {
		t.Error("reportó un agente efectivo pese a que ninguno respondió")
	}
}

// TestCadenaActualizaElAgenteEnCadaPeticion: el fallback es por petición y
// nunca se cachea, así que el autor registrado debe seguir a la última.
func TestCadenaActualizaElAgenteEnCadaPeticion(t *testing.T) {
	primero := &adaptadorFalso{nombre: "claude", modelo: "opus"}
	segundo := &adaptadorFalso{nombre: "opencode", modelo: "sonnet"}
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{primero, segundo}}

	if _, err := cadena.EjecutarPrompt("uno"); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if efectivo, _ := cadena.AgenteEfectivo(); efectivo.Binario != "claude" {
		t.Fatalf("primera petición: binario = %q, esperado claude", efectivo.Binario)
	}

	primero.falla = true
	if _, err := cadena.EjecutarPrompt("dos"); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if efectivo, _ := cadena.AgenteEfectivo(); efectivo.Binario != "opencode" {
		t.Errorf("segunda petición: binario = %q, esperado opencode", efectivo.Binario)
	}
}

// TestCLIAdapterReportaSuConfiguracion: el agente efectivo de un CLIAdapter es
// su binario con el modelo y esfuerzo con los que se le invocó.
func TestCLIAdapterReportaSuConfiguracion(t *testing.T) {
	adapter := &CLIAdapter{
		BinaryName: "claude",
		Config:     config.AgentConfig{Model: "opus", ReasoningEffort: "xhigh"},
	}
	efectivo, ok := adapter.AgenteEfectivo()
	if !ok {
		t.Fatal("el CLIAdapter no reportó agente efectivo")
	}
	if efectivo.Binario != "claude" || efectivo.Modelo != "opus" || efectivo.Esfuerzo != "xhigh" {
		t.Errorf("agente efectivo = %+v", efectivo)
	}
}
