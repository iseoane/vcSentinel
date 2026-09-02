package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// ejecutarReview audita uno o más commits contra el motor y guarda la ficha en
// el ledger. Códigos de salida (guía §9): 0 ok/warn, 1 block, 3 questions,
// 4 provider_unavailable. Con --gate, además, cualquier CRITICAL salta a 1.
// planDeRevision decide los bundles a auditar y, solo cuando hacen falta, lee la
// evidencia que la derivación necesita.
//
// El orden importa: unas dimensiones explícitas sustituyen el plan derivado por
// completo, así que leer .gitattributes antes de mirar dims abortaba una
// ejecución cuyos bundles ya había elegido quien llama. leerAtributos se inyecta
// para que ese salto sea comprobable sin ejecutar una revisión entera.
func planDeRevision(dims []string, profile change.ChangeProfile, archivos []string, diff string, leerAtributos func() (string, error)) ([]review.ReviewBundle, error) {
	if len(dims) > 0 {
		return []review.ReviewBundle{{Name: "requested", Dimensions: dims, Priority: review.PriorityRequired, Cost: 1}}, nil
	}
	atributos, err := leerAtributos()
	if err != nil {
		return nil, err
	}
	return review.PlanForProfile(profile, archivos, diff, atributos).Bundles, nil
}

