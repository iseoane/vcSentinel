package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Revision es una auditoría concreta de un SHA. El array revisions[] es
// append-only: re-auditar el mismo SHA añade una revisión, nunca pisa la
// anterior. El veredicto mostrado es el de la última revisión. Fixed indica
// que esta revisión salió sin críticos cuando la anterior estaba en block.
// Agent, Model y Effort identifican al agente que REALMENTE atendió la
// revisión, no el perfil que se le pidió (H4/T0.2): con `active_agent: auto`
// la cadena puede caer a otro binario y el veredicto quedaría sin autor. Son
// opcionales: una ficha v1 escrita antes de T0.2 no los trae y se sigue
// leyendo sin error.
type Revision struct {
	At     time.Time         `json:"at"`
	Result string            `json:"result"`
	Fixed  bool              `json:"fixed,omitempty"`
	Agent  string            `json:"agent,omitempty"`
	Model  string            `json:"model,omitempty"`
	Effort string            `json:"effort,omitempty"`
	Dims   []DimensionResult `json:"dims"`
	// AggregatedFindings is the deduplicated, cross-dimension merged, and
	// supersede-applied result review.AuditarCommit already computes
	// (ResultadoAuditoria.Findings, T6.1+T6.2). It is persisted here, separate
	// from Dims, so the renderer (T6.5) can show fused evidence and Source
	// per finding instead of only the raw per-dimension findings in Dims.
	// Empty for a Revision saved before this field existed, or when the
	// caller never propagated it: consumers must fall back to Dims.
	AggregatedFindings []Hallazgo `json:"aggregated_findings,omitempty"`
}

// HallazgosEfectivos returns the effective findings for this revision: the
// deduplicated, cross-dimension merged and supersede-applied result
// (AggregatedFindings, T6.1+T6.2) when the caller propagated it, or every
// raw per-dimension v1 ReviewFinding from Dims converted to the v2 Hallazgo
// shape otherwise. It is the single selection point riesgos() and
// BloqueantesDeRama (internal/review/renderer.go) both consume (T6.5 review
// finding: before this method existed, BloqueantesDeRama read only Dims and
// could still block on a semantic finding T6.2 had already superseded by a
// deterministic one, defeating the point of supersede in the one flow where
// it gates pr create --force).
func (r Revision) HallazgosEfectivos() []Hallazgo {
	if len(r.AggregatedFindings) > 0 {
		return r.AggregatedFindings
	}
	var hallazgos []Hallazgo
	for _, dr := range r.Dims {
		for _, h := range dr.Findings {
			hallazgos = append(hallazgos, hallazgoDesdeReviewFinding(dr.Dim, h))
		}
	}
	return hallazgos
}

