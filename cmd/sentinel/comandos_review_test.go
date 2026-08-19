package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// capturarStdout redirige os.Stdout durante f y devuelve lo que escribió.
// La restauración va en t.Cleanup (no solo tras un f() que retorna
// normalmente): un t.Fatalf/panic dentro de f dejaría, si no, os.Stdout
// apuntando permanentemente a un pipe ya cerrado para el resto del binario
// de test.
func capturarStdout(t *testing.T, f func()) string {
	t.Helper()
	original := os.Stdout
	lector, escritor, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = escritor
	t.Cleanup(func() { os.Stdout = original })
	f()
	escritor.Close()
	salida, err := io.ReadAll(lector)
	if err != nil {
		t.Fatalf("leer el pipe: %v", err)
	}
	return string(salida)
}

// TestImprimirPreguntasPendientesJSON cubre el contrato machine-readable del
// --json de T7.6: una pregunta con File emite "file", una sin File lo omite
// (json:"file,omitempty"), y el objeto expone exactamente sha/pending_questions.
func TestImprimirPreguntasPendientesJSON(t *testing.T) {
	pendientes := []review.AgentQuestion{
		{ID: "q1", Text: "¿usa camelCase?", File: "a.go"},
		{ID: "q2", Text: "sin archivo asociado"},
	}
	salida := capturarStdout(t, func() { imprimirPreguntasPendientesJSON("abc123", pendientes) })
	esperada := `{"sha":"abc123","pending_questions":[{"id":"q1","text":"¿usa camelCase?","file":"a.go"},{"id":"q2","text":"sin archivo asociado"}]}` + "\n"
	if salida != esperada {
		t.Errorf("salida = %q, esperada %q", salida, esperada)
	}
}

// TestEjecutarReview_ClaveDesconocidaEnYml_Exit1ConLinea cubre el Fix 1 (F1,
// hallazgo del orquestador): ejecutarReview usaba CargarConfiguracionLocal
// (sin error). Con un yml roto, antes seguía adelante en silencio hasta
// fallar más tarde con un error de git ajeno al problema real (el worktree de
// este test no es un repo); con CargarConfiguracionLocalEstricta debe cortar
// aquí mismo con exit 1 y el error del yml visible.
func TestEjecutarReview_ClaveDesconocidaEnYml_Exit1ConLinea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	escribirYmlConClaveDesconocida(t, worktree)

	salida, exit := ejecutarComoSubproceso(t, "ejecutarReview", worktree, home)

	if exit != 1 {
		t.Errorf("exit esperado 1, obtuve %d (salida: %q)", exit, salida)
	}
	if !strings.Contains(salida, "line") {
		t.Errorf("la salida debe incluir la línea del error del yml, obtuve: %q", salida)
	}
}

// TestParsearRespuestasAuditoria cubre T7.6: extraer respuestas dirigidas
// "id=texto" de --answer sin romper el uso existente (prosa libre, incluso
// con comas literales, debe reconstruirse byte a byte cuando no hay ningún
// token con "=").
func TestParsearRespuestasAuditoria(t *testing.T) {
	pruebas := []struct {
		nombre      string
		respuesta   string
		porIDEspera map[string]string
		restoEspera string
	}{
		{"vacío", "", map[string]string{}, ""},
		{"prosa libre sin coma (uso previo a T7.6)", "no aplica aqui", map[string]string{}, "no aplica aqui"},
		{"prosa libre con coma se reconstruye igual", "some prose, with a comma", map[string]string{}, "some prose, with a comma"},
		{"una respuesta dirigida sola", "q1=usa camelCase", map[string]string{"q1": "usa camelCase"}, ""},
		{"mezcla de dirigidas y prosa, en orden", "id1=texto uno,algo de prosa,id2=texto dos",
			map[string]string{"id1": "texto uno", "id2": "texto dos"}, "algo de prosa"},
		{"espacios se recortan en ambos lados", " q1 = texto con espacios ",
			map[string]string{"q1": "texto con espacios"}, ""},
		{"= sin id antes no es una respuesta dirigida", "=algo", map[string]string{}, "=algo"},
	}
	for _, prueba := range pruebas {
		t.Run(prueba.nombre, func(t *testing.T) {
			porID, resto := parsearRespuestasAuditoria(prueba.respuesta)
			if len(porID) != len(prueba.porIDEspera) {
				t.Fatalf("porID = %+v, esperado %+v", porID, prueba.porIDEspera)
			}
			for id, texto := range prueba.porIDEspera {
				if porID[id] != texto {
					t.Errorf("porID[%q] = %q, esperado %q", id, porID[id], texto)
				}
			}
			if resto != prueba.restoEspera {
				t.Errorf("resto = %q, esperado %q", resto, prueba.restoEspera)
			}
		})
	}
}

