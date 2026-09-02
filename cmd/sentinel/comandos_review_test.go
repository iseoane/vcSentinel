package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
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
	// Real repository with real commits. The fixture used fabricated SHAs and a
	// directory that is not a repository, which stopped being enough when
	// attribution started requiring the audited commit to be an ancestor of the
	// fix (FU-17). The rules it covers — the fix must touch a file named in the
	// findings, and a fix that itself blocks attributes nothing — are unchanged.
	repo, commit := repoConCommitsReales(t)
	gitDir, err := git.ObtenerGitDirDe(repo)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NuevoLedger(gitDir)

	bloqueado := commit("internal/a.go", "package a\n", "feat(x): con bug")

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
	if err := ledger.GuardarRevision(bloqueado, "feat(x): con bug", "backend", "m", rev); err != nil {
		t.Fatal(err)
	}

	// Un fix que toca internal/a.go sale sin críticos: debe marcar la ficha.
	arreglo := commit("internal/a.go", "package a // fixed\n", "fix(x): arregla")
	registrarCorrecciones(ledger, gitDir, arreglo, []string{"internal/a.go"}, "fix(x): arregla", 0, repo)
	ficha, err := ledger.LeerFicha(bloqueado)
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != arreglo {
		t.Errorf("FixedIn = %q, esperado %q", ficha.FixedIn, arreglo)
	}

	// Un fix que NO toca los archivos del hallazgo no marca nada.
	otro := commit("internal/b.go", "package b\n", "feat(y): otro")
	if err := ledger.GuardarRevision(otro, "feat(y): otro", "backend", "m",
		review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock,
			Dims: []review.DimensionResult{{Dim: review.DimLogic, Verdict: review.VerdictBlock,
				Findings: []review.ReviewFinding{{Severity: review.SevCritical, File: "internal/b.go", Line: 1, Description: "otro"}}}}},
	); err != nil {
		t.Fatal(err)
	}
	ajeno := commit("internal/a.go", "package a // otra cosa\n", "fix(y): arregla")
	registrarCorrecciones(ledger, gitDir, ajeno, []string{"internal/a.go"}, "fix(y): arregla", 0, repo)
	ficha, err = ledger.LeerFicha(otro)
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "" {
		t.Errorf("FixedIn = %q, esperado vacío (el fix no toca b.go)", ficha.FixedIn)
	}

	// Un commit que sale en block nunca registra correcciones.
	enBlock := commit("internal/b.go", "package b // intento\n", "fix(z): intento")
	registrarCorrecciones(ledger, gitDir, enBlock, []string{"internal/b.go"}, "fix(z): intento", 1, repo)
	ficha, err = ledger.LeerFicha(otro)
	if err != nil {
		t.Fatal(err)
	}
	if ficha.FixedIn != "" {
		t.Errorf("FixedIn = %q, esperado vacío (el fix salió en block)", ficha.FixedIn)
	}
}