func ejecutarReview(worktree string, args []string) {
	flags, err := parsearFlagsAuditoria(args)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	// Config ESTRICTA (hallazgo del orquestador, fuera del texto original de
	// la ficha): una clave desconocida en el yml debe cortar aquí con error
	// explícito, no seguir en silencio con la config por defecto.
	cfg, err := config.CargarConfiguracionLocalEstricta(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	cfg = aplicarTimeoutFlag(cfg, flags)
	// Resuelto desde el worktree sobre el que se opera, nunca desde el cwd del
	// proceso: ver el comentario de git.ObtenerGitDirDe.
	gitDir, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	// --prune runs BEFORE the shared ledger is built, and that order is load
	// bearing. Purging enumerates every per-checkout ledger and keeps a
	// documented fallback for when the common directory cannot be resolved;
	// building the shared ledger first turned that same resolution failure into
	// an exit, so the fallback became unreachable.
	if flags.prune {
		// --prune es un modo standalone: combinarlo con targets o flags de
		// auditoría sería ignorarlos en silencio (cf. flagsNoAplicablesAStatus).
		// Sin targets: parsearFlagsAuditoria ya no rellena "HEAD" por defecto
		// (B17), así que "solo --prune" se detecta por targets VACÍO, no por
		// targets == ["HEAD"].
		soloPrune := len(flags.targets) == 0 &&
			len(flags.dims) == 0 && !flags.all && !flags.chain && !flags.gate &&
			flags.profile == "" && flags.answer == "" && flags.timeout == 0
		if !soloPrune {
			fmt.Println("? review --prune no se combina con targets ni flags de auditoría (--dims/--all/--chain/--gate/--profile/--answer/--timeout).")
			os.Exit(1)
		}
		os.Exit(ejecutarPurgaYReportar(worktree, gitDir, flags.jsonOut))
	}

	// The ledger is anchored on the common directory, not on gitDir: see
	// sharedReviewLedger. gitDir stays for the operational event log, which is
	// per-checkout on purpose.
	ledger, err := sharedReviewLedger(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	shas, err := resolverShasAuditoria(ledger, flags)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	if len(shas) == 0 {
		fmt.Println("✅ No hay commits que auditar.")
		return
	}

	verificadorModelo := nuevoVerificadorModelo(worktree)
	exitFinal := 0

	total := len(shas)

	for idx, sha := range shas {
		fmt.Printf("⏳ [%d/%d] Auditar %s\n", idx+1, total, shaCorto(sha))
		mensaje, err := git.MensajeCommit(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudo leer el mensaje: %v\n", sha[:8], err)
			continue
		}
		diff, err := git.DiffCommit(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudo leer el diff: %v\n", sha[:8], err)
			continue
		}
		archivos, err := git.ArchivosDeCommit(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudieron leer los archivos: %v\n", sha[:8], err)
			continue
		}

		profile, err := change.PerfilDeCommit(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudo derivar el perfil de cambio: %v\n", sha[:8], err)
			os.Exit(1)
		}
		bundles, err := planDeRevision(flags.dims, profile, archivos, diff, func() (string, error) { return git.Attributes(sha) })
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudieron leer los atributos: %v\n", sha[:8], err)
			os.Exit(1)
		}

		// El recolector anota qué agente atendió cada dimensión para que la
		// ficha registre el autor real y no el perfil pedido (H4/T0.2).
		authorship := &recolectorAutoria{}
		fabrica := func(_ review.ReviewBundle, dimension string) (review.AuditorAgente, string, error) {
			profile := config.ResolverPerfil(cfg, reviewcontract.DefaultProfile(dimension), flags.profile)
			adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, profile)
			if err != nil {
				return nil, profile.Nombre, err
			}
			verificadorModelo.Verificar(profile.Nombre, profile.Modelo, adapter)
			return &observedAgent{AuditorAgente: adapter, authorship: authorship}, profile.Nombre, nil
		}
		reviewTransport, metricsFinalizer := announcedReviewTransportWithMetrics(cfg, worktree, sha, archivos, os.Stderr)
		opciones := review.OpcionesAuditoria{
			SHA:               sha,
			Mensaje:           mensaje,
			Diff:              diff,
			Bundles:           bundles,
			Respuestas:        flags.answer,
			PerfilOverride:    flags.profile,
			ProveedorContexto: proveedorContextoReview(cfg, worktree),
			RutasContexto:     archivos,
			// The announcer lives at this command boundary only: each durable
			// review run is announced on stderr (the JSON-safe channel) the
			// moment it is admitted, with its `runs attach --follow` command,
			// so an operator can attach while the review is still executing.
			ReviewTransportWithEvidence: reviewTransport,
			FinalizeMetrics:             metricsFinalizer,
			OnDimension: func(dim string) {
				fmt.Printf("  ⏳ %s …\n", dim)
			},
		}
		resultado := review.AuditarCommit(fabrica, cfg.Review.Parallel, opcionesAuditoriaConRefutador(opciones, cfg, verificadorModelo))

		resultado, pendientes, err := aplicarPreguntasPendientes(worktree, sha, fabrica, cfg, verificadorModelo, opciones, resultado)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}
		if flags.jsonOut && resultado.Veredicto == review.VerdictQuestion {
			imprimirPreguntasPendientesJSON(sha, pendientes)
		}

		modelo := flags.profile
		if modelo == "" {
			modelo = "default"
		}
		fixed := review.RevisionCorrigeBlockPrevio(ledger, sha, resultado.Veredicto)
		efectivo := authorship.consolidar()
		revision := review.Revision{
			At:                 time.Now(),
			Result:             resultado.Veredicto,
			Fixed:              fixed,
			Agent:              efectivo.Binario,
			Model:              efectivo.Modelo,
			Effort:             efectivo.Esfuerzo,
			Dims:               review.DimsResultadosParaFicha(resultado.Dims),
			AggregatedFindings: resultado.Findings,
		}
		if err := ledger.GuardarRevision(sha, mensaje, "", modelo, revision); err != nil {
			fmt.Printf("⚠️ %s: no se pudo guardar la ficha: %v\n", sha[:8], err)
		}

		fmt.Print(resultado.String())
		if fixed {
			fmt.Printf("  ✅ revisión anterior en block corregida por esta revisión\n")
		}

		exit := codigoSalidaVeredicto(resultado.Veredicto)
		if flags.gate && tieneHallazgosCriticos(resultado) {
			exit = 1
		}
		if exit > exitFinal {
			exitFinal = exit
		}
		detalle := reviewEventDetail(flags, resultado)
		if err := ops.RegistrarEvento(gitDir, "review", exit, []string{sha}, detalle, worktree); err != nil {
			fmt.Printf("⚠️ No se pudo registrar el evento: %v\n", err)
		}
		registrarCorrecciones(ledger, gitDir, sha, archivos, mensaje, exit, worktree)
	}
	os.Exit(exitFinal)
}

