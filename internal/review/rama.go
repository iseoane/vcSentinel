package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// ErrSinFabrica señala que no hay fábrica de auditores configurada para el
// overview. Es un error de configuración, no de ejecución: se puede comparar
// con errors.Is desde el caller.
var ErrSinFabrica = errors.New("sin fábrica de auditores configurada")

// LimiteDecisionChain es el umbral de volumen (líneas añadidas+borradas) a
// partir del cual una rama propone cadena de PRs salvo coherencia demostrada.
//
// Deriva de git.LimiteLineasRevisables POR DECISIÓN, no por casualidad: la
// decisión de partir un PR sigue la misma regla de volumen que el guardián,
// porque mide lo mismo — cuánto cambio puede revisar una persona de una
// sentada. Antes de T0.4 era un 400 escrito aparte que podía divergir en
// silencio del umbral del guardián (B4).
const LimiteDecisionChain = git.LimiteLineasRevisables

// StoreBlobs es el mínimo que AnalizarRama necesita del store de T2.5/T2.6
// para reutilizar revisiones por contenido de blob en vez de por SHA (T2.7):
// un rebase cambia el SHA de un commit sin tocar el contenido de sus
// archivos, así que consultar por blob es lo que sobrevive al rebase.
//
// Se define aquí, en internal/review, y NO en internal/store, a propósito:
// internal/store ya importa internal/review (review.Hallazgo, review.Ledger
// en la migración v1), así que si este paquete importara internal/store se
// crearía un ciclo review→store→review. *store.Store implementa esta
// interfaz de forma estructural, sin que ninguno de los dos paquetes
// necesite conocer al otro por nombre.
type StoreBlobs interface {
	// YaRevisado indica si blob ya se vio en algún commit auditado
	// anteriormente (bajo cualquier SHA) y devuelve los hallazgos v2
	// asociados, si los hay (ver store.Store.YaRevisado).
	YaRevisado(blob string) (bool, []Hallazgo, error)
	// RegistrarBlobsCommit guarda los blobs de un commit recién auditado
	// para que un rebase futuro pueda reconocerlos (ver
	// store.Store.RegistrarBlobsCommit).
	RegistrarBlobsCommit(sha string, blobs map[string]string) error
	// SHAsDeBlob devuelve los SHAs de commit que registraron blob, o nil si
	// nunca se registró (ver store.Store.SHAsDeBlob). commitCubiertoPorBlobs
	// lo usa para calcular la intersección exacta de SHAs entre todos los
	// blobs de un commit, no solo si "algún" SHA cubre cada blob por
	// separado.
	SHAsDeBlob(blob string) ([]string, error)
}