// FindingsWithDispositions returns the effective findings of this revision
// carrying the lifecycle disposition each one actually recorded. It exists
// because the two persisted finding shapes hold complementary halves of the
// same evidence: refutarHallazgosCriticosConEvidencia (engine.go) writes
// Status onto the raw per-dimension v1 ReviewFinding, while aggregateFindings
// (aggregation.go) persists the merged v2 set with an empty Status and skips
// the refuted findings outright. A consumer reading either shape alone sees a
// disposition-free ledger (FU-7).
//
// It is deliberately NOT what HallazgosEfectivos returns, and never replaces
// it: that method is the single selection point riesgos() and
// BloqueantesDeRama consume, so changing what it returns would change branch
// blocking and pr create --force. This one is for observation only. Like the
// hallazgoDesdeReviewFinding/reviewFindingDesdeHallazgo pair it lives beside,
// the v1-to-v2 correspondence it applies is a rule of the domain, not of the
// consumer that happens to need it today.
//
// The join is by dimension, file, start line and description, the same key
// refutarHallazgoV2 (engine.go) already uses to find a v1 finding's v2
// counterpart. Fingerprints cannot serve: aggregation recomputes them, and
// the v1 shape has neither Evidence nor Title, the two components Fingerprint
// hashes besides dimension and location. Merging is honoured rather than
// guessed at: mergeFindings keeps one whole contributor as the representative
// of a merged group, so exactly that contributor matches; the siblings it
// absorbed stay unknown instead of lending their disposition to a finding
// that may no longer be theirs.
//
// Only refuted raw findings with no counterpart are appended, never every
// undisposed one. Aggregation drops precisely the refuted findings, so this
// restores exactly what it removed. SupersedeDeterministicFindings
// (supersede.go) also drops semantic findings, silently and without marking
// them, so a broader rule would resurrect superseded findings as live
// observations.
func (r Revision) FindingsWithDispositions() []Hallazgo {
	if len(r.AggregatedFindings) == 0 {
		var findings []Hallazgo
		for _, dr := range r.Dims {
			for _, h := range dr.Findings {
				findings = append(findings, findingWithDisposition(dr.Dim, h))
			}
		}
		return findings
	}

	dispositions := rawDispositions(r.Dims)
	findings := make([]Hallazgo, len(r.AggregatedFindings))
	copy(findings, r.AggregatedFindings)
	counterparts := make(map[string]int, len(findings))
	for i := range findings {
		counterparts[dispositionKey(findings[i].Dimension, findings[i].Location.Archivo, findings[i].Location.LineaInicio, findings[i].Description)]++
	}
	for i := range findings {
		key := dispositionKey(findings[i].Dimension, findings[i].Location.Archivo, findings[i].Location.LineaInicio, findings[i].Description)
		// Canonicalise the value, do not merely test it in canonical form:
		// normalizing the check and discarding the result would return the
		// aggregate's status exactly as persisted while every raw-path
		// finding comes back canonical, so one observation API would speak
		// two status representations and a consumer comparing against
		// StatusRefuted would miss a padded refutation.
		findings[i].Status = NormalizeStatus(findings[i].Status)
		// A status the aggregated finding recorded itself is evidence, not an
		// absence: it wins over the raw one rather than being overwritten.
		if findings[i].Status != "" {
			continue
		}
		// Ambiguity is refused rather than guessed at. The key omits Evidence
		// and Title because the v1 shape carries neither, so two aggregates
		// can legitimately share it; attributing one raw disposition to both
		// would mark a finding nobody disposed of, and a security finding
		// carrying a refutation nobody issued reads as dismissed. Under-report
		// instead, exactly as contradictory raw statuses are under-reported.
		if counterparts[key] != 1 {
			continue
		}
		if status, ok := dispositions[key]; ok {
			findings[i].Status = status
		}
	}
	for _, dr := range r.Dims {
		for _, h := range dr.Findings {
			if NormalizeStatus(h.Status) != StatusRefuted {
				continue
			}
			// Keyed on counterpart presence, not on whether the status was
			// adopted: a raw finding whose aggregate kept its own status, or
			// whose key matched several aggregates, is already represented
			// among them and must not be observed a second time.
			if counterparts[dispositionKey(dr.Dim, h.File, int(h.Line), h.Description)] > 0 {
				continue
			}
			findings = append(findings, findingWithDisposition(dr.Dim, h))
		}
	}
	return findings
}

// rawDispositions indexes the dispositions the raw per-dimension findings
// recorded, by the same key FindingsWithDispositions joins on. A key whose
// findings disagree records no disposition at all: contradictory evidence is
// not evidence, and picking a winner would invent a lifecycle answer the
// ledger never gave. Dropping it keeps the result deterministic without
// needing a precedence order nothing in the domain authorises.
func rawDispositions(dims []DimensionResult) map[string]string {
	statusesByKey := make(map[string]map[string]struct{})
	for _, dr := range dims {
		for _, h := range dr.Findings {
			status := NormalizeStatus(h.Status)
			if status == "" {
				continue
			}
			key := dispositionKey(dr.Dim, h.File, int(h.Line), h.Description)
			if statusesByKey[key] == nil {
				statusesByKey[key] = make(map[string]struct{}, 1)
			}
			statusesByKey[key][status] = struct{}{}
		}
	}
	dispositions := make(map[string]string, len(statusesByKey))
	for key, statuses := range statusesByKey {
		if len(statuses) != 1 {
			continue
		}
		for status := range statuses {
			dispositions[key] = status
		}
	}
	return dispositions
}

