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

func TestRegistrarYLeerEventos(t *testing.T) {
	dir := t.TempDir()

	if err := RegistrarEvento(dir, "check", 0, nil, "", ""); err != nil {
		t.Fatalf("RegistrarEvento devolvió error: %v", err)
	}
	if err := RegistrarEvento(dir, "review", 4, []string{"abc123"}, "provider_unavailable", filepath.Join("C:", "repo")); err != nil {
		t.Fatalf("RegistrarEvento devolvió error: %v", err)
	}

	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Fatalf("eventos = %d, esperado 2", len(eventos))
	}
	// El más reciente primero.
	if eventos[0].Cmd != "review" || eventos[0].Exit != 4 || eventos[0].Detail != "provider_unavailable" {
		t.Errorf("evento más reciente = %+v, no coincide", eventos[0])
	}
	if len(eventos[0].Shas) != 1 || eventos[0].Shas[0] != "abc123" {
		t.Errorf("shas = %+v, esperado [abc123]", eventos[0].Shas)
	}
	if eventos[1].Cmd != "check" {
		t.Errorf("evento anterior = %+v, esperado check", eventos[1])
	}
}

func TestUltimosEventosLimite(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	eventos, err := UltimosEventos(dir, 3)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 3 {
		t.Fatalf("eventos = %d, esperado 3", len(eventos))
	}
}

func TestUltimosEventosSinLog(t *testing.T) {
	dir := t.TempDir()

	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 0 {
		t.Errorf("eventos = %d, esperado 0 sin log", len(eventos))
	}
}

func TestRegistrarMilEventosSinCorrupcion(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 1000; i++ {
		if err := RegistrarEvento(dir, "check", 0, nil, "", ""); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	eventos, err := UltimosEventos(dir, 0)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 1000 {
		t.Errorf("eventos = %d, esperado 1000", len(eventos))
	}
}

func TestRotarEventosDejaUltimasLineas(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	if err := RotarEventos(dir, 2); err != nil {
		t.Fatalf("RotarEventos devolvió error: %v", err)
	}
	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Errorf("eventos = %d, esperado 2 tras rotar", len(eventos))
	}

	// El log sigue siendo appendable tras la rotación.
	if err := RegistrarEvento(dir, "review", 1, nil, "", ""); err != nil {
		t.Fatalf("RegistrarEvento tras rotar devolvió error: %v", err)
	}
	eventos, err = UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 3 || eventos[0].Cmd != "review" {
		t.Errorf("eventos = %d, el más reciente %q, esperado 3 con review", len(eventos), eventos[0].Cmd)
	}
}

func TestRotarEventosSinLogEsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RotarEventos(dir, 100); err != nil {
		t.Fatalf("RotarEventos sin log devolvió error: %v", err)
	}
}

func TestRotarEventosDescartaLineasCorruptas(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	ruta := filepath.Join(dir, eventosRel)
	if err := appendLinea(ruta, "{\"corrupto\""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "review", 0, nil, "", ""); err != nil {
		t.Fatal(err)
	}

	if err := RotarEventos(dir, 10); err != nil {
		t.Fatalf("RotarEventos devolvió error: %v", err)
	}
	// El archivo reescrito solo contiene las líneas que se pudieron parsear
	// como evento: la corrupta se descarta en la rotación.
	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Errorf("eventos = %d, esperado 2 (las corruptas se descartan al rotar)", len(eventos))
	}
}

func TestPurgeEventosDeBorraSoloLosShasPedidos(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "review", 4, []string{"aaa111"}, "provider_unavailable", ""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "review", 0, []string{"bbb222"}, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "status", 0, nil, "", ""); err != nil {
		t.Fatal(err)
	}

	eliminadas, err := PurgeEventosDe(dir, []string{"aaa111"})
	if err != nil {
		t.Fatalf("PurgeEventosDe devolvió error: %v", err)
	}
	if eliminadas != 1 {
		t.Errorf("líneas eliminadas = %d, esperado 1", eliminadas)
	}

	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Fatalf("eventos = %d, esperado 2 (los de commits vivos se conservan)", len(eventos))
	}
	// El evento de aaa111 desapareció; el de bbb222 y el status siguen.
	if eventos[0].Cmd != "status" || eventos[1].Cmd != "review" || eventos[1].Shas[0] != "bbb222" {
		t.Errorf("eventos restantes = %+v, no coinciden", eventos)
	}

	// El log sigue siendo appendable tras el purge.
	if err := RegistrarEvento(dir, "check", 0, nil, "", ""); err != nil {
		t.Fatalf("RegistrarEvento tras purge devolvió error: %v", err)
	}
}

func TestPurgeEventosDeSinShasEsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "review", 0, []string{"aaa111"}, "", ""); err != nil {
		t.Fatal(err)
	}
	eliminadas, err := PurgeEventosDe(dir, nil)
	if err != nil || eliminadas != 0 {
		t.Errorf("PurgeEventosDe(nil) = %d, %v; esperado 0, nil", eliminadas, err)
	}
	eventos, _ := UltimosEventos(dir, 10)
	if len(eventos) != 1 {
		t.Errorf("eventos = %d, esperado 1 (sin cambios)", len(eventos))
	}
}

func TestPurgeEventosDeSinLogEsNoOp(t *testing.T) {
	dir := t.TempDir()
	eliminadas, err := PurgeEventosDe(dir, []string{"aaa111"})
	if err != nil || eliminadas != 0 {
		t.Errorf("PurgeEventosDe sin log = %d, %v; esperado 0, nil", eliminadas, err)
	}
}

func appendLinea(ruta, linea string) error {
	archivo, err := os.OpenFile(ruta, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer archivo.Close()
	_, err = archivo.WriteString(linea + "\n")
	return err
}

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
	escribirEventoPrCreate(t, gitDir, "11", true) // OPEN → conservar
	escribirEventoPrCreate(t, gitDir, "12", true) // CLOSED → purgar
	escribirEventoPrCreate(t, gitDir, "13", true) // DRAFT → conservar

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

	// El log sigue siendo appendable tras la purga por PR.
	if err := RegistrarEvento(gitDir, "pr-review", 0, []string{"zzz999"}, "", ""); err != nil {
		t.Fatalf("RegistrarEvento tras purga por PR devolvió error: %v", err)
	}
	eventos, err = UltimosEventos(gitDir, 10)
	if err != nil || len(eventos) != 3 {
		t.Errorf("eventos = %d, %v; esperado 3 tras reanexar", len(eventos), err)
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

// TestPurgaDetailCorruptoConservaYNoAborta: una acta con detail inválido se
// conserva con aviso y el resto de la purga sigue adelante (best-effort).
func TestPurgaDetailCorruptoConservaYNoAborta(t *testing.T) {
	gitDir := t.TempDir()
	escribirEventoPrCreate(t, gitDir, "30", true) // MERGED → purgar
	// Actas corruptas (no JSON de DetallePrCreate).
	_ = RegistrarEvento(gitDir, "pr-create", 0, nil, "no es json", "")
	_ = RegistrarEvento(gitDir, "pr-create", 0, nil, `{"pr_url": "incompleta"}`, "")

	res, err := PurgeEventosDePRsResueltas(gitDir, func(numero int) (string, error) {
		return "MERGED", nil
	})
	if err != nil {
		t.Fatalf("un detail corrupto no debe abortar la purga: %v", err)
	}
	if res.Purgadas != 1 || res.Conservadas != 2 {
		t.Errorf("Purgadas = %d Conservadas = %d, esperado 1/2", res.Purgadas, res.Conservadas)
	}
	if len(res.Avisos) != 2 {
		t.Errorf("Avisos = %d, esperado 2 (las actas inválidas se avisan)", len(res.Avisos))
	}
	eventos, err := UltimosEventos(gitDir, 0)
	if err != nil || len(eventos) != 2 {
		t.Errorf("eventos = %d, %v; esperado 2 conservadas", len(eventos), err)
	}
}

// TestNumeroDePR: tolera sufijos tras el número (/pull/10/files).
func TestNumeroDePR(t *testing.T) {
	casos := map[string]int{
		"https://github.com/a/b/pull/42":         42,
		"https://github.com/a/b/pull/42/":        42,
		"https://github.com/a/b/pull/42/files":   42,
		"https://github.com/a/b/pull/42/commits": 42,
	}
	for url, esperado := range casos {
		n, ok := numeroDePR(url)
		if !ok || n != esperado {
			t.Errorf("numeroDePR(%q) = %d, %v; esperado %d", url, n, ok, esperado)
		}
	}
	if _, ok := numeroDePR("https://github.com/a/b/issues/7"); ok {
		t.Error("numeroDePR aceptó una URL sin /pull/")
	}
	if _, ok := numeroDePR("https://github.com/a/b/pull/abc"); ok {
		t.Error("numeroDePR aceptó un número no numérico")
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
			GitDir: gitDir,
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
		_, err := Verificar(OpcionesVerificar{
			Cfg: config.Config{LintCommands: []string{"echo hola"}},
			Ejecutar: func(comando string) (int, error) {
				return 0, nil
			},
		})
		if err != nil {
			t.Fatalf("Verificar sin GitDir no debe fallar por registro: %v", err)
		}
	})
}