func TestCodigoSalidaVeredicto(t *testing.T) {
	pruebas := []struct {
		veredicto string
		esperado  int
	}{
		{review.VerdictOK, 0},
		{review.VerdictWarn, 0},
		{review.VerdictBlock, 1},
		{review.VerdictQuestion, 3},
		{review.VerdictUnavailable, 4},
	}
	for _, prueba := range pruebas {
		if got := codigoSalidaVeredicto(prueba.veredicto); got != prueba.esperado {
			t.Errorf("codigoSalidaVeredicto(%q) = %d, esperado %d", prueba.veredicto, got, prueba.esperado)
		}
	}
}

func TestDimsResultadosParaFicha(t *testing.T) {
	rd := []review.ResultadoDimension{
		{Dim: review.DimLogic, Resultado: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK}},
		{Dim: review.DimSecurity, Error: errors.New("fallo")},
		{Dim: review.DimSpec, Resultado: &review.DimensionResult{Dim: review.DimSpec, Verdict: review.VerdictBlock}},
	}
	resultados := review.DimsResultadosParaFicha(rd)
	if len(resultados) != 2 {
		t.Fatalf("DimsResultadosParaFicha = %d resultados, esperado 2 (filtra nil)", len(resultados))
	}
	if resultados[0].Verdict != review.VerdictOK || resultados[1].Verdict != review.VerdictBlock {
		t.Errorf("verdicts = %q/%q, esperado ok/block", resultados[0].Verdict, resultados[1].Verdict)
	}
}

func TestTieneHallazgosCriticos(t *testing.T) {
	construir := func(severidad string) review.ResultadoAuditoria {
		return review.ResultadoAuditoria{
			Dims: []review.ResultadoDimension{{
				Dim: review.DimSecurity,
				Resultado: &review.DimensionResult{
					Dim:     review.DimSecurity,
					Verdict: review.VerdictWarn,
					Findings: []review.ReviewFinding{{
						Severity: severidad,
						File:     "a.go",
					}},
				},
			}},
		}
	}

	if !tieneHallazgosCriticos(construir(review.SevCritical)) {
		t.Error("debería detectar CRITICAL")
	}
	if tieneHallazgosCriticos(construir(review.SevWarning)) {
		t.Error("WARNING no debería contar como crítico")
	}
	if tieneHallazgosCriticos(review.ResultadoAuditoria{}) {
		t.Error("sin hallazgos no debería haber críticos")
	}
}

func TestRevisionCorrigeBlockPrevio(t *testing.T) {
	dir := t.TempDir()
	ledger := review.NuevoLedger(dir)

	// Sin ficha previa: no corrige.
	if review.RevisionCorrigeBlockPrevio(ledger, "abc123", review.VerdictOK) {
		t.Error("sin ficha previa no debería marcar corrección")
	}

	// Previa en block y nueva sin block: corrige.
	rev := review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock}
	if err := ledger.GuardarRevision("abc123", "msg", "backend", "m", rev); err != nil {
		t.Fatal(err)
	}
	if !review.RevisionCorrigeBlockPrevio(ledger, "abc123", review.VerdictOK) {
		t.Error("previa en block y nueva ok debería marcar corrección")
	}
	if review.RevisionCorrigeBlockPrevio(ledger, "abc123", review.VerdictBlock) {
		t.Error("nueva en block no corrige nada")
	}
}