// repoConCommitsReales devuelve un repositorio Git y una función que escribe un
// archivo, lo commitea y devuelve su SHA. Existe porque la atribución de
// correcciones ya no se puede probar con SHAs inventados: exige ancestría real
// entre el commit auditado y el fix.
func repoConCommitsReales(t *testing.T) (string, func(ruta, contenido, mensaje string) string) {
	t.Helper()
	repo := t.TempDir()
	correr := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + repo,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	correr("init", "-q", "-b", "main")
	return repo, func(ruta, contenido, mensaje string) string {
		t.Helper()
		completa := filepath.Join(repo, ruta)
		if err := os.MkdirAll(filepath.Dir(completa), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(completa, []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
		correr("add", ruta)
		correr("commit", "-qm", mensaje)
		return correr("rev-parse", "HEAD")
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

// agenteFakeSecuencialReview returns its fixed JSONL answers in order, then
// repeats the final one indefinitely. auditWithAgent (internal/review/engine.go)
// makes its own extra round when the first answer is "question" and
// opts.Respuestas is not empty. A genuinely repeating agent (the T7.6
// deadlock case) must therefore return "question" on every call so that
// internal round trip cannot disguise it as "ok". This duplicates the
// pattern from agenteFake in internal/review/engine_test.go and
// rebaseReviewerStub in internal/review/rebase_test.go because neither type
// is exported from internal/review.
type agenteFakeSecuencialReview struct {
	respuestas []string
	llamadas   int
	// prompts records each received prompt in order. It proves that
	// Respuestas (opts.Respuestas) reaches the agent in auditWithAgent's
	// second internal sub-round only after the first answer is "question",
	// not merely that the final verdict is expected.
	prompts []string
}

func (a *agenteFakeSecuencialReview) EjecutarPrompt(prompt string) (string, error) {
	a.prompts = append(a.prompts, prompt)
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

// EjecutarRevision implements the internal restricted-reviewer interface.
// auditWithAgent requires that type; without it, the injected agent returns
// VerdictUnavailable ("restricted reviewer capability is required") rather
// than parsing the fixture response.
func (a *agenteFakeSecuencialReview) EjecutarRevision(prompt, _ string, _ []string) (string, error) {
	return a.EjecutarPrompt(prompt)
}

func (a *agenteFakeSecuencialReview) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

func fabricaFakeSecuencialReview(respuestas []string) review.FabricaAuditor {
	fabrica, _ := fabricaFakeSecuencialReviewCapturando(respuestas)
	return fabrica
}

// fabricaFakeSecuencialReviewCapturando es fabricaFakeSecuencialReview pero
// devuelve también el *agenteFakeSecuencialReview subyacente, para que el
// test pueda inspeccionar los prompts que realmente recibió (p. ej.
// confirmar qué texto de Respuestas llegó en la segunda sub-ronda interna).
func fabricaFakeSecuencialReviewCapturando(respuestas []string) (review.FabricaAuditor, *agenteFakeSecuencialReview) {
	fake := &agenteFakeSecuencialReview{respuestas: respuestas}
	return func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return fake, "test", nil
	}, fake
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

	resultadoFinal, pendientes, applyErr := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)
	if applyErr != nil {
		t.Fatalf("aplicarPreguntasPendientes: %v", applyErr)
	}

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

	resultadoFinal, pendientes, applyErr := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)
	if applyErr != nil {
		t.Fatalf("aplicarPreguntasPendientes: %v", applyErr)
	}

	if len(pendientes) != 0 {
		t.Fatalf("pendientes = %+v, esperado vacío (ya hay respuesta registrada para q1)", pendientes)
	}
	if resultadoFinal.Veredicto != review.VerdictWarn {
		t.Fatalf("Veredicto = %q, esperado %q (nunca %q: eso sería el deadlock permanente)",
			resultadoFinal.Veredicto, review.VerdictWarn, review.VerdictQuestion)
	}
}

// TestAplicarPreguntasPendientes_ContestadasComparteIDEntreArchivos_NoPierdeNinguna
// es la regresión literal del CRITICAL detectado en la ronda de fix
// anterior: SplitPendingQuestions dejó de colapsar por ID (T7.6 fix #1),
// pero el único llamador seguía volviendo a colapsar el resultado en un
// map[string]string indexado solo por ID antes de construir el prompt de
// reintento — el defecto no cambiaba de comportamiento observable, solo de
// ubicación. Aquí dos preguntas de "dimensiones" distintas comparten el ID
// "q1" pero tienen archivos y respuestas ya registradas distintas: ambas
// deben llegar como líneas separadas al agente, no colapsarse en una sola.
func TestAplicarPreguntasPendientes_ContestadasComparteIDEntreArchivos_NoPierdeNinguna(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		gitEjecutarPruebaReview(t, args...)
	}
	if err := os.WriteFile("a.go", []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.go", []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarPruebaReview(t, "add", "a.go", "b.go")
	gitEjecutarPruebaReview(t, "commit", "-m", "feat(test): dos archivos")
	salida, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(salida))

	blobA, err := git.BlobDeArchivoEnCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit a.go: %v", err)
	}
	blobB, err := git.BlobDeArchivoEnCommit(sha, "b.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit b.go: %v", err)
	}

	gitCommonDir, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir: %v", err)
	}
	st := store.NuevoStore(gitCommonDir)
	if err := st.RegistrarRespuesta(blobA, "q1", "respuesta para a.go", "test-actor"); err != nil {
		t.Fatalf("RegistrarRespuesta a.go: %v", err)
	}
	if err := st.RegistrarRespuesta(blobB, "q1", "respuesta para b.go", "test-actor"); err != nil {
		t.Fatalf("RegistrarRespuesta b.go: %v", err)
	}

	resultado := review.ResultadoAuditoria{
		Veredicto: review.VerdictQuestion,
		Preguntas: []review.AgentQuestion{
			{ID: "q1", Text: "¿procede en a.go?", File: "a.go"},
			{ID: "q1", Text: "¿procede en b.go?", File: "b.go"},
		},
	}
	// The first internal sub-round of auditWithAgent always asks WITHOUT
	// Respuestas. Only a "question" first answer triggers a second sub-round
	// WITH embedded Respuestas. The first item triggers that round; the second
	// is the assertion target, the prompt received in the second call.
	fabrica, fake := fabricaFakeSecuencialReviewCapturando([]string{
		`{"dim":"logic","verdict":"question"}`,
		`{"dim":"logic","verdict":"ok"}`,
	})
	opciones := review.OpcionesAuditoria{
		SHA: sha, Bundles: bundlesDePruebaReview(),
		Respuestas: "q1@b.go=fresh answer for b.go",
	}

	resultadoFinal, pendientes, applyErr := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)
	if applyErr != nil {
		t.Fatalf("aplicarPreguntasPendientes: %v", applyErr)
	}

	if len(pendientes) != 0 {
		t.Fatalf("pendientes = %+v, esperado vacío", pendientes)
	}
	if resultadoFinal.Veredicto != review.VerdictOK {
		t.Fatalf("Veredicto = %q, esperado %q", resultadoFinal.Veredicto, review.VerdictOK)
	}
	promptConRespuestas, ok := promptConClarificaciones(fake.prompts)
	if !ok {
		t.Fatalf("ningún prompt recibido contiene la sección de aclaraciones del usuario: %+v", fake.prompts)
	}
	if strings.Contains(promptConRespuestas, "respuesta para b.go") {
		t.Errorf("the retry prompt kept the stored answer overridden for b.go:\n%s", promptConRespuestas)
	}
	if !strings.Contains(promptConRespuestas, "q1@a.go: respuesta para a.go") {
		t.Errorf("the retry prompt lost the stored answer for a.go:\n%s", promptConRespuestas)
	}
	if !strings.Contains(promptConRespuestas, "q1@b.go: fresh answer for b.go") {
		t.Errorf("the retry prompt does not contain the fresh qualified answer for b.go:\n%s", promptConRespuestas)
	}
	storedLine := strings.Index(promptConRespuestas, "q1@a.go: respuesta para a.go")
	freshLine := strings.Index(promptConRespuestas, "q1@b.go: fresh answer for b.go")
	if storedLine == -1 || freshLine == -1 {
		t.Fatalf("qualified answer lines are missing from the retry prompt:\n%s", promptConRespuestas)
	}
	if storedLine > freshLine {
		t.Errorf("qualified answer lines are not ordered by (ID, File):\n%s", promptConRespuestas)
	}
	if got, ok, err := st.RespuestaRegistrada(blobA, "q1"); err != nil || !ok || got != "respuesta para a.go" {
		t.Errorf("stored answer for a.go = %q, %v, %v; want original stored answer", got, ok, err)
	}
	if got, ok, err := st.RespuestaRegistrada(blobB, "q1"); err != nil || !ok || got != "fresh answer for b.go" {
		t.Errorf("stored answer for b.go = %q, %v, %v; want fresh qualified answer", got, ok, err)
	}
}

