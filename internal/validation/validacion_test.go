package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// agenteFake implementa agentadapter.AdaptadorPrompt para los tests de la
// delegación sin capabilities configuradas.
type agenteFake struct {
	salida string
	err    error
}

func (a *agenteFake) EjecutarPrompt(prompt string) (string, error) {
	return a.salida, a.err
}

// cfgConCapability construye una config mínima con una única capability en
// un único perfil "standard".
func cfgConCapability(nombre string, cap config.CapabilityConfig) config.Config {
	return config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{nombre: cap},
			Profiles:     map[string][]string{"standard": {nombre}},
		},
	}
}

// TestEjecutarPerfil_CapabilityFallaExitCode: un comando falso con exit != 0
// produce un ValidationRun fallido cuyo Hallazgo trae la salida real como
// evidencia.
func TestEjecutarPerfil_CapabilityFallaExitCode(t *testing.T) {
	cfg := cfgConCapability("lint", config.CapabilityConfig{
		Command:   "go vet ./...",
		FailsWhen: config.FailsWhenExitCode,
	})
	runs, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{
		Cfg: cfg,
		Ejecutar: func(comando string) (int, string, error) {
			return 1, "vet: paquete roto", nil
		},
	})
	if err != nil {
		t.Fatalf("EjecutarPerfil falló: %v", err)
	}
	if len(runs) != 1 || runs[0].Exit != 1 {
		t.Fatalf("runs = %+v, esperado un run con exit 1", runs)
	}
	hallazgos := Hallazgos(runs, cfg.Validation.Capabilities)
	if len(hallazgos) != 1 {
		t.Fatalf("hallazgos = %+v, esperado 1", hallazgos)
	}
	h := hallazgos[0]
	if h.Source != "validation" || h.Severity != "CRITICAL" {
		t.Errorf("hallazgo = %+v, esperado Source=validation Severity=CRITICAL", h)
	}
	if !strings.Contains(h.Evidencia, "vet: paquete roto") {
		t.Errorf("Evidencia = %q, esperado que incluya la salida real", h.Evidencia)
	}
}

// TestEjecutarPerfil_OutputNotEmptyFallaConExitoCero: fails_when
// output_not_empty falla aunque el exit code sea 0 (caso gofmt -l).
func TestEjecutarPerfil_OutputNotEmptyFallaConExitoCero(t *testing.T) {
	cfg := cfgConCapability("gofmt", config.CapabilityConfig{
		Command:   "gofmt -l .",
		FailsWhen: config.FailsWhenOutputNotEmpty,
	})
	runs, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{
		Cfg: cfg,
		Ejecutar: func(comando string) (int, string, error) {
			return 0, "archivo_sin_formatear.go\n", nil
		},
	})
	if err != nil {
		t.Fatalf("EjecutarPerfil falló: %v", err)
	}
	hallazgos := Hallazgos(runs, cfg.Validation.Capabilities)
	if len(hallazgos) != 1 {
		t.Fatalf("hallazgos = %+v, esperado 1 (output_not_empty debe fallar aunque exit sea 0)", hallazgos)
	}
}

// TestEjecutarPerfil_OutputNotEmptyNoFallaSiVacia: exit 0 y salida vacía no
// falla con output_not_empty.
func TestEjecutarPerfil_OutputNotEmptyNoFallaSiVacia(t *testing.T) {
	cfg := cfgConCapability("gofmt", config.CapabilityConfig{
		Command:   "gofmt -l .",
		FailsWhen: config.FailsWhenOutputNotEmpty,
	})
	runs, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{
		Cfg: cfg,
		Ejecutar: func(comando string) (int, string, error) {
			return 0, "", nil
		},
	})
	if err != nil {
		t.Fatalf("EjecutarPerfil falló: %v", err)
	}
	hallazgos := Hallazgos(runs, cfg.Validation.Capabilities)
	if len(hallazgos) != 0 {
		t.Fatalf("hallazgos = %+v, esperado 0", hallazgos)
	}
}

// TestEjecutarPerfil_ScopedCommand: con alcance no vacío y SupportsScope,
// usa scoped_command con {packages} sustituido y Alcance == "parcial".
func TestEjecutarPerfil_ScopedCommand(t *testing.T) {
	cfg := cfgConCapability("test", config.CapabilityConfig{
		Command:       "go test ./...",
		FailsWhen:     config.FailsWhenExitCode,
		SupportsScope: true,
		ScopedCommand: "go test {packages}",
	})
	var comandoRecibido string
	runs, err := EjecutarPerfil("standard", []string{"./internal/a", "./internal/b"}, OpcionesEjecucion{
		Cfg: cfg,
		Ejecutar: func(comando string) (int, string, error) {
			comandoRecibido = comando
			return 0, "", nil
		},
	})
	if err != nil {
		t.Fatalf("EjecutarPerfil falló: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, esperado 1", runs)
	}
	if runs[0].Alcance != AlcanceParcial {
		t.Errorf("Alcance = %q, esperado %q", runs[0].Alcance, AlcanceParcial)
	}
	if runs[0].MotivoAlcance == "" {
		t.Error("MotivoAlcance vacío, se esperaba una explicación del acotamiento")
	}
	esperado := "go test ./internal/a ./internal/b"
	if comandoRecibido != esperado || runs[0].Comando != esperado {
		t.Errorf("comando = %q, esperado %q", comandoRecibido, esperado)
	}
}