func reviewEventDetail(flags flagsAuditoria, result review.ResultadoAuditoria) ops.EventDetail {
	detail := ops.EventDetail{
		"all":   flags.all,
		"chain": flags.chain,
		"gate":  flags.gate,
		"dims":  flags.dims,
	}
	if result.ContextSkipReason != "" {
		detail["context_skip_reason"] = result.ContextSkipReason
	}
	var failures []ops.EventDetail
	for _, dimension := range result.Dims {
		if dimension.Resultado != nil && dimension.Resultado.Verdict != review.VerdictUnavailable {
			continue
		}
		if dimension.Resultado == nil && dimension.Error == nil {
			continue
		}
		bundle, dim, reason := dimension.Bundle, dimension.Dim, ""
		if dimension.Resultado != nil {
			if bundle == "" {
				bundle = dimension.Resultado.Bundle
			}
			if dim == "" {
				dim = dimension.Resultado.Dim
			}
			reason = dimension.Resultado.Reason
		}
		if strings.TrimSpace(reason) == "" && dimension.Error != nil {
			reason = dimension.Error.Error()
		}
		reason = review.CompactProviderCause(reason)
		if strings.TrimSpace(reason) == "" {
			continue
		}
		failures = append(failures, ops.EventDetail{
			"bundle":    bundle,
			"dimension": dim,
			"reason":    reason,
		})
	}
	if len(failures) > 0 {
		detail["reviewer_failures"] = failures
	}
	return detail
}

var nuevoVerificadorModelo = func(worktree string) *modelprobe.Verificador {
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return modelprobe.NuevoVerificador(nil)
	}
	return modelprobe.NuevoVerificador(store.NuevoStore(gitCommonDir))
}

func proveedorContextoReview(cfg config.Config, worktree string) review.ContextProvider {
	if !cfg.Review.CodeGraphContext || !permiteDiffAgenteExterno(worktree) {
		return nil
	}
	return graph.DetectarProveedorCodeGraph(worktree)
}

func opcionesAuditoriaConRefutador(opts review.OpcionesAuditoria, cfg config.Config, verificador *modelprobe.Verificador) review.OpcionesAuditoria {
	opts.FabricaRefutador = fabricaRefutador(cfg, verificador)
	return opts
}

