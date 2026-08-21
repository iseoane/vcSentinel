package review

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
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

func (a *auditorStub) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

// salidaAuditOK es un JSONL de auditoría válido para cualquier dimensión
// (ParsearDimensionResult acepta la primera línea con dimensión conocida).
const salidaAuditOK = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"ok\"}\nEND_REVIEW\n"

func fabricaStub(a *auditorStub) FabricaAuditor {
	return func(_ ReviewBundle, dimension string) (AuditorAgente, string, error) {
		return a, "stub", nil
	}
}

type auditorRutasStub struct {
	auditorStub
	rutas [][]string
}

func (a *auditorRutasStub) EjecutarRevision(prompt, _ string, rutas []string) (string, error) {
	a.rutas = append(a.rutas, append([]string(nil), rutas...))
	return a.EjecutarPrompt(prompt)
}

// fakeStoreBlobs es un StoreBlobs de prueba que no depende de un
// store.Store real: permite fijar los SHAs registrados por blob a mano
// (para forzar mezclas de commits) y forzar el error de RegistrarBlobsCommit
// sin tocar disco.
type fakeStoreBlobs struct {
	shasPorBlob      map[string][]string // blob -> SHAs registrados (SHAsDeBlob)
	erroRegistrar    error
	blobsRegistrados map[string]map[string]string // sha -> blobs, para inspección
}

func (f *fakeStoreBlobs) YaRevisado(blob string) (bool, []Hallazgo, error) {
	return len(f.shasPorBlob[blob]) > 0, nil, nil
}

func (f *fakeStoreBlobs) SHAsDeBlob(blob string) ([]string, error) {
	return f.shasPorBlob[blob], nil
}