func dispositionKey(dimension, file string, line int, description string) string {
	return empaquetarConLongitud(dimension, file, strconv.Itoa(line), description)
}

// findingWithDisposition projects a v1 finding exactly like
// hallazgoDesdeReviewFinding and then restores the Status that projection
// drops. The drop is correct there: HallazgosEfectivos feeds the blocking
// gate, which selects on severity and supersede rather than on lifecycle.
// Here the lifecycle is the whole point.
func findingWithDisposition(dimension string, h ReviewFinding) Hallazgo {
	hallazgo := hallazgoDesdeReviewFinding(dimension, h)
	hallazgo.Status = NormalizeStatus(h.Status)
	return hallazgo
}

// hallazgoDesdeReviewFinding projects a legacy v1 ReviewFinding onto the v2
// Hallazgo shape so HallazgosEfectivos can return one uniform type
// regardless of origin. dimension comes from the containing DimensionResult
// (dr.Dim), not the finding itself, matching findingCrudo.aHallazgo's own
// convention (finding.go) for the same v1-to-v2 projection.
//
// Source is deliberately dropped, never copied from h.Source: v1 has no
// Confidence field, and Hallazgo treats Source and Confidence as a pair
// that must co-occur — a Source without its matching real Confidence is a
// fabricated datum, not an absent one. This makes the round-trip with
// reviewFindingDesdeHallazgo intentionally asymmetric for Source: it is
// dropped going v1-to-v2 here, but reviewFindingDesdeHallazgo still copies
// it going v2-to-v1, because a v2 Hallazgo built any other way always pairs
// Source with a real Confidence.
func hallazgoDesdeReviewFinding(dimension string, h ReviewFinding) Hallazgo {
	return Hallazgo{
		Dimension:   dimension,
		Severity:    h.Severity,
		Description: h.Description,
		Location:    Ubicacion{Archivo: h.File, LineaInicio: int(h.Line)},
	}
}

// reviewFindingDesdeHallazgo proyecta un Hallazgo v2 de vuelta a la forma v1
// ReviewFinding que BloqueantesDeRama (renderer.go) sigue devolviendo
// públicamente. Vive junto a hallazgoDesdeReviewFinding en vez de en el
// renderer: ambas direcciones de la pareja v1↔v2 son una regla de mapeo del
// dominio, no del renderizado, y mantenerlas juntas evita que un campo nuevo
// en Hallazgo/ReviewFinding obligue a tocar dos archivos con riesgo de
// divergencia silenciosa. No es la inversa exacta de hallazgoDesdeReviewFinding
// para Source: ver el comentario de esa función sobre por qué la asimetría
// es intencional, no un descuido.
func reviewFindingDesdeHallazgo(h Hallazgo) ReviewFinding {
	return ReviewFinding{
		Dimension:   h.Dimension,
		File:        h.Location.Archivo,
		Line:        Linea(h.Location.LineaInicio),
		Severity:    h.Severity,
		Description: h.Description,
		Source:      h.Source,
	}
}

// Ficha es el registro completo de auditoría de un commit, guardado como
// <git-dir>/vas-sentinel/<sha>.json. FixedIn es el SHA del commit que
// corrigió los hallazgos (se rellena cuando un fix re-audita los archivos).
type Ficha struct {
	SHA       string     `json:"sha"`
	Message   string     `json:"message"`
	Bucket    string     `json:"bucket"`
	Model     string     `json:"model"`
	FixedIn   string     `json:"fixed_in,omitempty"`
	Revisions []Revision `json:"revisions"`
}

// Ledger da acceso a las fichas por SHA dentro del common-dir de Git.
type Ledger struct {
	dir string
}