// aplicarPreguntasPendientes deduplicates resultado.Preguntas against
// answers already persisted in the store (T7.5/T7.6): if a question from
// this pass was already answered for the same content blob, it is folded
// back into a retry round exactly as if the user had repeated it via
// --answer in this same invocation. It also persists any fresh "id=text"
// answer the user supplies now (parsearRespuestasAuditoria, filtered to
// real question IDs by filtrarRespuestasPorIDsReales), so a future run over
// the same blob does not ask again. It never aborts the command: a
// missing/unavailable store, or any error while resolving blobs or looking
// up answers, degrades to reporting the raw (non-deduplicated) questions.
//
// Before returning, it always writes the final pending list back into
// resultado.Preguntas so every other consumer of resultado (the ledger, the
// human-readable text output, the exit code) sees the same deduplicated
// state. It also reconciles resultado.Veredicto: a model is not guaranteed
// to stop asking just because it received the clarification in the retry
// round, so if it re-emits "question" with only questions this run already
// has a registered answer for, that must not be a permanent block (exit 3
// forever on every future run over identical content) — it is downgraded to
// warn instead. That downgrade only fires when a retry actually happened
// with real answers (known or fresh): a "question" verdict with an empty
// questions list from the very first pass (malformed model output, no
// answer involved at all) is left as-is, not silently masked as warn.
func aplicarPreguntasPendientes(worktree, sha string, fabrica review.FabricaAuditor, cfg config.Config, verificador *modelprobe.Verificador, opciones review.OpcionesAuditoria, resultado review.ResultadoAuditoria) (review.ResultadoAuditoria, []review.AgentQuestion, error) {
	if resultado.Veredicto != review.VerdictQuestion {
		return resultado, resultado.Preguntas, nil
	}
	gitCommonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return resultado, resultado.Preguntas, nil
	}
	st := store.NuevoStore(gitCommonDir)
	resolveBlob := func(file string) (string, error) { return git.BlobDeArchivoEnCommit(sha, file) }

	originales := resultado.Preguntas
	pendientes, contestadas, err := review.SplitPendingQuestions(originales, resolveBlob, st.RespuestaRegistrada)
	if err != nil {
		return resultado, originales, nil
	}

	porIDCrudo, restoCrudo := parsearRespuestasAuditoria(opciones.Respuestas)
	// porIDCrudo puede contener "ids" que en realidad son prosa libre con un
	// "=" literal (p. ej. --answer "the flag --gate=true is set"): se
	// filtran aquí, antes de construir el prompt de reintento y antes del
	// bucle de persistencia, para que un id inexistente nunca llegue a
	// ninguno de los dos.
	porPregunta, resto, err := resolveQuestionAnswers(porIDCrudo, restoCrudo, originales)
	if err != nil {
		return resultado, originales, err
	}

	// Persistir las respuestas frescas ANTES del reintento (no después): el
	// SplitPendingQuestions posterior al reintento consulta el store, así
	// que si una pregunta recién contestada por el usuario vuelve a
	// aparecer en esa segunda pasada, debe reconocerse como ya respondida en
	// esta misma invocación, no solo en una ejecución futura.
	for pregunta, texto := range porPregunta {
		if pregunta.File == "" {
			continue // pregunta sin File: no se puede resolver un blob, no es un caso de error
		}
		blob, err := resolveBlob(pregunta.File)
		if err != nil {
			fmt.Printf("⚠️ %s: no se pudo resolver el blob de %q para persistir la respuesta a %q: %v\n", shaCorto(sha), pregunta.File, pregunta.ID, err)
			continue
		}
		if err := st.RegistrarRespuesta(blob, pregunta.ID, texto, resolverActor(worktree)); err != nil {
			fmt.Printf("⚠️ %s: no se pudo persistir la respuesta a %q: %v\n", shaCorto(sha), pregunta.ID, err)
		}
	}

	// reintentado distingue "se relanzó AuditarCommit con respuestas reales"
	// de "pendientes llegó vacío porque originales ya estaba vacío" (un
	// veredicto "question" con lista de preguntas vacía es una salida
	// malformada del modelo, no un deadlock resuelto): sin esta bandera, la
	// rebaja de veredicto de más abajo enmascararía ese caso como warn sin
	// haber pasado por ninguna respuesta conocida.
	reintentado := false
	if len(contestadas) > 0 || len(porPregunta) > 0 {
		// Unión de lo ya conocido por el store y lo que el usuario responde
		// ahora mismo (porID): una respuesta fresca sobre el mismo id
		// prevalece sobre la ya registrada, porque el usuario está
		// respondiendo activamente en esta invocación. Disparar el
		// reintento también cuando solo hay porID (sin contestadas del
		// store) evita que una respuesta recién dada por el usuario en esta
		// misma invocación se reporte como pendiente en su propio --json.
		var lineas []string
		if resto != "" {
			lineas = append(lineas, resto)
		}
		// contestadas conserva su propio (File, Answer): no se colapsa por
		// ID en un map[string]string, porque AgentQuestion.ID lo elige el
		// modelo por dimensión sin garantía de unicidad entre dimensiones —
		// dos preguntas distintas (archivos distintos) pueden compartir "q1"
		// legítimamente, y cada una debe llegar con su propia respuesta al
		// prompt de reintento. Se ordena por (ID, File) para que el prompt
		// sea determinista. Una entrada cuyo ID también está en porID se
		// omite aquí: la respuesta fresca de esta misma invocación prevalece
		// sobre la ya registrada para ESE id (el bucle de porID de abajo la
		// emite); sin este filtro, un id presente en ambos conjuntos
		// generaría dos líneas "id: ..." contradictorias en el mismo prompt.
		ordenadas := append([]review.AnsweredQuestion(nil), contestadas...)
		sort.Slice(ordenadas, func(i, j int) bool {
			if ordenadas[i].Question.ID != ordenadas[j].Question.ID {
				return ordenadas[i].Question.ID < ordenadas[j].Question.ID
			}
			return ordenadas[i].Question.File < ordenadas[j].Question.File
		})
		for _, aq := range ordenadas {
			key := questionKey{ID: aq.Question.ID, File: aq.Question.File}
			if _, fresca := porPregunta[key]; fresca {
				continue
			}
			lineas = append(lineas, questionSelector(key, originales)+": "+aq.Answer)
		}
		for _, key := range sortedQuestionKeys(porPregunta) {
			lineas = append(lineas, questionSelector(key, originales)+": "+porPregunta[key])
		}
		opciones.Respuestas = strings.Join(lineas, "\n")
		resultado = review.AuditarCommit(fabrica, cfg.Review.Parallel, opcionesAuditoriaConRefutador(opciones, cfg, verificador))
		reintentado = true
		if pendientes, _, err = review.SplitPendingQuestions(resultado.Preguntas, resolveBlob, st.RespuestaRegistrada); err != nil {
			pendientes = resultado.Preguntas
		}
	}

	resultado.Preguntas = pendientes
	if reintentado && resultado.Veredicto == review.VerdictQuestion && len(pendientes) == 0 {
		// El agente siguió preguntando aunque ya tenía respuesta registrada
		// para cada una de sus preguntas actuales (un modelo no está
		// garantizado a dejar de preguntar solo porque recibió la
		// aclaración). El sistema ya tiene respuesta para todo lo pendiente,
		// así que esto no debe bloquear para siempre: se rebaja a warn en
		// vez de dejar un deadlock permanente en "question" (exit 3 en cada
		// ejecución futura sobre el mismo contenido). Solo se aplica cuando
		// realmente hubo un reintento con respuestas: un "question" con
		// preguntas vacías desde el primer pase (sin conocidas ni frescas)
		// nunca entra en esta rama.
		resultado.Veredicto = review.VerdictWarn
	}
	return resultado, pendientes, nil
}

