package review

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitSalida ejecuta git en el cwd y devuelve la salida estándar.
func gitSalida(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	salida, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v falló: %v", args, err)
	}
	return string(salida)
}

func gitEjecutar(t *testing.T, args ...string) {
	t.Helper()
	gitSalida(t, args...)
}

// auditorStub responde distinto según el prompt: las auditorías por dimensión
// reciben auditSalida (JSONL válido); el overview (que contiene la palabra
// "coherente" en el prompt) recibe overviewSalida.
type auditorStub struct {
	auditSalida    string
	overviewSalida string
	llamadasAudit  int
	llamadasOv     int
}

func (a *auditorStub) EjecutarPrompt(prompt string) (string, error) {
	if strings.Contains(prompt, "coherente") {
		a.llamadasOv++
		return a.overviewSalida, nil
	}
	a.llamadasAudit++
	return a.auditSalida, nil
}

// salidaAuditOK es un JSONL de auditoría válido para cualquier dimensión
// (ParsearDimensionResult acepta la primera línea con dimensión conocida).
const salidaAuditOK = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"ok\"}\nEND_REVIEW\n"

func fabricaStub(a *auditorStub) FabricaAuditor {
	return func(dimension string) (AuditorAgente, string, error) {
		return a, "stub", nil
	}
}

// prepararRepoRama crea un repo temp con la rama actual en "feature" creada
// desde el primer commit de main, y devuelve el gitDir para el ledger.
func prepararRepoRama(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)

	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		gitEjecutar(t, args...)
	}

	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte(strings.Repeat("b\n", 5)), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutar(t, "add", "base.txt")
	gitEjecutar(t, "commit", "-m", "feat(base): base de la rama")
	gitEjecutar(t, "checkout", "-b", "feature")

	return filepath.Join(repo, ".git")
}

