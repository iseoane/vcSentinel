package agentadapter

import (
	"errors"
	"strings"
	"testing"
)

// adaptadorFake es un adaptador en memoria para probar CadenaAdaptador sin
// ejecutar binarios reales. Implementa adaptadorCompleto (AgentAdapter +
// EjecutarPrompt) y expone su nombre para los mensajes de error de la cadena.
type adaptadorFake struct {
	nombre  string
	salida  string
	mensaje string
	err     error
	prompts int
}

func (f *adaptadorFake) EjecutarPrompt(prompt string) (string, error) {
	f.prompts++
	if f.err != nil {
		return "", f.err
	}
	return f.salida, nil
}

func (f *adaptadorFake) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.mensaje, nil
}

func (f *adaptadorFake) String() string { return f.nombre }

// adaptadorFakeDiff añade la capacidad de diff (AdapterConDiff) a un fake base.
type adaptadorFakeDiff struct {
	adaptadorFake
	diffSalida string
	diffErr    error
}

func (f *adaptadorFakeDiff) ObtenerMensajeCommitConDiff(rutasArchivos []string, capa string, batchNum int, diff string) (string, error) {
	if f.diffErr != nil {
		return "", f.diffErr
	}
	return f.diffSalida, nil
}

// adaptadorFakeRefactor añade la capacidad de refactor (AdapterRefactor) a un
// fake base.
type adaptadorFakeRefactor struct {
	adaptadorFake
	planSalida    string
	planErr       error
	aplicarSalida string
	aplicarErr    error
}

func (f *adaptadorFakeRefactor) ProponerPlanRefactor(rutaArchivo string) (string, error) {
	if f.planErr != nil {
		return "", f.planErr
	}
	return f.planSalida, nil
}

func (f *adaptadorFakeRefactor) AplicarPlanRefactor(rutaArchivo string, plan string) (string, error) {
	if f.aplicarErr != nil {
		return "", f.aplicarErr
	}
	return f.aplicarSalida, nil
}

// TestCadenaEjecutarPromptConFallback verifica que EjecutarPrompt prueba el
// primer hijo y, si falla, devuelve la respuesta del segundo.
func TestCadenaEjecutarPromptConFallback(t *testing.T) {
	primero := &adaptadorFake{nombre: "primero", err: errors.New("no responde")}
	segundo := &adaptadorFake{nombre: "segundo", salida: "salida del segundo"}
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{primero, segundo}}

	salida, err := cadena.EjecutarPrompt("prompt de prueba")
	if err != nil {
		t.Fatalf("EjecutarPrompt devolvió error: %v", err)
	}
	if salida != "salida del segundo" {
		t.Errorf("salida = %q, esperado la del segundo adaptador", salida)
	}
}

func TestCadenaEjecutarRevisionRequiereCapacidadRestringida(t *testing.T) {
	sinRevision := &adaptadorFake{nombre: "sin-revision", salida: "no debe ejecutarse"}
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{sinRevision}}

	_, err := cadena.EjecutarRevision("revisar", []string{"a.go"})
	if err == nil {
		t.Fatal("EjecutarRevision debería fallar sin capacidad restringida")
	}
	if !strings.Contains(err.Error(), "semantic review unavailable") {
		t.Errorf("error = %q, esperado error de revisión semántica no disponible", err)
	}
	if sinRevision.prompts != 0 {
		t.Errorf("EjecutarPrompt se ejecutó %d veces, esperado 0", sinRevision.prompts)
	}
}

// TestCadenaObtenerMensajeCommitConFallback verifica que ObtenerMensajeCommit
// aplica el mismo fallback en cadena que EjecutarPrompt.
func TestCadenaObtenerMensajeCommitConFallback(t *testing.T) {
	primero := &adaptadorFake{nombre: "primero", err: errors.New("no responde")}
	segundo := &adaptadorFake{nombre: "segundo", mensaje: "feat: desde el segundo"}
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{primero, segundo}}

	mensaje, err := cadena.ObtenerMensajeCommit([]string{"a.go"}, "config", 1)
	if err != nil {
		t.Fatalf("ObtenerMensajeCommit devolvió error: %v", err)
	}
	if mensaje != "feat: desde el segundo" {
		t.Errorf("mensaje = %q, esperado el del segundo adaptador", mensaje)
	}
}