// NuevoLedger crea un ledger anclado a <gitDir>/vas-sentinel. No crea el
// directorio: eso ocurre en la primera escritura.
//
// It files fichas under whatever directory it is given and imposes no choice,
// exactly like store.NuevoStore. Callers that read or write review evidence
// must pass the Git common directory so linked worktrees share one ledger;
// passing a per-checkout gitDir files the record where `git worktree remove`
// destroys it. In cmd/sentinel that choice lives in sharedReviewLedger. The
// exception is `runs prune`, which enumerates every per-checkout ledger on
// purpose so no execution stream loses its provenance.
func NuevoLedger(gitDir string) *Ledger {
	return &Ledger{dir: filepath.Join(gitDir, "vas-sentinel")}
}

// RutaFicha devuelve la ruta del archivo de la ficha de un SHA.
func (l *Ledger) RutaFicha(sha string) string {
	return filepath.Join(l.dir, sha+".json")
}

// LeerFicha devuelve la ficha de un SHA o nil si aún no existe. Un archivo
// corrupto es un error explícito: el ledger nunca se borra en silencio.
func (l *Ledger) LeerFicha(sha string) (*Ficha, error) {
	datos, err := os.ReadFile(l.RutaFicha(sha))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ficha Ficha
	if err := json.Unmarshal(datos, &ficha); err != nil {
		return nil, err
	}
	return &ficha, nil
}

// ListarFichas devuelve los SHAs con ficha de auditoría guardada, en orden
// alfabético. No lee el contenido: para eso se usa LeerFicha por SHA.
//
// Enumerated with os.ReadDir and NOT with filepath.Glob. Glob reports only
// ErrBadPattern and swallows every I/O error it meets while reading a
// directory, so a static pattern over an unreadable ledger returned an empty
// list and a nil error. That is indistinguishable from a ledger holding no
// fichas, and it fails open in the callers whose whole contract is to fail
// closed: collectProvenanceReferences decides from this list which execution
// streams a prune may destroy, and PurgarHuerfanas decides which fichas it may
// delete (FU-16).
//
// Absence is separated from breakage with Lstat before the read. NuevoLedger
// does not create the directory — the first saved revision does — so a ledger
// nobody has written to yet genuinely holds no fichas. But os.ReadDir resolves
// symlinks, so it answers ErrNotExist for a ledger path pointing nowhere too,
// and only Lstat tells the two apart.
func (l *Ledger) ListarFichas() ([]string, error) {
	if _, err := os.Lstat(l.dir); errors.Is(err, os.ErrNotExist) {
		return []string{}, nil // no revision was ever saved in this checkout
	} else if err != nil {
		return nil, fmt.Errorf("checking the review ledger path %s: %w", l.dir, err)
	}
	entradas, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("enumerating the review ledger in %s: %w", l.dir, err)
	}
	shas := make([]string, 0, len(entradas))
	for _, entrada := range entradas {
		nombre := entrada.Name()
		if !strings.HasSuffix(nombre, ".json") {
			continue // temp files from guardarFicha, and anything else
		}
		shas = append(shas, strings.TrimSuffix(nombre, ".json"))
	}
	sort.Strings(shas)
	return shas, nil
}

// esperaMaximaBloqueoFicha bounds how long a writer waits for another
// process's lock. A review that cannot persist safely fails loudly: dropping a
// revision in silence is the exact failure this lock exists to prevent.
const esperaMaximaBloqueoFicha = 30 * time.Second

// intervaloReintentoBloqueoFicha is the poll interval while waiting. The
// critical section is one read, one marshal and one rename, so a short poll
// costs nothing and keeps an uncontended second writer from stalling.
const intervaloReintentoBloqueoFicha = 25 * time.Millisecond

// ErrBloqueoNoLiberado marks the one failure that must not be read as "the
// mutation did not happen": the protected operation SUCCEEDED and only its lock
// file could not be removed. A caller that records what it changed needs to
// tell the two apart, because a deletion reported as a failure leaves the ficha
// gone and its events pointing at it.
var ErrBloqueoNoLiberado = errors.New("the review ficha was written but its lock could not be released")