// TestAplicarPreguntasPendientes_AmbiguousBareAnswerPropagatesWithoutRetryOrPersistence
// proves that a bare answer cannot select one of several questions sharing an
// ID: the caller returns the deterministic ambiguity error before retrying the
// audit or persisting an answer for either file.
func TestAplicarPreguntasPendientes_AmbiguousBareAnswerPropagatesWithoutRetryOrPersistence(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		gitEjecutarPruebaReview(t, args...)
	}
	if err := os.WriteFile("a.go", []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.go", []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitEjecutarPruebaReview(t, "add", "a.go", "b.go")
	gitEjecutarPruebaReview(t, "commit", "-m", "feat(test): ambiguous question fixtures")
	shaOutput, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaOutput))

	blobA, err := git.BlobDeArchivoEnCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit a.go: %v", err)
	}
	blobB, err := git.BlobDeArchivoEnCommit(sha, "b.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit b.go: %v", err)
	}
	gitCommonDir, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir: %v", err)
	}
	st := store.NuevoStore(gitCommonDir)

	resultado := review.ResultadoAuditoria{
		Veredicto: review.VerdictQuestion,
		Preguntas: []review.AgentQuestion{
			{ID: "q1", Text: "question for b.go", File: "b.go"},
			{ID: "q1", Text: "question for a.go", File: "a.go"},
		},
	}
	fake := &agenteFakeSecuencialReview{respuestas: []string{`{"dim":"logic","verdict":"ok"}`}}
	factoryCalls := 0
	fabrica := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		factoryCalls++
		return fake, "test", nil
	}
	opciones := review.OpcionesAuditoria{
		SHA: sha, Bundles: bundlesDePruebaReview(), Respuestas: "q1=bare answer",
	}

	_, pendientes, applyErr := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)
	const wantErr = `answer "q1" is ambiguous; qualify one of: q1@a.go, q1@b.go`
	if applyErr == nil {
		t.Fatal("expected the ambiguous bare answer error")
	}
	if applyErr.Error() != wantErr {
		t.Fatalf("aplicarPreguntasPendientes error = %q, want %q", applyErr, wantErr)
	}
	if len(pendientes) != 2 {
		t.Fatalf("pendientes = %+v, want both original questions", pendientes)
	}
	if factoryCalls != 0 || fake.llamadas != 0 || len(fake.prompts) != 0 {
		t.Fatalf("ambiguous answer triggered an audit retry: factory calls=%d, agent calls=%d, prompts=%d", factoryCalls, fake.llamadas, len(fake.prompts))
	}

	for _, fixture := range []struct {
		file string
		blob string
	}{
		{file: "a.go", blob: blobA},
		{file: "b.go", blob: blobB},
	} {
		answer, ok, lookupErr := st.RespuestaRegistrada(fixture.blob, "q1")
		if lookupErr != nil {
			t.Fatalf("RespuestaRegistrada %s: %v", fixture.file, lookupErr)
		}
		if ok {
			t.Errorf("ambiguous answer persisted for %s: %q", fixture.file, answer)
		}
	}
}

