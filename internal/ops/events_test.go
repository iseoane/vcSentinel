package ops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// escribirEventoPrCreate anexa un evento pr-create de prueba con número de PR.
func escribirEventoPrCreate(t *testing.T, gitDir string, numero string, tieneURL bool) {
	t.Helper()
	detail := map[string]any{"fallback": !tieneURL, "chain_pr": false}
	if tieneURL {
		detail["pr_url"] = "https://github.com/demo/repo/pull/" + numero
	}
	datos, _ := json.Marshal(detail)
	if err := RegistrarEvento(gitDir, "pr-create", 0, nil, string(datos), "C:\\repo"); err != nil {
		t.Fatalf("no se pudo registrar el evento: %v", err)
	}
}

// TestPurgeEventosDePRsResueltas: las PRs mergeadas o cerradas se purgan del
// log; las abiertas o draft se conservan.
func TestPurgeEventosDePRsResueltas(t *testing.T) {
	gitDir := t.TempDir()
	escribirEventoPrCreate(t, gitDir, "10", true) // MERGED → purgar
	escribirEventoPrCreate(t, gitDir, "11", true)  // OPEN → conservar
	escribirEventoPrCreate(t, gitDir, "12", true)  // CLOSED → purgar
	escribirEventoPrCreate(t, gitDir, "13", true)  // DRAFT → conservar

	estados := map[string]string{
		"10": "MERGED",
		"11": "OPEN",
		"12": "CLOSED",
		"13": "DRAFT",
	}
	res, err := PurgeEventosDePRsResueltas(gitDir, func(numero int) (string, error) {
		return estados[strconv.Itoa(numero)], nil
	})
	if err != nil {
		t.Fatalf("Purge... falló: %v", err)
	}
	if res.Purgadas != 2 {
		t.Errorf("Purgadas = %d, esperado 2 (10 y 12)", res.Purgadas)
	}
	if res.Conservadas != 2 {
		t.Errorf("Conservadas = %d, esperado 2 (11 y 13)", res.Conservadas)
	}

	eventos, err := UltimosEventos(gitDir, 0)
	if err != nil {
		t.Fatalf("UltimosEventos falló: %v", err)
	}
	if len(eventos) != 2 {
		t.Fatalf("quedaron %d eventos, esperado 2", len(eventos))
	}
	for _, ev := range eventos {
		if !strings.Contains(ev.Detail, "/pull/11") && !strings.Contains(ev.Detail, "/pull/13") {
			t.Errorf("sobrevivió un evento indebido: %s", ev.Detail)
		}
	}
}

// TestPurgaFallbackSinURLSeConserva: una acta por fallback (sin URL) no es
// verificable con gh y se conserva intacta, con aviso.
func TestPurgaFallbackSinURLSeConserva(t *testing.T) {
	gitDir := t.TempDir()
	escribirEventoPrCreate(t, gitDir, "0", false)

	res, err := PurgeEventosDePRsResueltas(gitDir, func(numero int) (string, error) {
		t.Error("no debe consultar gh para una acta sin URL")
		return "OPEN", nil
	})
	if err != nil {
		t.Fatalf("Purge... falló: %v", err)
	}
	if res.Conservadas != 1 || res.Purgadas != 0 {
		t.Errorf("Conservadas = %d Purgadas = %d, esperado 1/0", res.Conservadas, res.Purgadas)
	}
	if len(res.Avisos) != 1 {
		t.Errorf("Avisos = %d, esperado 1 (acta no verificable)", len(res.Avisos))
	}
}

// TestPurgaGHErrorConserva: sin gh o sin red, la purga es best-effort: el
// fallo se avisa y las actas se conservan (nunca se destruyen por incertidumbre).
func TestPurgaGHErrorConserva(t *testing.T) {
	gitDir := t.TempDir()
	escribirEventoPrCreate(t, gitDir, "20", true)
	escribirEventoPrCreate(t, gitDir, "21", true)

	res, err := PurgeEventosDePRsResueltas(gitDir, func(numero int) (string, error) {
		return "", errors.New("gh no está o sin red")
	})
	if err != nil {
		t.Fatalf("Purge... no debe fallar ante gh ausente: %v", err)
	}
	if res.Purgadas != 0 || res.Conservadas != 2 {
		t.Errorf("Purgadas = %d Conservadas = %d, esperado 0/2", res.Purgadas, res.Conservadas)
	}
	if len(res.Avisos) != 2 {
		t.Errorf("Avisos = %d, esperado 2", len(res.Avisos))
	}
	if _, err := os.Stat(filepath.Join(gitDir, eventosRel)); err != nil {
		t.Fatalf("el log debe seguir existiendo: %v", err)
	}
}