type questionKey struct {
	ID   string
	File string
}

// resolveQuestionAnswers binds bare selectors to one unique question and
// qualified selectors to the exact (ID, File) pair. Unknown selectors retain
// the existing free-text behavior; ambiguous bare IDs fail explicitly.
func resolveQuestionAnswers(raw map[string]string, prose string, questions []review.AgentQuestion) (map[questionKey]string, string, error) {
	byID := make(map[string]map[string]questionKey)
	for _, q := range questions {
		if byID[q.ID] == nil {
			byID[q.ID] = make(map[string]questionKey)
		}
		byID[q.ID][q.File] = questionKey{ID: q.ID, File: q.File}
	}

	resolved := make(map[questionKey]string)
	var unknown []string
	for _, selector := range clavesOrdenadas(raw) {
		id, file, qualified := strings.Cut(selector, "@")
		candidates := byID[id]
		if qualified {
			key, ok := candidates[file]
			if ok {
				resolved[key] = raw[selector]
				continue
			}
			unknown = append(unknown, selector+"="+raw[selector])
			continue
		}
		if len(candidates) == 1 {
			for _, key := range candidates {
				resolved[key] = raw[selector]
			}
			continue
		}
		if len(candidates) > 1 {
			candidateNames := make([]string, 0, len(candidates))
			for _, key := range candidates {
				candidateNames = append(candidateNames, key.ID+"@"+key.File)
			}
			sort.Strings(candidateNames)
			return nil, prose, fmt.Errorf("answer %q is ambiguous; qualify one of: %s", selector, strings.Join(candidateNames, ", "))
		}
		unknown = append(unknown, selector+"="+raw[selector])
	}
	if prose != "" {
		unknown = append(unknown, prose)
	}
	return resolved, strings.Join(unknown, ","), nil
}