// commitEnRama añade un archivo y commitea en la rama actual; devuelve el SHA.
func commitEnRama(t *testing.T, nombre, contenido string) string {
	t.Helper()
	if err := os.WriteFile(nombre, []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutar(t, "add", nombre)
	gitEjecutar(t, "commit", "-m", "feat("+nombre+"): contenido de prueba")
	return strings.TrimSpace(gitSalida(t, "rev-parse", "HEAD"))
}

// TestAnalizarRamaAuditaPendientes: audita los commits de la rama sin ficha,
// deja las fichas en el ledger y decide single por volumen bajo.
func TestAnalizarRamaAuditaPendientes(t *testing.T) {
	gitDir := prepararRepoRama(t)
	sha := commitEnRama(t, "feat.txt", "1\n2\n3\n4\n5\n")
	ledger := NuevoLedger(gitDir)
	stub := &auditorStub{auditSalida: salidaAuditOK}
	parallel := 2

	res, err := AnalizarRama(ledger, OpcionesRama{Fabrica: fabricaStub(stub), Parallel: parallel})
	if err != nil {
		t.Fatalf("AnalizarRama falló: %v", err)
	}

	if len(res.SHAs) != 1 || res.SHAs[0] != sha {
		t.Errorf("SHAs = %v, esperado [%s]", res.SHAs, sha)
	}
	if len(res.Pendientes) != 1 {
		t.Errorf("Pendientes = %v, esperado [%s] (el commit nuevo)", res.Pendientes, sha)
	}
	if len(res.Fichas) != 1 {
		t.Fatalf("Fichas = %d, esperado 1", len(res.Fichas))
	}
	if res.Fichas[0].SHA != sha || res.Fichas[0].Revisions[0].Result != VerdictOK {
		t.Errorf("ficha incorrecta: %+v", res.Fichas[0])
	}
	if res.Decision != "single" {
		t.Errorf("Decision = %q, esperado single (volumen bajo)", res.Decision)
	}
	if res.Volumen != 5 {
		t.Errorf("Volumen = %d, esperado 5", res.Volumen)
	}

	// La ficha quedó persistida en el ledger.
	persistida, err := ledger.LeerFicha(sha)
	if err != nil || persistida == nil || len(persistida.Revisions) != 1 {
		t.Errorf("la ficha no quedó guardada en el ledger: %v", err)
	}
}

// TestAnalizarRamaSoloPendientes: con la opción activada no audita nada nuevo
// y devuelve las fichas ya existentes.
func TestAnalizarRamaSoloPendientes(t *testing.T) {
	gitDir := prepararRepoRama(t)
	sha := commitEnRama(t, "feat.txt", "1\n2\n3\n")
	ledger := NuevoLedger(gitDir)
	stub := &auditorStub{auditSalida: salidaAuditOK}

	if _, err := AnalizarRama(ledger, OpcionesRama{Fabrica: fabricaStub(stub), Parallel: 1}); err != nil {
		t.Fatalf("primera pasada falló: %v", err)
	}
	llamadasAntes := stub.llamadasAudit

	res, err := AnalizarRama(ledger, OpcionesRama{Fabrica: fabricaStub(stub), Parallel: 1, SoloPendientes: true})
	if err != nil {
		t.Fatalf("AnalizarRama(SoloPendientes) falló: %v", err)
	}

	if len(res.Pendientes) != 0 {
		t.Errorf("Pendientes = %v, esperado vacío (todo ya auditado)", res.Pendientes)
	}
	if len(res.Fichas) != 1 || res.Fichas[0].SHA != sha {
		t.Errorf("Fichas = %+v, esperado solo la ficha existente de %s", res.Fichas, sha)
	}
	if stub.llamadasAudit != llamadasAntes {
		t.Errorf("SoloPendientes audita igualmente: %d llamadas nuevas", stub.llamadasAudit-llamadasAntes)
	}
}

// TestDecisionChainPorVolumen: sin overview, una rama que supera el umbral de
// líneas propone cadena de PRs.
func TestDecisionChainPorVolumen(t *testing.T) {
	gitDir := prepararRepoRama(t)
	commitEnRama(t, "grande.txt", strings.Repeat("x\n", 500))
	ledger := NuevoLedger(gitDir)
	stub := &auditorStub{auditSalida: salidaAuditOK}

	res, err := AnalizarRama(ledger, OpcionesRama{Fabrica: fabricaStub(stub), Parallel: 1})
	if err != nil {
		t.Fatalf("AnalizarRama falló: %v", err)
	}
	if res.Decision != "chain" {
		t.Errorf("Decision = %q, esperado chain (500 líneas, sin overview)", res.Decision)
	}
}

// TestDecisionOverview: el overview (1 llamada Spec de rama) matiza la
// decisión cuando el volumen supera el umbral.
func TestDecisionOverview(t *testing.T) {
	casos := []struct {
		nombre    string
		coherente string
		esperado  string
	}{
		{"coherente → single", `{"coherente":true,"rationale":"Un único cambio: los commits comparten el mismo objetivo y archivos."}`, "single"},
		{"incoherente → chain", `{"coherente":false,"rationale":"Unidades independientes con costuras entre sí."}`, "chain"},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			gitDir := prepararRepoRama(t)
			commitEnRama(t, "grande.txt", strings.Repeat("x\n", 500))
			ledger := NuevoLedger(gitDir)
			stub := &auditorStub{auditSalida: salidaAuditOK, overviewSalida: caso.coherente}

			res, err := AnalizarRama(ledger, OpcionesRama{
				Fabrica: fabricaStub(stub), Parallel: 1, Overview: true,
			})
			if err != nil {
				t.Fatalf("AnalizarRama falló: %v", err)
			}
			if res.Decision != caso.esperado {
				t.Errorf("Decision = %q, esperado %q", res.Decision, caso.esperado)
			}
			if res.Overview == nil || res.Overview.Coherente != (caso.esperado == "single") {
				t.Errorf("Overview = %+v, esperado coherente=%v", res.Overview, caso.esperado == "single")
			}
			if stub.llamadasOv != 1 {
				t.Errorf("el overview debe ser exactamente 1 llamada, fueron %d", stub.llamadasOv)
			}
		})
	}
}

