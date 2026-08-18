package ops

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// Sentinelas de ficción para los tests de las ramas de error.
var (
	errBinarioAusente = errors.New("binario de verificación ausente")
	errSinRespuesta   = errors.New("sin respuesta del aviso")
)

// agenteFakeVerificar implementa AdaptadorPrompt para los tests de la
// delegación: devuelve la salida programada.
type agenteFakeVerificar struct {
	salida string
	err    error
}

func (a *agenteFakeVerificar) EjecutarPrompt(prompt string) (string, error) {
	return a.salida, a.err
}

// TestParsearContratoTested: extrae los comandos del contrato tested y
// rechaza unavailable / ausencia de contrato.
func TestParsearContratoTested(t *testing.T) {
	comandos, err := parsearContratoTested("Hecho.\ntested: go test ./...; go build ./...")
	if err != nil {
		t.Fatalf("parsearContratoTested falló: %v", err)
	}
	if len(comandos) != 2 || comandos[0] != "go test ./..." || comandos[1] != "go build ./..." {
		t.Errorf("comandos = %#v, esperado [go test ./... go build ./...]", comandos)
	}

	if _, err := parsearContratoTested("no pude ejecutar nada\nunavailable"); err == nil {
		t.Error("parsearContratoTested aceptó unavailable")
	}
	if _, err := parsearContratoTested("respuesta sin contrato"); err == nil {
		t.Error("parsearContratoTested aceptó una salida sin contrato")
	}
}

// TestVerificarDeterminista: con comandos configurados se ejecutan todos en
// orden y se recogen los exit codes reales (vía inyectada).
func TestVerificarDeterminista(t *testing.T) {
	ejecutados := []string{}
	verif, err := Verificar(OpcionesVerificar{
		Cfg: config.Config{
			LintCommands:  []string{"go vet ./..."},
			TestCommands:  []string{"go test ./..."},
			BuildCommands: []string{"go build ./..."},
		},
		Ejecutar: func(comando string) (int, error) {
			ejecutados = append(ejecutados, comando)
			if strings.Contains(comando, "test") {
				return 1, nil
			}
			return 0, nil
		},
	})
	if err != nil {
		t.Fatalf("Verificar falló: %v", err)
	}
	if verif.Modo != ModoDeterminista {
		t.Errorf("Modo = %q, esperado %q", verif.Modo, ModoDeterminista)
	}
	if len(ejecutados) != 3 {
		t.Fatalf("se ejecutaron %d comandos, esperado 3: %v", len(ejecutados), ejecutados)
	}
	if verif.Comandos[0].Exit != 0 || verif.Comandos[1].Exit != 1 || verif.Comandos[2].Exit != 0 {
		t.Errorf("exit codes = %+v, esperado [0 1 0]", verif.Comandos)
	}
}

// TestVerificarDeterministaErrorEjecucion: si un comando no se puede lanzar
// (binario ausente), la verificación falla con el error, no con exit code.
func TestVerificarDeterministaErrorEjecucion(t *testing.T) {
	_, err := Verificar(OpcionesVerificar{
		Cfg: config.Config{TestCommands: []string{"go test ./..."}},
		Ejecutar: func(comando string) (int, error) {
			return 0, errBinarioAusente
		},
	})
	if err == nil {
		t.Error("Verificar aceptó un error de ejecución sin propagarlo")
	}
}

// TestVerificarSinConfigDelegaSinAgente: elegir delegar sin agente degrada a
// omitido, nunca bloquea.
func TestVerificarSinConfigDelegaSinAgente(t *testing.T) {
	verif, err := Verificar(OpcionesVerificar{
		Preguntar: func(aviso string) (string, error) {
			return "delegar", nil
		},
	})
	if err != nil {
		t.Fatalf("Verificar falló: %v", err)
	}
	if verif.Modo != ModoOmitido || verif.Motivo != "sin_agente" {
		t.Errorf("Modo = %q Motivo = %q, esperado omitido/sin_agente", verif.Modo, verif.Motivo)
	}
}

// TestVerificarPreguntarErrorNoBloquea: si el aviso no se puede leer, se
// degrada a omitido con motivo propio y SIN error (la verificación nunca
// bloquea el flujo).
func TestVerificarPreguntarErrorNoBloquea(t *testing.T) {
	verif, err := Verificar(OpcionesVerificar{
		Preguntar: func(aviso string) (string, error) {
			return "", errSinRespuesta
		},
	})
	if err != nil {
		t.Fatalf("Verificar no debe propagar el error del aviso: %v", err)
	}
	if verif.Modo != ModoOmitido || verif.Motivo != "aviso_no_respondio" {
		t.Errorf("Modo = %q Motivo = %q, esperado omitido/aviso_no_respondio", verif.Modo, verif.Motivo)
	}
}