// TestCadenaTodosFallan verifica que el error agregado nombra a cada candidato
// y sus errores.
func TestCadenaTodosFallan(t *testing.T) {
	primero := &adaptadorFake{nombre: "primero", err: errors.New("timeout")}
	segundo := &adaptadorFake{nombre: "segundo", err: errors.New("sin conexión")}
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{primero, segundo}}

	_, err := cadena.EjecutarPrompt("prompt")
	if err == nil {
		t.Fatal("se esperaba un error agregado, no se devolvió ninguno")
	}
	if !strings.Contains(err.Error(), "primero") || !strings.Contains(err.Error(), "segundo") {
		t.Errorf("el error no nombra a los candidatos: %v", err)
	}
	if !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), "sin conexión") {
		t.Errorf("el error no incluye el de cada candidato: %v", err)
	}
}

// TestCadenaObtenerMensajeCommitConDiff verifica que la cadena usa el método
// con diff cuando el hijo lo implementa y cae al método base cuando no.
func TestCadenaObtenerMensajeCommitConDiff(t *testing.T) {
	t.Run("usa el hijo con capacidad de diff", func(t *testing.T) {
		conDiff := &adaptadorFakeDiff{
			adaptadorFake: adaptadorFake{nombre: "con-diff", mensaje: "base", salida: "base"},
			diffSalida:    "feat: con diff",
		}
		cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{conDiff}}

		mensaje, err := cadena.ObtenerMensajeCommitConDiff([]string{"a.go"}, "config", 1, "diff-xyz")
		if err != nil {
			t.Fatalf("ObtenerMensajeCommitConDiff devolvió error: %v", err)
		}
		if mensaje != "feat: con diff" {
			t.Errorf("mensaje = %q, esperado del método con diff", mensaje)
		}
	})

	t.Run("sin hijo con diff usa el método base", func(t *testing.T) {
		base := &adaptadorFake{nombre: "base", mensaje: "feat: base", salida: "x"}
		cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{base}}

		mensaje, err := cadena.ObtenerMensajeCommitConDiff([]string{"a.go"}, "config", 1, "diff-xyz")
		if err != nil {
			t.Fatalf("ObtenerMensajeCommitConDiff devolvió error: %v", err)
		}
		if mensaje != "feat: base" {
			t.Errorf("mensaje = %q, esperado del método base", mensaje)
		}
	})

	t.Run("el hijo con diff falla y el siguiente responde", func(t *testing.T) {
		conDiff := &adaptadorFakeDiff{
			adaptadorFake: adaptadorFake{nombre: "con-diff", err: errors.New("cae")},
			diffErr:       errors.New("cae con diff"),
		}
		base := &adaptadorFake{nombre: "base", mensaje: "feat: fallback", salida: "x"}
		cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{conDiff, base}}

		mensaje, err := cadena.ObtenerMensajeCommitConDiff([]string{"a.go"}, "config", 1, "diff")
		if err != nil {
			t.Fatalf("ObtenerMensajeCommitConDiff devolvió error: %v", err)
		}
		if mensaje != "feat: fallback" {
			t.Errorf("mensaje = %q, esperado fallback al siguiente adaptador", mensaje)
		}
	})
}

// TestCadenaRefactorSoloHijosConCapacidad verifica que el plan de refactor se
// delega solo a los hijos que implementan AdapterRefactor, saltando el resto.
func TestCadenaRefactorSoloHijosConCapacidad(t *testing.T) {
	base := &adaptadorFake{nombre: "base", err: errors.New("no implementa refactor")}
	conRefactor := &adaptadorFakeRefactor{
		adaptadorFake: adaptadorFake{nombre: "refactor", err: errors.New("no implementa refactor")},
		planSalida:    "plan de división",
	}
	cadena := &CadenaAdaptador{adaptadores: []adaptadorCompleto{base, conRefactor}}

	plan, err := cadena.ProponerPlanRefactor("masivo.go")
	if err != nil {
		t.Fatalf("ProponerPlanRefactor devolvió error: %v", err)
	}
	if plan != "plan de división" {
		t.Errorf("plan = %q, esperado el del hijo con AdapterRefactor", plan)
	}
}

// TestCadenaListaVacia verifica que una cadena sin adaptadores devuelve error
// en cualquier llamada.
func TestCadenaListaVacia(t *testing.T) {
	cadena := &CadenaAdaptador{}
	if _, err := cadena.EjecutarPrompt("prompt"); err == nil {
		t.Error("lista vacía debería devolver error en EjecutarPrompt")
	}
	if _, err := cadena.ObtenerMensajeCommit([]string{}, "config", 1); err == nil {
		t.Error("lista vacía debería devolver error en ObtenerMensajeCommit")
	}
	if _, err := cadena.ProponerPlanRefactor("masivo.go"); err == nil {
		t.Error("lista vacía debería devolver error en ProponerPlanRefactor")
	}
}