// promptConClarificaciones busca, entre los prompts capturados, el primero
// que lleva la sección de aclaraciones del usuario (marcador literal de
// internal/review/prompts.go: seccionRespuestas). Buscar por contenido en
// vez de indexar por posición fija evita que el test rompa en silencio si el
// motor añade una ronda adicional (p. ej. de refutación) que desplace los
// índices.
func promptConClarificaciones(prompts []string) (string, bool) {
	for _, p := range prompts {
		if strings.Contains(p, "Clarifications from the user") {
			return p, true
		}
	}
	return "", false
}

// TestAplicarPreguntasPendientes_RespuestaFrescaPrevaleceSobreLaDelStore
// cubre la regresión detectada tras el fix del CRITICAL de colisión de ID:
// al construir el prompt de reintento directamente desde `contestadas` (sin
// colapsar por ID), un id presente TANTO en el store (contestadas) COMO en
// una respuesta fresca de --answer (porID) para ese mismo id debía seguir
// resolviéndose con una sola línea, con la fresca ganando — no con dos
// líneas contradictorias "q1: ..." en el mismo prompt.
func TestAplicarPreguntasPendientes_RespuestaFrescaPrevaleceSobreLaDelStore(t *testing.T) {
	repo, sha := repoDePruebaConUnCommit(t, "a.go", "package a\n")
	blob, err := git.BlobDeArchivoEnCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit: %v", err)
	}
	gitCommonDir, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir: %v", err)
	}
	st := store.NuevoStore(gitCommonDir)
	if err := st.RegistrarRespuesta(blob, "q1", "respuesta vieja del store", "otro-actor"); err != nil {
		t.Fatalf("RegistrarRespuesta: %v", err)
	}

	resultado := review.ResultadoAuditoria{
		Veredicto: review.VerdictQuestion,
		Preguntas: []review.AgentQuestion{{ID: "q1", Text: "¿procede el cambio?", File: "a.go"}},
	}
	fabrica, fake := fabricaFakeSecuencialReviewCapturando([]string{
		`{"dim":"logic","verdict":"question"}`,
		`{"dim":"logic","verdict":"ok"}`,
	})
	opciones := review.OpcionesAuditoria{SHA: sha, Bundles: bundlesDePruebaReview(), Respuestas: "q1=respuesta fresca de esta invocación"}

	resultadoFinal, pendientes, applyErr := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)
	if applyErr != nil {
		t.Fatalf("aplicarPreguntasPendientes: %v", applyErr)
	}

	if len(pendientes) != 0 {
		t.Fatalf("pendientes = %+v, esperado vacío", pendientes)
	}
	if resultadoFinal.Veredicto != review.VerdictOK {
		t.Fatalf("Veredicto = %q, esperado %q", resultadoFinal.Veredicto, review.VerdictOK)
	}
	prompt, ok := promptConClarificaciones(fake.prompts)
	if !ok {
		t.Fatalf("ningún prompt recibido contiene la sección de aclaraciones del usuario: %+v", fake.prompts)
	}
	if strings.Contains(prompt, "respuesta vieja del store") {
		t.Errorf("el prompt de reintento incluye la respuesta vieja del store; debía ceder ante la fresca de esta invocación:\n%s", prompt)
	}
	if !strings.Contains(prompt, "q1: respuesta fresca de esta invocación") {
		t.Errorf("el prompt de reintento no contiene la respuesta fresca:\n%s", prompt)
	}
	if strings.Count(prompt, "q1:") != 1 {
		t.Errorf("el prompt de reintento tiene %d líneas \"q1:\", esperada exactamente 1 (sin líneas contradictorias): %s",
			strings.Count(prompt, "q1:"), prompt)
	}
}

