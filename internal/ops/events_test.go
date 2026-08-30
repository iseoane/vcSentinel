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

var _ func(string, string, int, []string, EventDetail, string) error = RegistrarEvento

func TestRegistrarYLeerEventos(t *testing.T) {
	dir := t.TempDir()
	writeEventLines(t, dir,
		`{"at":"2026-01-01T00:00:00Z","cmd":"check","exit":0,"detail":{}}`,
		`{"at":"2026-01-01T00:00:01Z","cmd":"review","exit":4,"shas":["abc123"],"detail":"provider_unavailable","worktree":"C:\\repo"}`,
	)

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
		if err := RegistrarEvento(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
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
		if err := RegistrarEvento(dir, "check", 0, nil, EventDetail{}, ""); err != nil {
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
		if err := RegistrarEvento(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
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
	if err := RegistrarEvento(dir, "review", 1, nil, EventDetail{}, ""); err != nil {
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
	if err := RegistrarEvento(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}
	ruta := filepath.Join(dir, eventosRel)
	if err := appendLinea(ruta, "{\"corrupto\""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "review", 0, nil, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}

	if err := RotarEventos(dir, 10); err != nil {
		t.Fatalf("RotarEventos devolvió error: %v", err)
	}
	// La rotación descarta las líneas corruptas: quedan solo los eventos
	// que parsean, en orden.
	eventos, err := UltimosEventos(dir, 10)
	if err != nil {
		t.Fatalf("UltimosEventos devolvió error: %v", err)
	}
	if len(eventos) != 2 {
		t.Errorf("eventos = %d, esperado 2 (la corrupta se descartó en la rotación)", len(eventos))
	}
	// El más reciente primero (review se registró después de status).
	if eventos[0].Cmd != "review" || eventos[1].Cmd != "status" {
		t.Errorf("orden = %q, %q; esperado review, status", eventos[0].Cmd, eventos[1].Cmd)
	}
	// El archivo ya no contiene la línea corrupta.
	contenido, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contenido), "corrupto") {
		t.Errorf("la línea corrupta sigue en el archivo tras la rotación")
	}
}

func TestPurgeEventosDeBorraSoloLosShasPedidos(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "review", 4, []string{"aaa111"}, EventDetail{"reason": "provider_unavailable"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "review", 0, []string{"bbb222"}, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := RegistrarEvento(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
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
	if err := RegistrarEvento(dir, "check", 0, nil, EventDetail{}, ""); err != nil {
		t.Fatalf("RegistrarEvento tras purge devolvió error: %v", err)
	}
}

func TestPurgeEventosDeSinShasEsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "review", 0, []string{"aaa111"}, EventDetail{}, ""); err != nil {
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

func writeEventLines(t *testing.T, gitDir string, lines ...string) {
	t.Helper()
	ruta := filepath.Join(gitDir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	contenido := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}
}

// escribirEventoPrCreate anexa un evento pr-create de prueba con número de PR.
func escribirEventoPrCreate(t *testing.T, gitDir string, numero string, tieneURL bool) {
	t.Helper()
	detail := EventDetail{"fallback": !tieneURL, "chain_pr": false}
	if tieneURL {
		detail["pr_url"] = "https://github.com/demo/repo/pull/" + numero
	}
	if err := RegistrarEvento(gitDir, "pr-create", 0, nil, detail, "C:\\repo"); err != nil {
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
		text := detailText(t, ev.Detail)
		if !strings.Contains(text, "/pull/11") && !strings.Contains(text, "/pull/13") {
			t.Errorf("sobrevivió un evento indebido: %s", text)
		}
	}

	// El log sigue siendo appendable tras la purga por PR.
	if err := RegistrarEvento(gitDir, "pr-review", 0, []string{"zzz999"}, EventDetail{}, ""); err != nil {
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

// TestEscribirLogTemporalSinLogPrevio: sin log previo el temporal pasa a ser
// el log (no se crea .bak).
func TestEscribirLogTemporalSinLogPrevio(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}

	if err := escribirLogTemporalRenombrando(ruta, [][]byte{[]byte("a"), []byte("b")}, os.Rename); err != nil {
		t.Fatalf("escribirLogTemporal sin log previo falló: %v", err)
	}
	datos, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("el log debe existir tras la escritura: %v", err)
	}
	if string(datos) != "a\nb\n" {
		t.Errorf("log = %q, esperado %q", datos, "a\nb\n")
	}
	if _, err := os.Stat(ruta + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("sin log previo no debe quedar .bak: %v", err)
	}
}

// TestEscribirLogTemporalRestauraBackup: si el rename temp→ruta falla, el
// .bak se restaura y el log original queda intacto.
func TestEscribirLogTemporalRestauraBackup(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := escribirLogTemporalRenombrando(ruta, [][]byte{[]byte("nuevo")}, func(origen, destino string) error {
		if strings.HasSuffix(origen, ".tmp") {
			return errors.New("simulado: rename temp→ruta falla")
		}
		return os.Rename(origen, destino)
	})
	if err == nil {
		t.Fatal("esperaba error ante rename fallido")
	}
	datos, err := os.ReadFile(ruta)
	if err != nil || string(datos) != "original\n" {
		t.Errorf("el log original debe restaurarse, got %q, %v", datos, err)
	}
	if _, err := os.Stat(ruta + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("el .bak debe consumirse en la restauración: %v", err)
	}
}

// TestEscribirLogTemporalFalloDobleInformaBak: si la restauración también
// falla, el error informa dónde quedó el respaldo (recuperación manual).
func TestEscribirLogTemporalFalloDobleInformaBak(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := escribirLogTemporalRenombrando(ruta, [][]byte{[]byte("nuevo")}, func(origen, destino string) error {
		if strings.HasSuffix(origen, ".tmp") || strings.HasSuffix(origen, ".bak") {
			return errors.New("simulado: rename falla")
		}
		return os.Rename(origen, destino)
	})
	if err == nil {
		t.Fatal("esperaba error ante doble fallo de rename")
	}
	if !strings.Contains(err.Error(), ".bak") {
		t.Errorf("el error debe indicar dónde está el respaldo: %v", err)
	}
	if _, err := os.Stat(ruta + ".bak"); err != nil {
		t.Errorf("el original debe quedar a salvo como .bak: %v", err)
	}
}

// TestEscribirLogTemporalToleraBakResidual: un .bak residual de una ejecución
// interrumpida no bloquea la siguiente escritura (Windows no renombra sobre
// destino existente) y se limpia al terminar.
func TestEscribirLogTemporalToleraBakResidual(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta+".bak", []byte("viejos datos\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := escribirLogTemporalRenombrando(ruta, [][]byte{[]byte("nuevo")}, os.Rename); err != nil {
		t.Fatalf("un .bak residual no debe bloquear la escritura: %v", err)
	}
	datos, err := os.ReadFile(ruta)
	if err != nil || string(datos) != "nuevo\n" {
		t.Errorf("log = %q, %v; esperado %q", datos, err, "nuevo\n")
	}
	if _, err := os.Stat(ruta + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("el .bak residual debe limpiarse: %v", err)
	}
}

// TestPurgaDetailCorruptoConservaYNoAborta: una acta con detail inválido se
// conserva con aviso y el resto de la purga sigue adelante (best-effort).
func TestPurgaDetailCorruptoConservaYNoAborta(t *testing.T) {
	gitDir := t.TempDir()
	escribirEventoPrCreate(t, gitDir, "30", true) // MERGED → purgar
	// Actas corruptas (no JSON de DetallePrCreate), seeded as historical JSONL.
	ruta := filepath.Join(gitDir, eventosRel)
	if err := appendLinea(ruta, `{"at":"2026-01-01T00:00:01Z","cmd":"pr-create","exit":0,"detail":"no es json"}`); err != nil {
		t.Fatal(err)
	}
	if err := appendLinea(ruta, `{"at":"2026-01-01T00:00:02Z","cmd":"pr-create","exit":0,"detail":"{\"pr_url\": \"incompleta\"}"}`); err != nil {
		t.Fatal(err)
	}

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
		t.Fatalf("Avisos = %d, esperado 2 (las actas inválidas se avisan)", len(res.Avisos))
	}
	avisoTexto := strings.Join(res.Avisos, "\n")
	if !strings.Contains(avisoTexto, "inválido") || !strings.Contains(avisoTexto, "se conserva") {
		t.Errorf("los avisos deben explicar la conservación: %v", res.Avisos)
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
		if !strings.Contains(detailText(t, eventos[0].Detail), `"exit":1`) {
			t.Errorf("el detail no reporta el exit por comando: %s", detailText(t, eventos[0].Detail))
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
		if !strings.Contains(detailText(t, eventos[0].Detail), `"tested"`) || !strings.Contains(detailText(t, eventos[0].Detail), `make test`) {
			t.Errorf("el detail no reporta el contrato tested: %s", detailText(t, eventos[0].Detail))
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
		if !strings.Contains(detailText(t, eventos[0].Detail), `"motivo":"omitido"`) {
			t.Errorf("el detail no reporta el motivo: %s", detailText(t, eventos[0].Detail))
		}
	})

	t.Run("sin gitDir no registra", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		_, err := Verificar(OpcionesVerificar{
			Cfg: config.Config{LintCommands: []string{"echo hola"}},
			Ejecutar: func(comando string) (int, error) {
				return 0, nil
			},
		})
		if err != nil {
			t.Fatalf("Verificar sin GitDir no debe fallar por registro: %v", err)
		}
		// Sin GitDir no hay efectos secundarios: no se crea el log.
		if _, err := os.Stat(filepath.Join(dir, eventosRel)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("sin GitDir no debe crearse events.jsonl: %v", err)
		}
	})
}

func TestRecordEventWritesStructuredObjectDetail(t *testing.T) {
	dir := t.TempDir()
	detail := EventDetail{
		"reason":  "ripgrep execution failed",
		"unknown": map[string]any{"kept": true},
	}
	if err := RegistrarEvento(dir, "review", 4, nil, detail, ""); err != nil {
		t.Fatalf("RegistrarEvento failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, eventosRel))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("event is not valid JSON: %v", err)
	}
	if !strings.HasPrefix(string(envelope.Detail), "{") {
		t.Fatalf("detail = %s, want a JSON object rather than an encoded string", envelope.Detail)
	}
	object := map[string]any{}
	if err := json.Unmarshal(envelope.Detail, &object); err != nil {
		t.Fatalf("structured detail is not an object: %v", err)
	}
	if object["reason"] != "ripgrep execution failed" {
		t.Errorf("reason = %v, want the producer value", object["reason"])
	}
	unknown, ok := object["unknown"].(map[string]any)
	if !ok || unknown["kept"] != true {
		t.Errorf("unknown fields = %#v, want them preserved", object["unknown"])
	}
}

func TestLatestEventsReadsMixedLegacyAndStructuredDetails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"at":"2026-01-01T00:00:00Z","cmd":"legacy-json","exit":0,"detail":"{\"legacy\":\"value\",\"unknown\":{\"kept\":true}}"}`,
		`{"at":"2026-01-01T00:00:01Z","cmd":"legacy-text","exit":4,"detail":"provider_unavailable"}`,
		`{"at":"2026-01-01T00:00:02Z","cmd":"new-object","exit":0,"detail":{"new":"value","unknown":{"number":7}}}`,
	}
	original := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	events, err := UltimosEventos(dir, 0)
	if err != nil {
		t.Fatalf("UltimosEventos failed: %v", err)
	}
	if len(events) != len(lines) {
		t.Fatalf("events = %d, want %d mixed lines", len(events), len(lines))
	}
	// UltimosEventos presents the newest event first; restore the stream order
	// here to verify that decoding never loses or reorders a historical line.
	chronological := make([]Evento, len(events))
	for i := range events {
		chronological[len(events)-1-i] = events[i]
	}
	if chronological[0].Cmd != "legacy-json" || chronological[1].Cmd != "legacy-text" || chronological[2].Cmd != "new-object" {
		t.Fatalf("commands = %q, %q, %q; want original stream order", chronological[0].Cmd, chronological[1].Cmd, chronological[2].Cmd)
	}
	legacyObject := structuredDetail(t, chronological[0].Detail)
	if legacyObject["legacy"] != "value" {
		t.Errorf("legacy object detail = %#v, want its fields decoded", legacyObject)
	}
	legacyUnknown, ok := legacyObject["unknown"].(map[string]any)
	if !ok || legacyUnknown["kept"] != true {
		t.Errorf("legacy unknown fields = %#v, want them preserved", legacyObject["unknown"])
	}
	if chronological[1].Detail != "provider_unavailable" {
		t.Errorf("legacy non-JSON detail = %#v, want the original text", chronological[1].Detail)
	}
	newObject := structuredDetail(t, chronological[2].Detail)
	if newObject["new"] != "value" {
		t.Errorf("new object detail = %#v, want its fields decoded", newObject)
	}
	newUnknown, ok := newObject["unknown"].(map[string]any)
	if !ok || newUnknown["number"] != float64(7) {
		t.Errorf("new unknown fields = %#v, want them preserved", newObject["unknown"])
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatal("reading mixed events must not rewrite events.jsonl")
	}
}

func TestPrVerifyProducerWritesStructuredDetail(t *testing.T) {
	gitDir := t.TempDir()
	_, err := Verificar(OpcionesVerificar{
		GitDir: gitDir,
		Cfg:    config.Config{TestCommands: []string{"go test ./..."}},
		Ejecutar: func(string) (int, error) {
			return 1, nil
		},
	})
	if err != nil {
		t.Fatalf("Verificar failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(gitDir, eventosRel))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("event is not valid JSON: %v", err)
	}
	if !strings.HasPrefix(string(envelope.Detail), "{") {
		t.Fatalf("producer detail = %s, want a JSON object", envelope.Detail)
	}
}

func structuredDetail(t *testing.T, detail any) map[string]any {
	t.Helper()
	object, ok := detail.(map[string]any)
	if !ok {
		t.Fatalf("detail = %#v (%T), want a decoded object", detail, detail)
	}
	return object
}

func detailText(t *testing.T, detail any) string {
	t.Helper()
	if text, ok := detail.(string); ok {
		return text
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	return string(encoded)
}

func TestRotateAndPurgePreserveRetainedLineBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	keep := []byte(`{"at":"2026-01-01T00:00:00Z","cmd":"keep","exit":0,"shas":["keep"],"detail":{"unknown":{"kept":true}}}` + "\r\n")
	remove := []byte(`{"at":"2026-01-01T00:00:01Z","cmd":"remove","exit":0,"shas":["remove"],"detail":{"unknown":{"number":7}}}`)
	original := append(append([]byte(nil), keep...), remove...)
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	if err := RotarEventos(dir, 2); err != nil {
		t.Fatalf("RotarEventos failed: %v", err)
	}
	afterRotate, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRotate) != string(original) {
		t.Fatalf("rotation changed retained bytes: got %q, want %q", afterRotate, original)
	}

	deleted, err := PurgeEventosDe(dir, []string{"remove"})
	if err != nil {
		t.Fatalf("PurgeEventosDe failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("eliminated = %d, want 1", deleted)
	}
	afterPurge, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterPurge) != string(keep) {
		t.Fatalf("purge changed retained bytes: got %q, want %q", afterPurge, keep)
	}
}

func TestRecordEventSeparatesAnUnterminatedLegacyRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"at":"2026-01-01T00:00:00Z","cmd":"legacy","exit":0,"detail":"text"}`
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RegistrarEvento(dir, "new", 0, nil, EventDetail{"kind": "object"}, ""); err != nil {
		t.Fatalf("RegistrarEvento failed: %v", err)
	}
	events, err := UltimosEventos(dir, 0)
	if err != nil {
		t.Fatalf("UltimosEventos failed: %v", err)
	}
	if len(events) != 2 || events[0].Cmd != "new" || events[1].Cmd != "legacy" {
		t.Fatalf("events = %#v, want separate new and legacy records", events)
	}
}

func TestRecordEventRejectsInvalidDetailBeforeCreatingLog(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "invalid", 1, nil, EventDetail{"channel": make(chan int)}, ""); err == nil {
		t.Fatal("RegistrarEvento accepted a detail that JSON cannot encode")
	}
	if _, err := os.Stat(filepath.Join(dir, eventosRel)); !os.IsNotExist(err) {
		t.Fatalf("events.jsonl exists after marshal failure: %v", err)
	}
}

func TestRecordEventRejectsNilDetailBeforeCreatingLog(t *testing.T) {
	dir := t.TempDir()
	if err := RegistrarEvento(dir, "invalid", 1, nil, nil, ""); err == nil {
		t.Fatal("RegistrarEvento accepted a nil detail")
	}
	if _, err := os.Stat(filepath.Join(dir, eventosRel)); !os.IsNotExist(err) {
		t.Fatalf("events.jsonl exists after nil detail: %v", err)
	}
}