func TestRegistrarCorreccionesMarcaFicha(t *testing.T) {
	dir := t.TempDir()
	ledger := review.NuevoLedger(dir)

	// Ficha previa en block con hallazgo en internal/a.go.
	rev := review.Revision{
		At: time.Now().UTC(), Result: review.VerdictBlock,
		Dims: []review.DimensionResult{{
			Dim: review.DimLogic, Verdict: review.VerdictBlock,
			Findings: []review.ReviewFinding{{
				Severity: review.SevCritical, File: "internal/a.go", Line: 10,
				Description: "bug real",
			}},
		}},
	}
	if err := ledger.GuardarRevision("aaa111", "feat(x): con bug", "backend", "m", rev); err != nil {
		t.Fatal(err)
	}

	// Un fix que toca internal/a.go sale sin críticos: debe marcar la ficha.
	registrarCorrecciones(ledger, dir, "bbb222", []string{"internal/a.go"}, "fix(x): arregla", 0, "worktree")
	ficha, err := ledger.LeerFicha("aaa111")
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "bbb222" {
		t.Errorf("FixedIn = %q, esperado bbb222", ficha.FixedIn)
	}

	// Un fix que NO toca los archivos del hallazgo no marca nada.
	if err := ledger.GuardarRevision("ccc333", "feat(y): otro", "backend", "m",
		review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock,
			Dims: []review.DimensionResult{{Dim: review.DimLogic, Verdict: review.VerdictBlock,
				Findings: []review.ReviewFinding{{Severity: review.SevCritical, File: "internal/b.go", Line: 1, Description: "otro"}}}}},
	); err != nil {
		t.Fatal(err)
	}
	registrarCorrecciones(ledger, dir, "ddd444", []string{"internal/a.go"}, "fix(y): arregla", 0, "worktree")
	ficha, err = ledger.LeerFicha("ccc333")
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "" {
		t.Errorf("FixedIn = %q, esperado vacío (el fix no toca b.go)", ficha.FixedIn)
	}

	// Un commit que sale en block nunca registra correcciones.
	registrarCorrecciones(ledger, dir, "eee555", []string{"internal/a.go"}, "fix(z): intento", 1, "worktree")
	ficha, err = ledger.LeerFicha("ccc333")
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "" {
		t.Errorf("FixedIn = %q, esperado vacío (el fix salió en block)", ficha.FixedIn)
	}
}

// TestFiltrarRespuestasPorIDsReales cubre el WARNING de T7.6: un token
// "id=texto" de --answer cuyo "id" no coincide con ninguna pregunta real de
// este pase no es una respuesta dirigida, es prosa libre que contiene un "="
// literal (p. ej. --answer "the flag --gate=true is set"). Debe reconstruirse
// byte a byte dentro de resto, nunca colarse en el mapa validado.
func TestFiltrarRespuestasPorIDsReales(t *testing.T) {
	preguntasReales := []review.AgentQuestion{{ID: "q1", Text: "¿procede?", File: "a.go"}}

	pruebas := []struct {
		nombre         string
		porID          map[string]string
		resto          string
		validadoQuiero map[string]string
		restoQuiero    string
	}{
		{
			nombre:         "id real se mantiene validado",
			porID:          map[string]string{"q1": "sí"},
			resto:          "",
			validadoQuiero: map[string]string{"q1": "sí"},
			restoQuiero:    "",
		},
		{
			// El caso literal reportado: "--answer \"the flag --gate=true is set\""
			// hoy se corta en porID["the flag --gate"] = "true is set" porque
			// contiene un "=" literal, pero "the flag --gate" no es el ID de
			// ninguna pregunta real de este pase.
			nombre:         "id inexistente se reconstruye como prosa (the flag --gate=true is set)",
			porID:          map[string]string{"the flag --gate": "true is set"},
			resto:          "",
			validadoQuiero: map[string]string{},
			restoQuiero:    "the flag --gate=true is set",
		},
		{
			nombre: "mezcla: id real se valida, id ajeno vuelve a prosa junto con el resto existente",
			porID: map[string]string{
				"q1":              "sí",
				"the flag --gate": "true is set",
			},
			resto:          "algo de prosa ya existente",
			validadoQuiero: map[string]string{"q1": "sí"},
			restoQuiero:    "the flag --gate=true is set,algo de prosa ya existente",
		},
		{
			nombre:         "sin porID no cambia resto",
			porID:          map[string]string{},
			resto:          "solo prosa",
			validadoQuiero: map[string]string{},
			restoQuiero:    "solo prosa",
		},
	}

	for _, prueba := range pruebas {
		t.Run(prueba.nombre, func(t *testing.T) {
			validado, resto := filtrarRespuestasPorIDsReales(prueba.porID, prueba.resto, preguntasReales)
			if len(validado) != len(prueba.validadoQuiero) {
				t.Fatalf("validado = %+v, esperado %+v", validado, prueba.validadoQuiero)
			}
			for id, texto := range prueba.validadoQuiero {
				if validado[id] != texto {
					t.Errorf("validado[%q] = %q, esperado %q", id, validado[id], texto)
				}
			}
			if resto != prueba.restoQuiero {
				t.Errorf("resto = %q, esperado %q", resto, prueba.restoQuiero)
			}
		})
	}
}