// TestAplicarPreguntasPendientes_QuestionVacioSinReintento_NoSeRebajaAWarn
// cubre la rama reintentado==false: un veredicto "question" con Preguntas
// vacío desde el primer pase (sin ninguna respuesta conocida ni fresca) no
// debe rebajarse a warn — sería enmascarar una salida malformada del modelo
// como si fuera un deadlock ya resuelto. Esta rama nunca se había ejercitado:
// los demás tests de aplicarPreguntasPendientes siempre entran al bloque de
// reintento.
func TestAplicarPreguntasPendientes_QuestionVacioSinReintento_NoSeRebajaAWarn(t *testing.T) {
	repo, sha := repoDePruebaConUnCommit(t, "a.go", "package a\n")

	resultado := review.ResultadoAuditoria{
		Veredicto: review.VerdictQuestion,
		Preguntas: nil,
	}
	fabrica := fabricaFakeSecuencialReview(nil)
	opciones := review.OpcionesAuditoria{SHA: sha, Bundles: bundlesDePruebaReview()}

	resultadoFinal, pendientes, applyErr := aplicarPreguntasPendientes(
		repo, sha, fabrica, config.Config{}, modelprobe.NuevoVerificador(nil), opciones, resultado)
	if applyErr != nil {
		t.Fatalf("aplicarPreguntasPendientes: %v", applyErr)
	}

	if len(pendientes) != 0 {
		t.Fatalf("pendientes = %+v, esperado vacío", pendientes)
	}
	if resultadoFinal.Veredicto != review.VerdictQuestion {
		t.Fatalf("Veredicto = %q, esperado %q (no debe rebajarse a warn sin haber reintentado)",
			resultadoFinal.Veredicto, review.VerdictQuestion)
	}
}