// TestEjecutarPerfil_SinAlcanceUsaCommandCompleto: aunque la capability
// soporte scope, sin alcance se usa command sin acotar.
func TestEjecutarPerfil_SinAlcanceUsaCommandCompleto(t *testing.T) {
	cfg := cfgConCapability("test", config.CapabilityConfig{
		Command:       "go test ./...",
		SupportsScope: true,
		ScopedCommand: "go test {packages}",
	})
	runs, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{
		Cfg: cfg,
		Ejecutar: func(comando string) (int, string, error) {
			return 0, "", nil
		},
	})
	if err != nil {
		t.Fatalf("EjecutarPerfil falló: %v", err)
	}
	if runs[0].Alcance != AlcanceCompleto || runs[0].Comando != "go test ./..." {
		t.Errorf("runs[0] = %+v, esperado command completo sin acotar", runs[0])
	}
}

// TestEjecutarPerfil_SinCapabilitiesDelegaContratoTested: sin capabilities
// configuradas para el perfil, delega al agente y produce runs equivalentes
// al contrato tested (mismo camino que ya cubría internal/ops.Verificar).
func TestEjecutarPerfil_SinCapabilitiesDelegaContratoTested(t *testing.T) {
	agente := &agenteFake{salida: "todo verde\ntested: make test; go vet ./..."}
	runs, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{
		Cfg:    config.Config{},
		Agente: agente,
	})
	if err != nil {
		t.Fatalf("EjecutarPerfil falló: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, esperado 2 (contrato tested con 2 comandos)", runs)
	}
	if runs[0].Comando != "make test" || runs[1].Comando != "go vet ./..." {
		t.Errorf("comandos = %+v, esperado [make test, go vet ./...]", runs)
	}
	for _, r := range runs {
		if r.Capability != capabilityDelegada {
			t.Errorf("Capability = %q, esperado %q", r.Capability, capabilityDelegada)
		}
	}
}

// TestEjecutarPerfil_SinCapabilitiesSinAgenteNoBloquea: sin capabilities y
// sin agente, degrada a lista vacía sin error: la validación nunca bloquea.
func TestEjecutarPerfil_SinCapabilitiesSinAgenteNoBloquea(t *testing.T) {
	runs, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{Cfg: config.Config{}})
	if err != nil {
		t.Fatalf("EjecutarPerfil falló: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("runs = %+v, esperado vacío", runs)
	}
}

// TestEjecutarPerfil_AgenteFallaNoBloquea: agente unavailable o con error no
// bloquea la validación (mismo criterio que internal/ops.Verificar).
func TestEjecutarPerfil_AgenteFallaNoBloquea(t *testing.T) {
	casos := map[string]*agenteFake{
		"unavailable":  {salida: "unavailable"},
		"error":        {salida: "", err: errors.New("sin respuesta")},
		"sin contrato": {salida: "respuesta sin contrato"},
	}
	for nombre, agente := range casos {
		runs, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{Cfg: config.Config{}, Agente: agente})
		if err != nil {
			t.Fatalf("%s: EjecutarPerfil falló: %v", nombre, err)
		}
		if len(runs) != 0 {
			t.Errorf("%s: runs = %+v, esperado vacío", nombre, runs)
		}
	}
}

// TestEjecutarPerfil_CapabilityDesconocidaEnPerfil: si el perfil referencia
// una capability que no existe en Capabilities, es un error de configuración.
func TestEjecutarPerfil_CapabilityDesconocidaEnPerfil(t *testing.T) {
	cfg := config.Config{
		Validation: config.ValidationConfig{
			Capabilities: map[string]config.CapabilityConfig{},
			Profiles:     map[string][]string{"standard": {"inexistente"}},
		},
	}
	_, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{
		Cfg: cfg,
		Ejecutar: func(comando string) (int, string, error) {
			return 0, "", nil
		},
	})
	if err == nil {
		t.Error("EjecutarPerfil aceptó una capability no configurada sin error")
	}
}

// TestEjecutarPerfil_ErrorDeEjecucionSePropaga: si el comando no se puede
// lanzar (no exit code, sino fallo de ejecución), el error se propaga.
func TestEjecutarPerfil_ErrorDeEjecucionSePropaga(t *testing.T) {
	cfg := cfgConCapability("test", config.CapabilityConfig{Command: "go test ./..."})
	_, err := EjecutarPerfil("standard", nil, OpcionesEjecucion{
		Cfg: cfg,
		Ejecutar: func(comando string) (int, string, error) {
			return 0, "", errors.New("binario ausente")
		},
	})
	if err == nil {
		t.Error("EjecutarPerfil aceptó un error de ejecución sin propagarlo")
	}
}