// OpcionesRama define el análisis de una rama completa contra su base.
type OpcionesRama struct {
	Base           string // rama de comparación; vacío = "main"
	SoloPendientes bool   // --only-unaudited: no auditar, solo mostrar fichas
	Overview       bool   // --overview: 1 llamada Spec de rama para la coherencia
	PerfilOverride string
	Respuestas     string // aclaraciones para la ronda extra de preguntas
	// OnCommit avisa antes de empezar a auditar cada commit pendiente (idx
	// desde 0, total = len(pendientes)): sin esto, el progreso de
	// OnDimension no permite saber a qué commit pertenece cada dimensión
	// que arranca, porque AnalizarRama audita varios commits en la misma
	// pasada y el propio motor no conoce el contexto de "rama" (deuda
	// documentada al cerrar F1).
	OnCommit    func(idx, total int, sha string)
	OnDimension func(dim string)
	Fabrica     FabricaAuditor
	Parallel    int
	// Store es opcional (nil-safe): si no es nil, AnalizarRama consulta por
	// blob antes que por SHA para decidir pendientes (T2.7, criterio de
	// salida de F2: un rebase que no altera contenido conserva el 100% de
	// los findings) y registra los blobs de cada commit que audite. Sin
	// Store, el comportamiento es el de antes de T2.7: solo el ledger v1.
	Store StoreBlobs
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
		if opts.Store != nil {
			cubierto, shaOrigen, err := commitCubiertoPorBlobs(opts.Store, sha)
			if err != nil {
				return nil, err
			}
			if cubierto {
				// El contenido de este commit ya se revisó bajo otro SHA
				// (rebase típico): no hace falta volver a auditarlo, pero
				// hay que ADOPTAR su ficha bajo el SHA nuevo. Si solo
				// hiciéramos continue, ledger.LeerFicha(sha) devolvería nil
				// más abajo y los hallazgos reales de la ficha vieja
				// (huérfana) desaparecerían de res.Fichas — justo lo que el
				// criterio de salida de F2 prohíbe. Un fallo aquí se
				// propaga: no hay forma segura de perderlo en silencio sin
				// perder también el hallazgo.
				if err := ledger.AdoptarFicha(shaOrigen, sha); err != nil {
					return nil, err
				}
				continue
			}
		}
		ficha, err := ledger.LeerFicha(sha)
		if err != nil {
			return nil, err
		}
		if ficha == nil {
			pendientes = append(pendientes, sha)
		}
	}

	if !opts.SoloPendientes {
		for idx, sha := range pendientes {
			if opts.OnCommit != nil {
				opts.OnCommit(idx, len(pendientes), sha)
			}
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

	profile, err := change.PerfilDeCommit(sha)
	if err != nil {
		return err
	}
	resultado := AuditarCommit(opts.Fabrica, opts.Parallel, OpcionesAuditoria{
		SHA:            sha,
		Mensaje:        mensaje,
		Diff:           diff,
		Bundles:        PlanForProfile(profile, archivos).Bundles,
		Respuestas:     opts.Respuestas,
		PerfilOverride: opts.PerfilOverride,
		RutasContexto:  archivos,
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
	if err := ledger.GuardarRevision(sha, mensaje, "pr", modelo, revision); err != nil {
		return err
	}

	if opts.Store != nil {
		// Registra los blobs de este commit para que un rebase futuro pueda
		// reconocerlos vía SHAsDeBlob/YaRevisado (T2.7). No se inventan
		// hallazgos v2 a partir del veredicto v1 (regla de oro: nunca
		// fabricar evidencia): el IndiceCommit queda con Fingerprints vacío
		// y solo Blobs poblado, que ya basta para que YaRevisado funcione
		// ("revisado sin hallazgos" es lo esperado mientras el agente siga
		// emitiendo v1, hasta F5).
		//
		// Un fallo aquí NO es fatal para esta auditoría: la ficha ya se
		// guardó arriba con GuardarRevision, que es lo único que importa
		// ahora mismo. Registrar blobs es solo una optimización de
		// reutilización futura (T2.7 fix); abortar AnalizarRama por esto
		// tiraría un resultado real ya persistido. Se avisa por stderr en
		// vez de propagar el error, para no perder la señal en silencio sin
		// hacerla fatal.
		blobs, err := blobsDeArchivos(sha, archivos)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vas-sentinel: no se pudieron resolver los blobs de %s, se continúa sin registrar (optimización de reutilización futura, no afecta esta auditoría): %v\n", sha, err)
			return nil
		}
		if err := opts.Store.RegistrarBlobsCommit(sha, blobs); err != nil {
			fmt.Fprintf(os.Stderr, "vas-sentinel: no se pudieron registrar los blobs de %s en el store, se continúa sin registrar (optimización de reutilización futura, no afecta esta auditoría): %v\n", sha, err)
		}
	}
	return nil
}

// blobsDeArchivos resuelve el blob de cada archivo de un commit (archivo →
// blob), para registrarlo en el store o consultarlo antes de auditar (T2.7).
// Un commit sin archivos (caso degenerado) devuelve un mapa nil, no vacío:
// así el llamador distingue "no hay nada que registrar" sin necesitar un
// chequeo de longitud aparte.
func blobsDeArchivos(sha string, archivos []string) (map[string]string, error) {
	if len(archivos) == 0 {
		return nil, nil
	}
	blobs := make(map[string]string, len(archivos))
	for _, archivo := range archivos {
		blob, err := git.BlobDeArchivoEnCommit(sha, archivo)
		if err != nil {
			return nil, err
		}
		blobs[archivo] = blob
	}
	return blobs, nil
}

// commitCubiertoPorBlobs indica si el contenido de sha coincide EXACTAMENTE
// con el de UN ÚNICO commit anterior: calcula la intersección de los SHAs
// que registraron cada blob de sha (SHAsDeBlob, no YaRevisado — YaRevisado
// solo dice "algún SHA cubre este blob", perdiendo de vista si es el MISMO
// SHA para todos) y, si la intersección no está vacía, cubierto=true y
// shaOrigen es uno cualquiera de esos candidatos: todos registraron el
// conjunto completo de blobs de sha, así que adoptar la ficha de cualquiera
// es igual de seguro (elección arbitraria entre candidatos válidos, no hay
// un "mejor"). Es el caso típico de un rebase: el commit se reescribe sin
// tocar su contenido.
//
// Si algún blob no tiene NINGÚN SHA registrado, o si la intersección se
// vacía en cualquier punto, sha NO se considera cubierto: sus archivos
// vendrían de una MEZCLA de commits previos distintos (p. ej. un squash), y
// ahí no hay una única ficha previa que adoptar sin inventar contenido.
// Perder la reutilización es preferible a perder hallazgos reales. Un
// commit sin archivos nunca se considera cubierto (nada que reutilizar).
func commitCubiertoPorBlobs(s StoreBlobs, sha string) (cubierto bool, shaOrigen string, err error) {
	archivos, err := git.ArchivosDeCommit(sha)
	if err != nil {
		return false, "", err
	}
	blobs, err := blobsDeArchivos(sha, archivos)
	if err != nil {
		return false, "", err
	}
	if len(blobs) == 0 {
		return false, "", nil
	}

	var candidatos map[string]bool
	for _, blob := range blobs {
		shas, err := s.SHAsDeBlob(blob)
		if err != nil {
			return false, "", err
		}
		if len(shas) == 0 {
			return false, "", nil
		}
		vistos := make(map[string]bool, len(shas))
		for _, shaBlob := range shas {
			vistos[shaBlob] = true
		}
		if candidatos == nil {
			candidatos = vistos
			continue
		}
		interseccion := make(map[string]bool, len(candidatos))
		for shaBlob := range candidatos {
			if vistos[shaBlob] {
				interseccion[shaBlob] = true
			}
		}
		candidatos = interseccion
		if len(candidatos) == 0 {
			return false, "", nil
		}
	}
	for shaBlob := range candidatos {
		return true, shaBlob, nil
	}
	return false, "", nil
}

// overviewDeRama ejecuta la llamada Spec de rama (una sola, no una por
// commit). El error se propaga para que un fallo del overview no decida en
// silencio: el análisis sigue (con decisión chain como fallback seguro) pero
// el caller conoce la causa exacta.
func overviewDeRama(opts OpcionesRama, rama string, fichas []Ficha) (*ResultadoOverview, error) {
	if opts.Fabrica == nil {
		return nil, ErrSinFabrica
	}
	agente, _, err := opts.Fabrica(ReviewBundle{Name: "overview", Dimensions: []string{DimSpec}}, DimSpec)
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
			resultado := *rd.Resultado
			resultado.Bundle = rd.Bundle
			resultados = append(resultados, resultado)
		}
	}
	return resultados
}