// TestVerificarAgenteFallaNoBloquea: un agente que no responde (o devuelve
// unavailable) degrada a omitido con motivo: aviso, nunca bloqueo.
func TestVerificarAgenteFallaNoBloquea(t *testing.T) {
	casos := map[string]struct {
		agente *agenteFakeVerificar
		motivo string
	}{
		"unavailable":       {&agenteFakeVerificar{salida: "unavailable"}, "agente_unavailable"},
		"contrato inválido": {&agenteFakeVerificar{salida: "respuesta sin contrato"}, "contrato_invalido"},
		"error":             {&agenteFakeVerificar{salida: "", err: errSinAgente}, "agente_no_respondio"},
	}
	for nombre, caso := range casos {
		verif, err := Verificar(OpcionesVerificar{
			Agente: caso.agente,
			Preguntar: func(aviso string) (string, error) {
				return "delegar", nil
			},
		})
		if err != nil {
			t.Fatalf("%s: Verificar falló: %v", nombre, err)
		}
		if verif.Modo != ModoOmitido || verif.Motivo != caso.motivo {
			t.Errorf("%s: Modo = %q Motivo = %q, esperado omitido/%s", nombre, verif.Modo, verif.Motivo, caso.motivo)
		}
	}
}

// TestVerificarSinConfigDelega: sin comandos configurados, el aviso ofrece
// delegar y el agente devuelve el contrato tested.
func TestVerificarSinConfigDelega(t *testing.T) {
	agente := &agenteFakeVerificar{salida: "todo verde\ntested: make test; go vet ./..."}
	verif, err := Verificar(OpcionesVerificar{
		Agente: agente,
		Preguntar: func(aviso string) (string, error) {
			if !strings.Contains(aviso, "test_commands") {
				t.Errorf("el aviso no menciona test_commands: %q", aviso)
			}
			return "delegar", nil
		},
	})
	if err != nil {
		t.Fatalf("Verificar falló: %v", err)
	}
	if verif.Modo != ModoDelegado {
		t.Errorf("Modo = %q, esperado %q", verif.Modo, ModoDelegado)
	}
	if len(verif.Tested) != 2 || verif.Tested[0] != "make test" {
		t.Errorf("Tested = %#v", verif.Tested)
	}
}

// TestVerificarSinConfigOmitir: la elección omitir no ejecuta nada ni delega.
func TestVerificarSinConfigOmitir(t *testing.T) {
	verif, err := Verificar(OpcionesVerificar{
		Agente: &agenteFakeVerificar{salida: "nunca se llama"},
		Preguntar: func(aviso string) (string, error) {
			return "omitir", nil
		},
	})
	if err != nil {
		t.Fatalf("Verificar falló: %v", err)
	}
	if verif.Modo != ModoOmitido {
		t.Errorf("Modo = %q, esperado %q", verif.Modo, ModoOmitido)
	}
	if len(verif.Tested) != 0 || len(verif.Comandos) != 0 {
		t.Errorf("omitir no debe ejecutar nada: %+v", verif)
	}
}

// TestVerificarGitDirRelativoDevuelveError: un GitDir no vacío pero relativo
// es un caller mal cableado (contrato: git.ObtenerGitDir siempre devuelve una
// ruta absoluta), y debe fallar de forma ruidosa y rápida (antes de pagar
// verificarInterno, que sí tiene efectos reales) en vez de omitir el registro
// del evento pr-verify en silencio. t.Chdir aísla el cwd en un directorio
// descartable: si el guard alguna vez regresa a escribir antes de comprobar
// filepath.IsAbs, la escritura cae ahí y no en el árbol del repositorio.
func TestVerificarGitDirRelativoDevuelveError(t *testing.T) {
	t.Chdir(t.TempDir())
	// Sin Preguntar/Ejecutar/Agente: verificarInterno degradaría en silencio a
	// ModoOmitido si llegara a ejecutarse. El guard debe rechazar antes de
	// llegar ahí, así que el único resultado válido es el error de cableado.
	_, err := Verificar(OpcionesVerificar{GitDir: "gitdir-relativo"})
	if err == nil {
		t.Fatal("Verificar con GitDir relativo debe devolver error, no omitir en silencio")
	}
	if !errors.Is(err, errGitDirRelativo) {
		t.Errorf("el error debe envolver errGitDirRelativo (distinguible con errors.Is), got: %v", err)
	}
	if !strings.Contains(err.Error(), "gitdir-relativo") {
		t.Errorf("el error debe citar la ruta recibida, got: %v", err)
	}
}

// TestVerificarSinConfigConfigurar: la elección configurar devuelve el modo
// para que el caller pare y edite el yml.
func TestVerificarSinConfigConfigurar(t *testing.T) {
	verif, err := Verificar(OpcionesVerificar{
		Preguntar: func(aviso string) (string, error) {
			return "configurar", nil
		},
	})
	if err != nil {
		t.Fatalf("Verificar falló: %v", err)
	}
	if verif.Modo != ModoConfigurar {
		t.Errorf("Modo = %q, esperado %q", verif.Modo, ModoConfigurar)
	}
}

// TestTextoAvisoVerificacion: menciona la elección y distingue CI detectada.
func TestTextoAvisoVerificacion(t *testing.T) {
	sinCI := textoAvisoVerificacion(false)
	if !strings.Contains(sinCI, "CI") || !strings.Contains(sinCI, "delegar") {
		t.Errorf("aviso sin CI incompleto: %q", sinCI)
	}
	conCI := textoAvisoVerificacion(true)
	if !strings.Contains(conCI, "detectó") {
		t.Errorf("aviso con CI no la menciona: %q", conCI)
	}
}