func TestResolveQuestionAnswers(t *testing.T) {
	questions := []review.AgentQuestion{
		{ID: "q1", File: "b.go"},
		{ID: "q1", File: "a.go"},
		{ID: "q2", File: "c.go"},
	}
	tests := []struct {
		name      string
		raw       map[string]string
		want      map[questionKey]string
		wantProse string
		wantErr   string
	}{
		{
			name: "bare unique remains compatible",
			raw:  map[string]string{"q2": "unique"},
			want: map[questionKey]string{{ID: "q2", File: "c.go"}: "unique"},
		},
		{
			name: "qualified selector chooses one duplicate",
			raw:  map[string]string{"q1@b.go": "only b"},
			want: map[questionKey]string{{ID: "q1", File: "b.go"}: "only b"},
		},
		{
			name:    "ambiguous bare selector reports deterministic candidates",
			raw:     map[string]string{"q1": "ambiguous"},
			wantErr: `answer "q1" is ambiguous; qualify one of: q1@a.go, q1@b.go`,
		},
		{
			name:      "unknown qualified selector remains prose",
			raw:       map[string]string{"q1@missing.go": "unknown"},
			want:      map[questionKey]string{},
			wantProse: "q1@missing.go=unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, prose, err := resolveQuestionAnswers(tt.raw, "", questions)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected an ambiguity error")
				}
				if err.Error() != tt.wantErr {
					t.Errorf("error = %q, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveQuestionAnswers: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("answers = %#v, want %#v", got, tt.want)
			}
			if prose != tt.wantProse {
				t.Errorf("prose = %q, want %q", prose, tt.wantProse)
			}
		})
	}
}

// TestRegistrarCorreccionesNoCruzaRamas closes FU-17. Correction attribution
// tested only whether the fix touched a file named in the ficha's findings.
// While each checkout had its own ledger that was bounded by accident; once
// review, status and pr shared one ledger per repository, a fix( commit on one
// branch could clear a block recorded on an unrelated branch because both
// happened to touch the same file.
//
// The fixture is two real branches from one root, both changing the same path,
// so the only thing that can separate them is ancestry.
func TestRegistrarCorreccionesNoCruzaRamas(t *testing.T) {
	worktree := t.TempDir()
	correr := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	escribirYCommitear := func(contenido, mensaje string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(worktree, "x.go"), []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
		correr("add", "x.go")
		correr("commit", "-qm", mensaje)
		return correr("rev-parse", "HEAD")
	}
	correr("init", "-q", "-b", "main")
	escribirYCommitear("package x\n", "feat(x): root")
	raiz := correr("rev-parse", "HEAD")

	// The audited commit, blocked, on main.
	auditado := escribirYCommitear("package x // audited\n", "feat(x): audited")

	// The fix, on a branch that forked BEFORE the audited commit. It touches the
	// same file, so only ancestry tells the two apart.
	correr("checkout", "-q", "-b", "otra", raiz)
	fix := escribirYCommitear("package x // unrelated fix\n", "fix(x): unrelated")

	gitDir, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NuevoLedger(gitDir)
	if err := ledger.GuardarRevision(auditado, "feat(x): audited", "b", "m", review.Revision{
		At: time.Now(), Result: review.VerdictBlock,
		Dims: []review.DimensionResult{{Dim: review.DimLogic, Verdict: review.VerdictBlock,
			Findings: []review.ReviewFinding{{File: "x.go", Severity: "CRITICAL", Description: "d"}}}},
	}); err != nil {
		t.Fatal(err)
	}

	registrarCorrecciones(ledger, gitDir, fix, []string{"x.go"}, "fix(x): unrelated", 0, worktree)

	ficha, err := ledger.LeerFicha(auditado)
	if err != nil || ficha == nil {
		t.Fatalf("LeerFicha() = %v, %v", ficha, err)
	}
	if ficha.FixedIn != "" {
		t.Errorf("FixedIn = %q; a fix on a branch that does not contain the audited commit cleared its block because both touched x.go", ficha.FixedIn)
	}

	// The same fix, made on the audited commit's own line of history, must still
	// clear it: the check must not have simply disabled attribution.
	correr("checkout", "-q", "main")
	descendiente := escribirYCommitear("package x // real fix\n", "fix(x): real")
	registrarCorrecciones(ledger, gitDir, descendiente, []string{"x.go"}, "fix(x): real", 0, worktree)

	ficha, err = ledger.LeerFicha(auditado)
	if err != nil || ficha == nil {
		t.Fatalf("LeerFicha() = %v, %v", ficha, err)
	}
	if ficha.FixedIn != descendiente {
		t.Errorf("FixedIn = %q, want %q: a fix descending from the audited commit must still clear its block", ficha.FixedIn, descendiente)
	}
}