func (f *fakeStoreBlobs) RegistrarBlobsCommit(sha string, blobs map[string]string) error {
	if f.erroRegistrar != nil {
		return f.erroRegistrar
	}
	if f.blobsRegistrados == nil {
		f.blobsRegistrados = make(map[string]map[string]string)
	}
	f.blobsRegistrados[sha] = blobs
	return nil
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

func TestHallazgosDeterministasParaCommitOnlyAppliesToExplicitSHA(t *testing.T) {
	deterministas := []Hallazgo{{Source: SourceValidation}}

	if got := hallazgosDeterministasParaCommit("head-sha", "head-sha", deterministas); len(got) != 1 {
		t.Errorf("commit == shaValidado: hallazgos = %#v, expected them to apply", got)
	}
	if got := hallazgosDeterministasParaCommit("older-sha", "head-sha", deterministas); got != nil {
		t.Errorf("commit != shaValidado: hallazgos = %#v, expected nil", got)
	}
	if got := hallazgosDeterministasParaCommit("head-sha", "", deterministas); got != nil {
		t.Errorf("shaValidado vacío: hallazgos = %#v, expected nil (never infer by position)", got)
	}
}

func TestAuditarCommitRamaPassesImmutableCommitPathsToRestrictedReviewer(t *testing.T) {
	gitDir := prepararRepoRama(t)
	sha := commitEnRama(t, "committed.go", "package committed\n")
	if err := os.WriteFile("uncommitted.go", []byte("package uncommitted\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ledger := NuevoLedger(gitDir)
	stub := &auditorRutasStub{auditorStub: auditorStub{auditSalida: salidaAuditOK}}

	err := auditarCommitRama(ledger, sha, OpcionesRama{
		Fabrica: func(_ ReviewBundle, _ string) (AuditorAgente, string, error) {
			return stub, "stub", nil
		},
		Parallel: 1,
	})
	if err != nil {
		t.Fatalf("auditarCommitRama() error = %v", err)
	}
	if len(stub.rutas) == 0 {
		t.Fatal("restricted reviewer received no planned paths")
	}
	for _, rutas := range stub.rutas {
		if expected := []string{"committed.go"}; !reflect.DeepEqual(rutas, expected) {
			t.Errorf("reviewer paths = %v, expected immutable commit paths %v", rutas, expected)
		}
	}
}

// TestAnalizarRamaAvisaOnCommitPorCadaPendienteEnOrden cubre la deuda de
// trazabilidad documentada al cerrar F1: sin OnCommit, el progreso de
// OnDimension no dice a qué commit pertenece, porque AnalizarRama audita
// varios commits en la misma pasada.
func TestAnalizarRamaAvisaOnCommitPorCadaPendienteEnOrden(t *testing.T) {
	gitDir := prepararRepoRama(t)
	sha1 := commitEnRama(t, "feat1.txt", "1\n2\n")
	sha2 := commitEnRama(t, "feat2.txt", "3\n4\n")
	ledger := NuevoLedger(gitDir)
	stub := &auditorStub{auditSalida: salidaAuditOK}

	type aviso struct {
		idx, total int
		sha        string
	}
	var avisos []aviso

	_, err := AnalizarRama(ledger, OpcionesRama{
		Fabrica:  fabricaStub(stub),
		Parallel: 1,
		OnCommit: func(idx, total int, sha string) {
			avisos = append(avisos, aviso{idx, total, sha})
		},
	})
	if err != nil {
		t.Fatalf("AnalizarRama falló: %v", err)
	}

	esperados := []aviso{{0, 2, sha1}, {1, 2, sha2}}
	if len(avisos) != len(esperados) {
		t.Fatalf("OnCommit se llamó %d veces, esperado %d: %+v", len(avisos), len(esperados), avisos)
	}
	for i, e := range esperados {
		if avisos[i] != e {
			t.Errorf("aviso %d = %+v, esperado %+v", i, avisos[i], e)
		}
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

// TestCommitCubiertoPorBlobsMezclaDeCommitsNoCubre: un commit cuyos archivos
// vienen de una MEZCLA de commits previos distintos (simula un squash) no
// debe considerarse cubierto, aunque cada blob individual sí tenga ALGÚN
// SHA que lo registró. Antes del fix, commitCubiertoPorBlobs consultaba
// YaRevisado por blob (solo "algún SHA cubre este blob") y esta mezcla se
// colaba como si fuera un rebase seguro; ahora exige que la intersección de
// SHAs entre todos los blobs no esté vacía.
func TestCommitCubiertoPorBlobsMezclaDeCommitsNoCubre(t *testing.T) {
	prepararRepoRama(t)
	// Un único commit con dos archivos simula el resultado de un squash: sus
	// dos blobs existen, pero se registran (a mano, vía el fake) bajo SHAs
	// históricos DISTINTOS, como si cada archivo viniera de un commit previo
	// diferente.
	if err := os.WriteFile("mix1.txt", []byte("contenido 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("mix2.txt", []byte("contenido 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutar(t, "add", "mix1.txt", "mix2.txt")
	gitEjecutar(t, "commit", "-m", "feat(mix): simula un squash de dos commits")
	shaCombinado := strings.TrimSpace(gitSalida(t, "rev-parse", "HEAD"))

	archivos, err := git.ArchivosDeCommit(shaCombinado)
	if err != nil {
		t.Fatalf("ArchivosDeCommit: %v", err)
	}
	blobs, err := blobsDeArchivos(shaCombinado, archivos)
	if err != nil {
		t.Fatalf("blobsDeArchivos: %v", err)
	}
	if len(blobs) != 2 {
		t.Fatalf("blobs = %v, esperado 2 archivos (mix1.txt + mix2.txt)", blobs)
	}

	fake := &fakeStoreBlobs{shasPorBlob: map[string][]string{}}
	i := 0
	for _, blob := range blobs {
		fake.shasPorBlob[blob] = []string{fmt.Sprintf("sha-historico-%d", i)}
		i++
	}

	cubierto, shaOrigen, err := commitCubiertoPorBlobs(fake, shaCombinado)
	if err != nil {
		t.Fatalf("commitCubiertoPorBlobs: %v", err)
	}
	if cubierto {
		t.Errorf("commitCubiertoPorBlobs = (true, %q), esperado false: los archivos vienen de SHAs históricos distintos, ninguno cubre el conjunto completo", shaOrigen)
	}
}

// TestAuditarCommitRamaContinuaSiRegistrarBlobsFalla: si RegistrarBlobsCommit
// falla DESPUÉS de que la auditoría y el guardado en el ledger ya tuvieron
// éxito, auditarCommitRama no debe propagar el error — abortaría
// AnalizarRama y perdería un resultado real ya persistido por una simple
// optimización de reutilización futura.
func TestAuditarCommitRamaContinuaSiRegistrarBlobsFalla(t *testing.T) {
	gitDir := prepararRepoRama(t)
	sha := commitEnRama(t, "feat.txt", "1\n2\n3\n")
	ledger := NuevoLedger(gitDir)
	stub := &auditorStub{auditSalida: salidaAuditOK}
	fake := &fakeStoreBlobs{erroRegistrar: errors.New("fallo simulado de registro de blobs")}

	err := auditarCommitRama(ledger, sha, OpcionesRama{
		Fabrica: fabricaStub(stub), Parallel: 1, Store: fake,
	})
	if err != nil {
		t.Fatalf("auditarCommitRama debería continuar aunque RegistrarBlobsCommit falle, devolvió: %v", err)
	}

	ficha, err := ledger.LeerFicha(sha)
	if err != nil {
		t.Fatalf("LeerFicha: %v", err)
	}
	if ficha == nil || len(ficha.Revisions) != 1 {
		t.Errorf("la ficha debería haberse guardado igual en el ledger: %+v", ficha)
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

func TestAuditarCommitRamaRoutesThroughPerCommitTransport(t *testing.T) {
	gitDir := prepararRepoRama(t)
	sha := commitEnRama(t, "transport.go", "package transport\n")
	ledger := NuevoLedger(gitDir)
	stub := &auditorStub{auditSalida: salidaAuditOK}

	var factorySHA string
	var factoryPaths []string
	transportCalled := false
	err := auditarCommitRama(ledger, sha, OpcionesRama{
		Fabrica: func(_ ReviewBundle, _ string) (AuditorAgente, string, error) {
			return stub, "stub", nil
		},
		Parallel: 1,
		ReviewTransportFactory: func(commitSHA string, paths []string) ReviewTransport {
			factorySHA = commitSHA
			factoryPaths = paths
			return func(_, _, _ string, _ AuditorAgente) (string, error) {
				transportCalled = true
				return salidaAuditOK, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("auditarCommitRama() error = %v", err)
	}
	if !transportCalled || factorySHA != sha {
		t.Fatalf("per-commit transport invoked=%v boundSHA=%q, want routed for %q", transportCalled, factorySHA, sha)
	}
	if len(factoryPaths) != 1 || factoryPaths[0] != "transport.go" {
		t.Fatalf("factory paths = %v, want the audited commit's immutable paths", factoryPaths)
	}
}