// gitEjecutarPruebaReview ejecuta git y falla el test si el comando no sale
// con éxito, con la salida combinada en el mensaje de error.
func gitEjecutarPruebaReview(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v falló: %v\n%s", args, err, out)
	}
}

// repoDePruebaConUnCommit crea un repositorio git real con un solo commit que
// añade nombreArchivo con contenido real, y posiciona el proceso de test en
// su directorio: git.BlobDeArchivoEnCommit resuelve el blob vía
// "git rev-parse <sha>:<ruta>" sobre el cwd del proceso (no acepta -C), igual
// que TestAnalizarRamaSobreviveRebaseViaBlob en internal/review/rebase_test.go
// (la convención ya establecida en este codebase para este tipo de fixture).
// Devuelve el directorio del repo (= worktree = git-common-dir, sin
// worktrees enlazados) y el SHA del commit.
func repoDePruebaConUnCommit(t *testing.T, nombreArchivo, contenido string) (repo, sha string) {
	t.Helper()
	repo = t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		gitEjecutarPruebaReview(t, args...)
	}
	if err := os.WriteFile(nombreArchivo, []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarPruebaReview(t, "add", nombreArchivo)
	gitEjecutarPruebaReview(t, "commit", "-m", "feat(test): contenido de prueba")

	salida, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return repo, strings.TrimSpace(string(salida))
}

// agenteFakeSecuencialReview devuelve, en orden, las respuestas JSONL fijas
// de respuestas, y repite la última indefinidamente una vez agotada la
// lista (nunca cae a un "ok" por defecto): auditarConAgente
// (internal/review/engine.go) ya hace su propia ronda extra interna cuando
// la primera respuesta es "question" y opts.Respuestas no está vacío, así
// que un agente que sigue preguntando de verdad (el caso deadlock de T7.6)
// necesita devolver "question" en TODAS las llamadas, no solo en la primera,
// para que ese round-trip interno no lo disfrace de "ok". Mismo patrón que
// agenteFake en internal/review/engine_test.go y stubAuditorRebase en
// internal/review/rebase_test.go — duplicado aquí porque ninguno de esos
// tipos está exportado desde internal/review.
type agenteFakeSecuencialReview struct {
	respuestas []string
	llamadas   int
}

func (a *agenteFakeSecuencialReview) EjecutarPrompt(prompt string) (string, error) {
	defer func() { a.llamadas++ }()
	if len(a.respuestas) == 0 {
		return `{"dim":"logic","verdict":"ok"}`, nil
	}
	idx := a.llamadas
	if idx >= len(a.respuestas) {
		idx = len(a.respuestas) - 1
	}
	return a.respuestas[idx], nil
}

// EjecutarRevision implementa auditorConHerramientasRestringidas (interfaz
// interna de internal/review no exportada): auditarConAgente exige ese tipo
// y, si el agente inyectado no lo implementa, el resultado es
// VerdictUnavailable ("restricted reviewer capability is required") en vez
// de parsear la respuesta fija de la prueba.
func (a *agenteFakeSecuencialReview) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func fabricaFakeSecuencialReview(respuestas []string) review.FabricaAuditor {
	fake := &agenteFakeSecuencialReview{respuestas: respuestas}
	return func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return fake, "test", nil
	}
}

func bundlesDePruebaReview() []review.ReviewBundle {
	return []review.ReviewBundle{{Name: "test", Dimensions: []string{review.DimLogic}, Priority: review.PriorityRequired, Cost: 1}}
}