func sortedQuestionKeys(answers map[questionKey]string) []questionKey {
	keys := make([]questionKey, 0, len(answers))
	for key := range answers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ID != keys[j].ID {
			return keys[i].ID < keys[j].ID
		}
		return keys[i].File < keys[j].File
	})
	return keys
}

func questionSelector(key questionKey, questions []review.AgentQuestion) string {
	files := make(map[string]struct{})
	for _, q := range questions {
		if q.ID == key.ID {
			files[q.File] = struct{}{}
		}
	}
	if len(files) > 1 {
		return key.ID + "@" + key.File
	}
	return key.ID
}

// filtrarRespuestasPorIDsReales separates porID (from parsearRespuestasAuditoria)
// into the entries whose id actually matches one of preguntas' real
// question IDs from this pass, and the entries that don't. An "id=text"
// token whose id was never asked in this pass is not a targeted answer: it
// is ordinary free text that happened to contain a literal "=" (e.g.
// --answer "the flag --gate=true is set" must not be parsed as an answer to
// a nonexistent question "the flag --gate"). Non-matching entries are folded
// back into resto, reconstructed as "id=text", so they keep behaving as
// plain prose instead of being silently dropped or persisted as garbage.
// parsearRespuestasAuditoria itself stays pure/context-free — it doesn't
// know about real question IDs — which is why this filtering lives here,
// where that context (preguntas) is available.
func filtrarRespuestasPorIDsReales(porID map[string]string, resto string, preguntas []review.AgentQuestion) (map[string]string, string) {
	reales := make(map[string]bool, len(preguntas))
	for _, q := range preguntas {
		reales[q.ID] = true
	}

	validado := make(map[string]string, len(porID))
	var prosaAjena []string
	for _, id := range clavesOrdenadas(porID) {
		if reales[id] {
			validado[id] = porID[id]
			continue
		}
		prosaAjena = append(prosaAjena, id+"="+porID[id])
	}
	if len(prosaAjena) == 0 {
		return validado, resto
	}
	if resto != "" {
		prosaAjena = append(prosaAjena, resto)
	}
	return validado, strings.Join(prosaAjena, ",")
}

// clavesOrdenadas devuelve las claves de m ordenadas: un map no itera en
// orden determinista y este texto se manda literalmente a un agente o se
// reconstruye como salida — dos ejecuciones idénticas deben producir el
// mismo resultado.
func clavesOrdenadas(m map[string]string) []string {
	claves := make([]string, 0, len(m))
	for clave := range m {
		claves = append(claves, clave)
	}
	sort.Strings(claves)
	return claves
}

// archivoDePregunta devuelve el File de la pregunta id dentro de preguntas, o
// "" si no aparece (id no preguntado en este pase, o preguntado sin File).
func archivoDePregunta(preguntas []review.AgentQuestion, id string) string {
	for _, q := range preguntas {
		if q.ID == id {
			return q.File
		}
	}
	return ""
}

// parsearRespuestasAuditoria separates --answer into "id=text" targeted
// answers and the leftover free-text prose, so a targeted answer can be
// deduplicated/persisted (T7.6) while every other shape of --answer keeps
// behaving exactly as before. Tokens are comma-separated; this is
// deliberately simple, not a CSV/quoting parser, so free-text prose
// containing a literal comma is split into several prose tokens — an
// accepted limitation of this simple transport.
func parsearRespuestasAuditoria(respuesta string) (porID map[string]string, resto string) {
	porID = make(map[string]string)
	if respuesta == "" {
		return porID, ""
	}
	var prosa []string
	for _, token := range strings.Split(respuesta, ",") {
		id, texto, tieneIgual := strings.Cut(token, "=")
		id = strings.TrimSpace(id)
		if !tieneIgual || id == "" {
			prosa = append(prosa, token)
			continue
		}
		porID[id] = strings.TrimSpace(texto)
	}
	return porID, strings.Join(prosa, ",")
}