// TestParseOverview: parsea el JSON del agente y rechaza salidas inválidas.
func TestParseOverview(t *testing.T) {
	ok, err := ParseOverview(`texto previo
{"coherente":true,"rationale":"Primera línea.\nSegunda línea."}
texto posterior`)
	if err != nil {
		t.Fatalf("ParseOverview de JSON válido falló: %v", err)
	}
	if !ok.Coherente || !strings.Contains(ok.Rationale, "Primera línea.") {
		t.Errorf("overview parseado = %+v", ok)
	}

	if _, err := ParseOverview("respuesta sin JSON"); err == nil {
		t.Error("ParseOverview aceptó una salida sin JSON")
	}

	// JSON presente pero sin el campo "coherente": rechazo por guarda.
	if _, err := ParseOverview(`{"rationale":"solo texto"}`); err == nil {
		t.Error("ParseOverview aceptó un JSON sin el campo coherente")
	}

	// JSON con "coherente" pero sintácticamente inválido: rechazo por Unmarshal.
	if _, err := ParseOverview(`{"coherente": tru}`); err == nil {
		t.Error("ParseOverview aceptó un JSON malformado")
	}

	// Preamble JSON antes del objeto de coherencia: debe tomar el objeto que
	// contiene "coherente", no el primer { con el último }.
	conPreamble, err := ParseOverview(`{"metadato":1}
{"coherente":false,"rationale":"Dos unidades con costuras."}`)
	if err != nil {
		t.Fatalf("ParseOverview no toleró un preamble JSON: %v", err)
	}
	if conPreamble.Coherente {
		t.Errorf("ParseOverview = %+v, esperado coherente=false", conPreamble)
	}
}

// TestCierreJSON: el matcher de llaves balanceadas tolera strings, escapes y
// anidamiento, y detecta objetos sin cerrar.
func TestCierreJSON(t *testing.T) {
	casos := []struct {
		nombre  string
		salida  string
		desde   int
		espera  int
		cerrado bool
	}{
		{"simple", `{"a":1}`, 0, 6, true},
		{"anidado", `{"a":{"b":2}}`, 0, 12, true},
		{"llaves en string", `{"a":"{b}"}`, 0, 10, true},
		{"escape de comilla", `{"a":"\"}x"}`, 0, 11, true},
		{"objeto incompleto", `{"a":1`, 0, 0, false},
		{"desde no cero", `xx {"a":1} yy`, 3, 9, true},
		{"desde no cero incompleto", `xx {"a":1`, 3, 0, false},
	}
	for _, caso := range casos {
		fin, ok := cierreJSON(caso.salida, caso.desde)
		if ok != caso.cerrado || (ok && fin != caso.espera) {
			t.Errorf("%s: cierreJSON(%q) = (%d, %v), esperado (%d, %v)",
				caso.nombre, caso.salida, fin, ok, caso.espera, caso.cerrado)
		}
	}
}

// TestOverviewSinFabrica: sin fábrica el overview falla con el centinela
// ErrSinFabrica, comparable con errors.Is desde el caller.
func TestOverviewSinFabrica(t *testing.T) {
	_, err := overviewDeRama(OpcionesRama{}, "feature", nil)
	if !errors.Is(err, ErrSinFabrica) {
		t.Errorf("overviewDeRama sin fábrica = %v, esperado errors.Is ErrSinFabrica", err)
	}
}

// TestConstruirPromptOverview: el prompt muestra los commits de la rama.
func TestConstruirPromptOverview(t *testing.T) {
	fichas := []Ficha{
		{SHA: "a1b2c3d4e5f6", Message: "feat(a): uno"},
		{SHA: "1234567890ab", Message: "fix(b): dos"},
	}
	prompt := ConstruirPromptOverview("feature", fichas)
	for _, parte := range []string{"feature", "a1b2c3d", "feat(a): uno", "fix(b): dos", "coherente"} {
		if !strings.Contains(prompt, parte) {
			t.Errorf("el prompt no menciona %q:\n%s", parte, prompt)
		}
	}
}