// TestAplicarPreguntasPendientes_RespuestaYaRegistrada_DesbloqueaSinPreguntarDeNuevo
// es el criterio de aceptación literal de T7.6: con una respuesta ya
// registrada para (blob, questionID), una segunda auditoría del mismo blob
// no incluye esa pregunta en el --json de pendientes y aplica la respuesta
// registrada sin bloquear la ejecución. Cubre también el CRITICAL #2 (el
// veredicto final debe reflejar el estado deduplicado, no el "question" en
// bruto que trajo resultado antes de deduplicar).
func TestAplicarPreguntasPendientes_RespuestaYaRegistrada_DesbloqueaSinPreguntarDeNuevo(t *testing.T) {
	repo, sha := repoDePruebaConUnCommit(t, "a.go", "package a\n")
	blob, err := git.BlobDeArchivoEnCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit: %v", err)
	}

	// El store debe vivir en el mismo git-common-dir que leerá
	// aplicarPreguntasPendientes (ObtenerGitCommonDir(worktree)), no en la
	// raíz del worktree: en un repo sin worktrees enlazados es <repo>/.git,
	// pero eso es un detalle de implementación de git que no hay que asumir
	// a mano.
	gitCommonDir, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir: %v", err)
	}
	st := store.NuevoStore(gitCommonDir)
	if err := st.RegistrarRespuesta(blob, "q1", "yes", "test-actor"); err != nil {
		t.Fatalf("RegistrarRespuesta: %v", err)
	}

	resultado := review.ResultadoAuditoria{
		Veredicto: review.VerdictQuestion,
		Preguntas: []review.AgentQuestion{{ID: "q1", Text: "¿procede el cambio?", File: "a.go"}},
	}
	// La ronda de reintento debe resolver limpio: el agente ya no vuelve a
	// preguntar tras recibir la respuesta.
	fabrica := fabricaFakeSecuencialReview([]string{`{"dim":"logic","verdict":"ok"}`})
	opciones := review.OpcionesAuditoria{SHA: sha, Bundles: bundlesDePruebaReview()}

	resultadoFinal, pendientes := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)

	if len(pendientes) != 0 {
		t.Fatalf("pendientes = %+v, esperado vacío", pendientes)
	}
	if resultadoFinal.Veredicto != review.VerdictOK {
		t.Fatalf("Veredicto = %q, esperado %q (la respuesta registrada desbloquea sin preguntar de nuevo)",
			resultadoFinal.Veredicto, review.VerdictOK)
	}
	if len(resultadoFinal.Preguntas) != 0 {
		t.Fatalf("resultadoFinal.Preguntas = %+v, esperado vacío (debe reflejar el estado deduplicado)", resultadoFinal.Preguntas)
	}
}

// TestAplicarPreguntasPendientes_AgenteVuelveAPreguntar_NoSeQuedaEnDeadlock
// cubre el CRITICAL #2, caso deadlock: el agente puede legítimamente volver
// a emitir verdict:"question" con la MISMA pregunta pese a haber recibido ya
// la aclaración en la ronda de reintento (un modelo no está garantizado a
// dejar de preguntar solo porque recibió la respuesta). Sin la
// reconciliación de veredicto, esto bloquearía para siempre (exit 3 en cada
// ejecución futura sobre el mismo contenido) — aquí debe rebajarse a warn.
func TestAplicarPreguntasPendientes_AgenteVuelveAPreguntar_NoSeQuedaEnDeadlock(t *testing.T) {
	repo, sha := repoDePruebaConUnCommit(t, "a.go", "package a\n")
	blob, err := git.BlobDeArchivoEnCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit: %v", err)
	}

	// El store debe vivir en el mismo git-common-dir que leerá
	// aplicarPreguntasPendientes (ObtenerGitCommonDir(worktree)), no en la
	// raíz del worktree: en un repo sin worktrees enlazados es <repo>/.git,
	// pero eso es un detalle de implementación de git que no hay que asumir
	// a mano.
	gitCommonDir, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir: %v", err)
	}
	st := store.NuevoStore(gitCommonDir)
	if err := st.RegistrarRespuesta(blob, "q1", "yes", "test-actor"); err != nil {
		t.Fatalf("RegistrarRespuesta: %v", err)
	}

	resultado := review.ResultadoAuditoria{
		Veredicto: review.VerdictQuestion,
		Preguntas: []review.AgentQuestion{{ID: "q1", Text: "¿procede el cambio?", File: "a.go"}},
	}
	// La ronda de reintento simula un agente que insiste en la misma
	// pregunta pese a haber recibido la respuesta ya registrada.
	fabrica := fabricaFakeSecuencialReview([]string{
		`{"dim":"logic","verdict":"question","questions":[{"id":"q1","text":"¿procede el cambio?","file":"a.go"}]}`,
	})
	opciones := review.OpcionesAuditoria{SHA: sha, Bundles: bundlesDePruebaReview()}

	resultadoFinal, pendientes := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)

	if len(pendientes) != 0 {
		t.Fatalf("pendientes = %+v, esperado vacío (ya hay respuesta registrada para q1)", pendientes)
	}
	if resultadoFinal.Veredicto != review.VerdictWarn {
		t.Fatalf("Veredicto = %q, esperado %q (nunca %q: eso sería el deadlock permanente)",
			resultadoFinal.Veredicto, review.VerdictWarn, review.VerdictQuestion)
	}
}