// imprimirPreguntasPendientesJSON imprime, en una sola línea, las preguntas
// que siguen pendientes tras la deduplicación (T7.6). El shape es una
// preocupación de presentación de cmd/sentinel, no de internal/review.
func imprimirPreguntasPendientesJSON(sha string, pendientes []review.AgentQuestion) {
	type preguntaPendiente struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		File string `json:"file,omitempty"`
	}
	payload := struct {
		SHA              string              `json:"sha"`
		PendingQuestions []preguntaPendiente `json:"pending_questions"`
	}{SHA: sha, PendingQuestions: []preguntaPendiente{}}
	for _, q := range pendientes {
		payload.PendingQuestions = append(payload.PendingQuestions, preguntaPendiente{ID: q.ID, Text: q.Text, File: q.File})
	}
	datos, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("⚠️ %s: no se pudo serializar las preguntas pendientes: %v\n", shaCorto(sha), err)
		return
	}
	fmt.Println(string(datos))
}

// fabricaRefutador resolves the explicit cheap profile for the independent
// challenge that runs once for each semantic CRITICAL finding.
func fabricaRefutador(cfg config.Config, verificador *modelprobe.Verificador) review.FabricaRefutador {
	return func() (review.AuditorAgente, string, error) {
		perfil := config.ResolverPerfil(cfg, "", "cheap")
		adapter, err := agentadapter.NuevoAdaptadorConPerfil(cfg, perfil)
		if err != nil {
			return nil, perfil.Nombre, err
		}
		verificador.Verificar(perfil.Nombre, perfil.Modelo, adapter)
		return adapter, perfil.Nombre, nil
	}
}

// registrarCorrecciones asocia un commit fix (mensaje fix(...) que sale sin
// críticos) con las fichas previas en block que tocan los mismos archivos:
// marca su FixedIn y registra un evento fix.
func registrarCorrecciones(ledger *review.Ledger, gitDir, sha string, archivos []string, mensaje string, exit int, worktree string) {
	if exit != 0 || !strings.HasPrefix(mensaje, "fix(") {
		return
	}
	shasPrevios, err := ledger.ListarFichas()
	if err != nil {
		return
	}
	archivosFix := map[string]bool{}
	for _, a := range archivos {
		archivosFix[a] = true
	}

	corregidos := []string{}
	for _, shaPrev := range shasPrevios {
		if shaPrev == sha {
			continue
		}
		ficha, err := ledger.LeerFicha(shaPrev)
		if err != nil || ficha == nil || len(ficha.Revisions) == 0 || ficha.FixedIn != "" {
			continue
		}
		ultima := ficha.Revisions[len(ficha.Revisions)-1]
		if ultima.Result != review.VerdictBlock {
			continue
		}
		if !fixTocaHallazgos(archivosFix, ultima.Dims) {
			continue
		}
		// The audited commit must be an ancestor of the fix, in that order: a
		// fix comes after what it corrects. File-name overlap was the whole
		// test until the ledger became repository-wide, and then a fix( commit
		// on one branch could clear a block recorded on an unrelated branch
		// because both happened to touch the same file (FU-17).
		//
		// A query that cannot be answered attributes nothing. That is the safe
		// direction here — the ficha keeps its block and a later fix can still
		// clear it — and it matches the rest of this function, which is
		// best-effort and already returns silently when the ledger cannot be
		// listed.
		mismaHistoria, err := git.EsAncestroDe(worktree, shaPrev, sha)
		if err != nil || !mismaHistoria {
			continue
		}
		if err := ledger.MarcarCorregida(shaPrev, sha); err == nil {
			corregidos = append(corregidos, shaPrev)
		}
	}

	if len(corregidos) > 0 {
		_ = ops.RegistrarEvento(gitDir, "fix", 0, []string{sha},
			ops.EventDetail{"corrects": corregidos}, worktree)
		fmt.Printf("  🔧 fix %s marcado como corrección de: %s\n", shaCorto(sha), strings.Join(corregidos, ","))
	}
}