// TestVerificarRegistraPrVerify: con GitDir, Verificar registra el evento
// pr-verify en los tres modos con el detail del esquema §13.
func TestVerificarRegistraPrVerify(t *testing.T) {
	t.Run("determinista", func(t *testing.T) {
		gitDir := t.TempDir()
		_, err := Verificar(OpcionesVerificar{
			GitDir: gitDir,
			Cfg: config.Config{
				LintCommands: []string{"go vet ./..."},
				TestCommands: []string{"go test ./..."},
			},
			Ejecutar: func(comando string) (int, error) {
				if strings.Contains(comando, "test") {
					return 1, nil
				}
				return 0, nil
			},
		})
		if err != nil {
			t.Fatalf("Verificar falló: %v", err)
		}
		eventos, _ := UltimosEventos(gitDir, 1)
		if len(eventos) != 1 || eventos[0].Cmd != "pr-verify" {
			t.Fatalf("sin evento pr-verify: %+v", eventos)
		}
		if eventos[0].Exit != 1 {
			t.Errorf("Exit = %d, esperado 1 (el exit code peor)", eventos[0].Exit)
		}
		if !strings.Contains(eventos[0].Detail, `"exit":1`) {
			t.Errorf("el detail no reporta el exit por comando: %s", eventos[0].Detail)
		}
	})

	t.Run("delegado", func(t *testing.T) {
		gitDir := t.TempDir()
		_, err := Verificar(OpcionesVerificar{
			GitDir: gitDir,
			Agente: &agenteFakeVerificar{salida: "tested: make test"},
			Preguntar: func(aviso string) (string, error) {
				return "delegar", nil
			},
		})
		if err != nil {
			t.Fatalf("Verificar falló: %v", err)
		}
		eventos, err := UltimosEventos(gitDir, 1)
		if err != nil || len(eventos) != 1 {
			t.Fatalf("sin evento pr-verify: %v %+v", err, eventos)
		}
		if !strings.Contains(eventos[0].Detail, `"tested"`) || !strings.Contains(eventos[0].Detail, `make test`) {
			t.Errorf("el detail no reporta el contrato tested: %s", eventos[0].Detail)
		}
	})

	t.Run("motivo", func(t *testing.T) {
		gitDir := t.TempDir()
		_, err := Verificar(OpcionesVerificar{
			GitDir:   gitDir,
			Preguntar: func(aviso string) (string, error) {
				return "omitir", nil
			},
		})
		if err != nil {
			t.Fatalf("Verificar falló: %v", err)
		}
		eventos, err := UltimosEventos(gitDir, 1)
		if err != nil || len(eventos) != 1 {
			t.Fatalf("evento pr-verify: %+v", eventos)
		}
		if !strings.Contains(eventos[0].Detail, `"motivo":"omitido"`) {
			t.Errorf("el detail no reporta el motivo: %s", eventos[0].Detail)
		}
	})

	t.Run("sin gitDir no registra", func(t *testing.T) {
		temp := t.TempDir()
		_, err := Verificar(OpcionesVerificar{
			Cfg: config.Config{LintCommands: []string{"echo hola"}},
			Ejecutar: func(comando string) (int, error) {
				return 0, nil
			},
		})
		if err != nil {
			t.Fatalf("Verificar falló: %v", err)
		}
		// No hay gitDir: no debe haber events.jsonl en el temp dir.
		if _, err := os.Stat(filepath.Join(temp, "vas-sentinel", "events.jsonl")); err == nil {
			t.Error("Verificar escribió eventos sin GitDir")
		}
	})
}