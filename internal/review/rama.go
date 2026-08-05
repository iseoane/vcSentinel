package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// LimiteDecisionChain es el umbral de volumen (líneas añadidas+borradas) a
// partir del cual una rama propone cadena de PRs salvo coherencia demostrada.
// Es el mismo umbral del guardián de volumen: la decisión de PR sigue la
// regla de volumen del proyecto.
const LimiteDecisionChain = 400

// OpcionesRama define el análisis de una rama completa contra su base.
type OpcionesRama struct {
	Base           string // rama de comparación; vacío = "main"
	SoloPendientes bool   // --only-unaudited: no auditar, solo mostrar fichas
	Overview       bool   // --overview: 1 llamada Spec de rama para la coherencia
	PerfilOverride string
	Respuestas     string // aclaraciones para la ronda extra de preguntas
	OnDimension    func(dim string)
	Fabrica        FabricaAuditor
	Parallel       int
}

// ResultadoOverview es la respuesta de la llamada Spec de rama: coherencia
// del conjunto y rationale para la plantilla de la PR.
type ResultadoOverview struct {
	Coherente bool   `json:"coherente"`
	Rationale string `json:"rationale"`
}

// ResultadoRama agrega el análisis completo de la rama: los SHAs del rango,
// las fichas, el volumen real y la decisión single/chain.
type ResultadoRama struct {
	Rama          string
	SHAs          []string
	Pendientes    []string
	Fichas        []Ficha
	Volumen       int
	Overview      *ResultadoOverview // nil si no se pidió o no se pudo obtener
	OverviewError string             // por qué no hay overview, si se pidió y falló
	Decision      string             // "single" | "chain"
}

// AnalizarRama analiza la rama actual contra su base (guía §12.1): resuelve
// el merge-base, audita los SHAs pendientes con el motor (el ledger es caché,
// no autoridad), mide el volumen real con numstat, ejecuta el overview si se
// pidió y decide PR única vs cadena por volumen + coherencia.
func AnalizarRama(ledger *Ledger, opts OpcionesRama) (*ResultadoRama, error) {
	base := opts.Base
	if base == "" {
		base = "main"
	}
	rama, err := git.RamaActual()
	if err != nil {
		return nil, err
	}
	mergeBase, err := git.MergeBase(base, "HEAD")
	if err != nil {
		return nil, err
	}
	shas, err := git.SHAsRango(mergeBase, "HEAD")
	if err != nil {
		return nil, err
	}

	var pendientes []string
	for _, sha := range shas {
		ficha, err := ledger.LeerFicha(sha)
		if err != nil {
			return nil, err
		}
		if ficha == nil {
			pendientes = append(pendientes, sha)
		}
	}

	if !opts.SoloPendientes {
		for _, sha := range pendientes {
			if err := auditarCommitRama(ledger, sha, opts); err != nil {
				return nil, fmt.Errorf("no se pudo auditar %s: %v", sha, err)
			}
		}
	}

	fichas := make([]Ficha, 0, len(shas))
	for _, sha := range shas {
		ficha, err := ledger.LeerFicha(sha)
		if err != nil {
			return nil, err
		}
		if ficha != nil {
			fichas = append(fichas, *ficha)
		}
	}

	volumen, err := git.NumstatRango(mergeBase, "HEAD")
	if err != nil {
		return nil, err
	}

	res := &ResultadoRama{
		Rama: rama, SHAs: shas, Pendientes: pendientes,
		Fichas: fichas, Volumen: volumen,
	}
	if opts.Overview {
		overview, err := overviewDeRama(opts, rama, fichas)
		res.Overview = overview
		if err != nil {
			res.OverviewError = err.Error()
		}
	}

	// Decisión por dos ejes: volumen real + coherencia (guía §12.1).
	switch {
	case volumen <= LimiteDecisionChain:
		res.Decision = "single"
	case res.Overview == nil || !res.Overview.Coherente:
		res.Decision = "chain"
	default:
		res.Decision = "single"
	}
	return res, nil
}

// auditarCommitRama audita un commit pendiente con el motor y persiste la
// revisión en el ledger con bucket "pr" (origen: análisis de rama).
func auditarCommitRama(ledger *Ledger, sha string, opts OpcionesRama) error {
	mensaje, err := git.MensajeCommit(sha)
	if err != nil {
		return err
	}
	diff, err := git.DiffCommit(sha)
	if err != nil {
		return err
	}
	archivos, err := git.ArchivosDeCommit(sha)
	if err != nil {
		return err
	}

	resultado := AuditarCommit(opts.Fabrica, opts.Parallel, OpcionesAuditoria{
		SHA:            sha,
		Mensaje:        mensaje,
		Diff:           diff,
		Dims:           DimensionesParaArchivos(archivos),
		Respuestas:     opts.Respuestas,
		PerfilOverride: opts.PerfilOverride,
		OnDimension:    opts.OnDimension,
	})

	modelo := opts.PerfilOverride
	if modelo == "" {
		modelo = "default"
	}
	revision := Revision{
		At:     time.Now(),
		Result: resultado.Veredicto,
		Fixed:  RevisionCorrigeBlockPrevio(ledger, sha, resultado.Veredicto),
		Dims:   DimsResultadosParaFicha(resultado.Dims),
	}
	return ledger.GuardarRevision(sha, mensaje, "pr", modelo, revision)
}