// shaCorto recorta un SHA a 8 caracteres sin reventar si es más corto (los
// tests usan SHAs cortos).
func shaCorto(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// fixTocaHallazgos indica si el commit fix toca algún archivo señalado en los
// hallazgos de las dimensiones previas.
func fixTocaHallazgos(archivosFix map[string]bool, dims []review.DimensionResult) bool {
	for _, dim := range dims {
		for _, hallazgo := range dim.Findings {
			if archivosFix[hallazgo.File] {
				return true
			}
		}
	}
	return false
}

// resolverShasAuditoria calcula los SHAs a auditar según los flags: la lista
// de commits pedida (default HEAD), la cadena desde el base (--chain) o todos
// los commits sin ficha (--all). --chain/--all no se combinan con targets
// explícitos: no tiene sentido mezclar dos criterios de selección.
// resolverShasAuditoria recibe el ledger ya construido por el llamante en vez
// de resolverlo de nuevo: era la segunda resolución del mismo dato y la hacía
// desde el cwd, así que --all leía el ledger del repositorio equivocado cuando
// el worktree no era el directorio de trabajo. It now receives the ledger
// itself rather than a directory, so --all cannot resolve a different anchor
// than the one the audit writes to.
func resolverShasAuditoria(ledger *review.Ledger, flags flagsAuditoria) ([]string, error) {
	// Default a HEAD vive aquí, no en parsearFlagsAuditoria (B17): es la
	// única función que necesita un target por defecto (status no tiene
	// concepto de target). flags se recibe por valor: mutar targets aquí no
	// afecta a la copia que ya usó flagsNoAplicablesAStatus.
	if len(flags.targets) == 0 {
		flags.targets = []string{"HEAD"}
	}
	switch {
	case flags.chain:
		if len(flags.targets) > 1 {
			return nil, errors.New("--chain no se combina con una lista de commits: usa un solo target como extremo")
		}
		base, err := git.UpstreamOMain()
		if err != nil {
			return nil, err
		}
		return git.SHAsRango(base, flags.targets[0])
	case flags.all:
		if len(flags.targets) > 1 {
			return nil, errors.New("--all no se combina con una lista de commits: audita todo el historial sin ficha")
		}
		todos, err := git.SHAsHasta(flags.targets[0])
		if err != nil {
			return nil, err
		}
		auditados, err := ledger.ListarFichas()
		if err != nil {
			return nil, err
		}
		yaAuditados := map[string]bool{}
		for _, sha := range auditados {
			yaAuditados[sha] = true
		}
		var pendientes []string
		for _, sha := range todos {
			if !yaAuditados[sha] {
				pendientes = append(pendientes, sha)
			}
		}
		return pendientes, nil
	default:
		resueltos := make([]string, 0, len(flags.targets))
		for _, expresion := range flags.targets {
			sha, err := git.ResolverSHA(expresion)
			if err != nil {
				return nil, fmt.Errorf("no se pudo resolver %q: %v", expresion, err)
			}
			resueltos = append(resueltos, sha)
		}
		return resueltos, nil
	}
}

// codigoSalidaVeredicto traduce el veredicto global al código de salida según
// la guía §9: ok/warn 0, block 1, question 3, unavailable 4.
func codigoSalidaVeredicto(veredicto string) int {
	switch veredicto {
	case review.VerdictBlock:
		return 1
	case review.VerdictQuestion:
		return 3
	case review.VerdictUnavailable:
		return 4
	default:
		return 0
	}
}

// tieneHallazgosCriticos indica si alguna dimensión reportó CRITICAL.
func tieneHallazgosCriticos(resultado review.ResultadoAuditoria) bool {
	for _, rd := range resultado.Dims {
		if rd.Resultado == nil {
			continue
		}
		for _, hallazgo := range rd.Resultado.Findings {
			if hallazgo.Severity == review.SevCritical {
				return true
			}
		}
	}
	return false
}