// borrarBloqueo libera el archivo de bloqueo. Es una variable, no una llamada
// directa, por la misma razón que leerAtributosGate en cmd/sentinel: la rama de
// fallo decide si un borrado ya hecho se reporta o se pierde, y no hay forma de
// provocarla desde la API pública porque ocurre dentro de la sección crítica.
var borrarBloqueo = os.Remove

// conFichaBloqueada ejecuta fn manteniendo un bloqueo exclusivo sobre la ficha
// de un SHA.
//
// The ledger became repository-wide when review, status and pr moved their
// anchor to the Git common directory, so two checkouts now audit into the same
// ficha file. Every mutation here is a read-append-rename cycle, and without
// serialization each writer reads the same ficha, appends its own revision and
// replaces the other: a clean result can erase a blocking one, and `review
// --all` then reads that SHA as audited so the lost verdict never resurfaces.
// Measured: eight concurrent writers left one revision of eight.
//
// The lock is a file created with O_EXCL, which is atomic on POSIX and on
// Windows. It is advisory and only between sentinel processes; nothing stops an
// editor from rewriting a ficha by hand. Its name ends in .lock and not .json,
// so ListarFichas never reports it as a SHA.
//
// A process killed inside the critical section leaves its lock behind, and the
// wait then fails naming the path so an operator can remove it. That is
// deliberate: stealing a lock after a timeout would guess that the holder is
// dead, and guessing wrong reintroduces the lost update this prevents.
func (l *Ledger) conFichaBloqueada(sha string, fn func() error) (err error) {
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		return err
	}
	ruta := l.RutaFicha(sha) + ".lock"
	limite := time.Now().Add(esperaMaximaBloqueoFicha)
	for {
		lock, errAbrir := os.OpenFile(ruta, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if errAbrir == nil {
			if errCerrar := lock.Close(); errCerrar != nil {
				os.Remove(ruta)
				return fmt.Errorf("locking the review ficha of %s: %w", sha, errCerrar)
			}
			// Released through defer so a panic inside fn cannot leak the lock,
			// and the removal failure is reported rather than discarded: a lock
			// left behind makes every later writer of this SHA wait the full
			// timeout and then fail, which is not something to learn later.
			defer func() {
				// An already-absent lock is a released lock: someone removed it
				// out of protocol, which is not this call's failure.
				errBorrar := borrarBloqueo(ruta)
				if errBorrar == nil || errors.Is(errBorrar, os.ErrNotExist) || err != nil {
					return
				}
				err = fmt.Errorf("%w (%s of %s): %v", ErrBloqueoNoLiberado, ruta, sha, errBorrar)
			}()
			return fn()
		}
		if !errors.Is(errAbrir, os.ErrExist) {
			return fmt.Errorf("locking the review ficha of %s: %w", sha, errAbrir)
		}
		if time.Now().After(limite) {
			return fmt.Errorf("the review ficha of %s stayed locked for %s; if no sentinel process is running, delete %s",
				sha, esperaMaximaBloqueoFicha, ruta)
		}
		time.Sleep(intervaloReintentoBloqueoFicha)
	}
}

// GuardarRevision añade una revisión a la ficha del SHA (creándola si es la
// primera) y la persiste con escritura atómica temp + rename. En Windows el
// destino existente se borra antes del rename porque el sistema no permite
// sobrescribir con os.Rename.
//
// The whole read-append-write runs under the ficha's lock: revisions[] is
// append-only, and two unserialized writers make that a claim rather than a
// fact.
func (l *Ledger) GuardarRevision(sha, mensaje, bucket, modelo string, revision Revision) error {
	return l.conFichaBloqueada(sha, func() error { return l.guardarRevisionBloqueada(sha, mensaje, bucket, modelo, revision) })
}