// overviewDeRama ejecuta la llamada Spec de rama (una sola, no una por
// commit). El error se propaga para que un fallo del overview no decida en
// silencio: el análisis sigue (con decisión chain como fallback seguro) pero
// el caller conoce la causa exacta.
func overviewDeRama(opts OpcionesRama, rama string, fichas []Ficha) (*ResultadoOverview, error) {
	if opts.Fabrica == nil {
		return nil, errors.New("sin fábrica de auditores configurada")
	}
	agente, _, err := opts.Fabrica(DimSpec)
	if err != nil {
		return nil, fmt.Errorf("no se pudo crear el auditor de rama: %w", err)
	}
	salida, err := agente.EjecutarPrompt(ConstruirPromptOverview(rama, fichas))
	if err != nil {
		return nil, fmt.Errorf("el auditor de rama no respondió: %w", err)
	}
	overview, err := ParseOverview(salida)
	if err != nil {
		return nil, fmt.Errorf("respuesta de coherencia inválida: %w", err)
	}
	return overview, nil
}

// ConstruirPromptOverview pide al agente la coherencia del conjunto de
// commits de la rama en una sola llamada (dimensión Spec a nivel de rama).
func ConstruirPromptOverview(rama string, fichas []Ficha) string {
	var b strings.Builder
	b.WriteString("Eres el revisor de coherencia de una rama de desarrollo.\n")
	b.WriteString("Rama: " + rama + "\n\nCommits de la rama:\n")
	for _, ficha := range fichas {
		b.WriteString(fmt.Sprintf("- %s %s\n", shaCortoRama(ficha.SHA), ficha.Message))
	}
	b.WriteString("\n¿Forman estos commits un único cambio coherente con la rama (una sola PR) o son unidades independientes con costuras que merecen PRs separadas en cadena?\n")
	b.WriteString("Devuelve SOLO una línea JSON con esta forma exacta:\n")
	b.WriteString(`{"coherente": true|false, "rationale": "<explicación de 3 a 5 líneas en castellano>"}` + "\n")
	return b.String()
}

// ParseOverview extrae la respuesta de coherencia del texto del agente:
// recorre los objetos JSON balanceados y toma el primero que contenga el
// campo "coherente" (tolera texto alrededor, JSON multilínea y preamble JSON).
func ParseOverview(salida string) (*ResultadoOverview, error) {
	desde := 0
	for {
		inicio := strings.Index(salida[desde:], "{")
		if inicio < 0 {
			return nil, errors.New("el agente no devolvió un JSON de coherencia")
		}
		inicio += desde
		fin, ok := cierreJSON(salida, inicio)
		if !ok {
			return nil, errors.New("el agente no devolvió un JSON de coherencia")
		}
		candidato := salida[inicio : fin+1]
		if strings.Contains(candidato, "coherente") {
			var overview ResultadoOverview
			if err := json.Unmarshal([]byte(candidato), &overview); err != nil {
				return nil, fmt.Errorf("JSON de coherencia inválido: %v", err)
			}
			return &overview, nil
		}
		desde = fin + 1
	}
}

// cierreJSON devuelve el índice del '}' que cierra el objeto JSON que empieza
// en inicio, sin confundirse con llaves dentro de strings.
func cierreJSON(salida string, inicio int) (int, bool) {
	profundidad := 0
	enString := false
	escapado := false
	for i := inicio; i < len(salida); i++ {
		c := salida[i]
		if enString {
			if escapado {
				escapado = false
				continue
			}
			switch c {
			case '\\':
				escapado = true
			case '"':
				enString = false
			}
			continue
		}
		switch c {
		case '"':
			enString = true
		case '{':
			profundidad++
		case '}':
			profundidad--
			if profundidad == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// shaCortoRama recorta un SHA a 8 caracteres sin reventar si es más corto.
func shaCortoRama(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// RevisionCorrigeBlockPrevio indica si esta auditoría (sin block) corrige una
// revisión anterior del mismo SHA que estaba en block.
func RevisionCorrigeBlockPrevio(ledger *Ledger, sha, veredicto string) bool {
	if veredicto == VerdictBlock {
		return false
	}
	ficha, err := ledger.LeerFicha(sha)
	if err != nil || ficha == nil || len(ficha.Revisions) == 0 {
		return false
	}
	return ficha.Revisions[len(ficha.Revisions)-1].Result == VerdictBlock
}

// DimsResultadosParaFicha copia los resultados de las dimensiones a la forma
// que persiste la ficha (sin el error ni el perfil, que ya van en otros
// campos).
func DimsResultadosParaFicha(dims []ResultadoDimension) []DimensionResult {
	resultados := make([]DimensionResult, 0, len(dims))
	for _, rd := range dims {
		if rd.Resultado != nil {
			resultados = append(resultados, *rd.Resultado)
		}
	}
	return resultados
}