func (l *Ledger) guardarRevisionBloqueada(sha, mensaje, bucket, modelo string, revision Revision) error {
	ficha, err := l.LeerFicha(sha)
	if err != nil {
		return err
	}
	if ficha == nil {
		ficha = &Ficha{SHA: sha, Message: mensaje, Bucket: bucket, Model: modelo}
	}
	if ficha.Message == "" {
		ficha.Message = mensaje
	}
	if ficha.Bucket == "" {
		ficha.Bucket = bucket
	}
	if ficha.Model == "" {
		ficha.Model = modelo
	}
	ficha.Revisions = append(ficha.Revisions, revision)
	return l.guardarFicha(ficha)
}

// MarcarCorregida registra que los hallazgos del SHA fueron corregidos por el
// commit fixedIn (trazabilidad hallazgo → corrección). No sobreescribe un
// FixedIn ya existente: la primera corrección gana.
func (l *Ledger) MarcarCorregida(sha, fixedIn string) error {
	// Checked on the DIRECTORY, not on the ficha. The goal is only to keep a
	// ledger that has never stored anything from having its directory created
	// by a call that is a documented no-op, and this call runs for every earlier
	// commit of every fix( commit. Testing the ficha itself instead would be a
	// correctness bug: guardarFicha deletes the destination before renaming over
	// it, so an unlocked existence check lands in that window and reports a live
	// ficha as absent — measured, it silently dropped the correction.
	if _, err := os.Stat(l.dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// Under the ficha's lock: "the first correction wins" is decided by reading
	// FixedIn and then writing it, so two unserialized callers can both read it
	// empty and the second one wins instead.
	return l.conFichaBloqueada(sha, func() error {
		ficha, err := l.LeerFicha(sha)
		if err != nil {
			return err
		}
		if ficha == nil || ficha.FixedIn != "" {
			return nil
		}
		ficha.FixedIn = fixedIn
		return l.guardarFicha(ficha)
	})
}

// AdoptarFicha copia la ficha de desde bajo el SHA hacia. Es el fix de T2.7
// para commitCubiertoPorBlobs: cuando un rebase reescribe un commit sin
// tocar su contenido, el commit se cubre por blob bajo un SHA anterior, pero
// si AnalizarRama solo hiciera "continue" sin escribir nada bajo el SHA
// nuevo, ledger.LeerFicha(hacia) devolvería nil y los hallazgos reales de la
// ficha vieja (huérfana, bajo un SHA que ya no existe en la rama)
// desaparecerían de res.Fichas. Adoptar la ficha bajo el SHA nuevo es lo que
// los mantiene recuperables.
//
// desde debería existir siempre que la llame commitCubiertoPorBlobs, porque
// salió del store, que solo registra commits ya auditados: por eso, a
// diferencia de MarcarCorregida (donde "no hay nada que marcar" es un
// estado válido y se resuelve como no-op), que desde no tenga ficha aquí es
// un error real, no algo que ignorar en silencio.
//
// Idempotente: llamarlo dos veces con los mismos argumentos sobrescribe con
// el mismo contenido, sin duplicar nada. Revisions se copia a un slice
// nuevo (no se comparte el subyacente de la ficha origen) por higiene de
// aliasing, no porque Revision se mute después de guardarse.
func (l *Ledger) AdoptarFicha(desde, hacia string) error {
	// Locked on hacia, which is the ficha this writes. Locking desde too would
	// be a second lock in a fixed-order pair and buys nothing: the source is
	// only read, and a concurrent append to it either lands in the copy or does
	// not, whereas an unserialized adoption can overwrite a revision another
	// writer just appended to the destination.
	return l.conFichaBloqueada(hacia, func() error {
		origen, err := l.LeerFicha(desde)
		if err != nil {
			return err
		}
		if origen == nil {
			return fmt.Errorf("ledger: no hay ficha en %s para adoptar hacia %s", desde, hacia)
		}
		revisiones := make([]Revision, len(origen.Revisions))
		copy(revisiones, origen.Revisions)
		adoptada := &Ficha{
			SHA:       hacia,
			Message:   origen.Message,
			Bucket:    origen.Bucket,
			Model:     origen.Model,
			FixedIn:   origen.FixedIn,
			Revisions: revisiones,
		}
		return l.guardarFicha(adoptada)
	})
}

// EliminarFicha borra la ficha de un SHA. No devuelve error si no existe:
// eliminar algo que ya no está es un no-op.
func (l *Ledger) EliminarFicha(sha string) error {
	ruta := l.RutaFicha(sha)
	if err := os.Remove(ruta); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// PurgarHuerfanas borra las fichas cuyo SHA ya no es alcanzable desde ningún
// ref (commits reescritos por rebase, amend o squash: siguen en el object
// store como dangling, pero ContenidoEnAlgunRef los detecta). Devuelve la
// lista de SHAs eliminados; si algo falla a mitad, devuelve el error junto
// con los SHAs que sí llegó a eliminar. El caso del commit reescrito por
// amend queda cubierto por TestLedgerPurgarHuerfanasDangling.

// El criterio de existencia se inyecta y DEBE poder fallar. No hay variante sin
// él a propósito: la que había envolvía un ayudante que convertía cualquier
// error de git en "no está", así que el camino cómodo era justo el que borraba
// fichas vivas ante una consulta rota. Un llamador que purgue los ledgers de
// varios checkouts debe además anclar el criterio al repositorio correcto:
// clasificar con el directorio de trabajo del proceso convierte cada ficha viva
// de otro checkout en huérfana.
func (l *Ledger) PurgarHuerfanas(existe func(sha string) (bool, error)) ([]string, error) {
	shas, err := l.ListarFichas()
	if err != nil {
		return nil, err
	}
	eliminados := []string{}
	for _, sha := range shas {
		presente, err := existe(sha)
		if err != nil {
			// Abortar con lo ya borrado: preguntar y no poder responder nunca
			// autoriza un borrado.
			return eliminados, err
		}
		if presente {
			continue
		}
		// Deletion takes the same per-SHA lock as every mutation. Without it the
		// purge could remove a ficha while a locked GuardarRevision was writing
		// it, so a revision that reported success would simply not exist.
		if err := l.conFichaBloqueada(sha, func() error { return l.EliminarFicha(sha) }); err != nil {
			// ErrBloqueoNoLiberado means the ficha IS deleted and only the lock
			// survived. Recording it before aborting is what lets the caller
			// clean its events, which are purged from this very list.
			if errors.Is(err, ErrBloqueoNoLiberado) {
				eliminados = append(eliminados, sha)
			}
			return eliminados, err
		}
		eliminados = append(eliminados, sha)
	}
	return eliminados, nil
}

// guardarFicha persiste la ficha con escritura atómica temp + rename. En
// Windows el destino existente se borra antes del rename porque el sistema no
// permite sobrescribir con os.Rename.
func (l *Ledger) guardarFicha(ficha *Ficha) error {
	if err := os.MkdirAll(l.dir, 0755); err != nil {
		return err
	}
	datos, err := json.MarshalIndent(ficha, "", "  ")
	if err != nil {
		return err
	}

	destino := l.RutaFicha(ficha.SHA)
	temp, err := os.CreateTemp(l.dir, "ficha-*.tmp")
	if err != nil {
		return err
	}
	rutaTemp := temp.Name()
	defer os.Remove(rutaTemp)
	if _, err := temp.Write(datos); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// The destination is removed ONLY on Windows, which refuses to rename over
	// an existing file. Elsewhere rename replaces it atomically, and deleting
	// first opened a window in which the ficha did not exist at all: a
	// concurrent reader saw a live record as absent, which AnalizarRama reads
	// as "never audited". Narrowing that window to the platform that needs it
	// costs nothing.
	if runtime.GOOS == "windows" {
		if err := os.Remove(destino); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(rutaTemp, destino)
}
