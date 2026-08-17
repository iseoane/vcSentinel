package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
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

// hallazgoDesdeReviewFinding projects a legacy v1 ReviewFinding onto the v2
// Hallazgo shape so HallazgosEfectivos can return one uniform type
// regardless of origin. dimension comes from the containing DimensionResult
// (dr.Dim), not the finding itself, matching findingCrudo.aHallazgo's own
// convention (finding.go) for the same v1-to-v2 projection.
//
// Source is deliberately dropped, never copied from h.Source: a v1
// ReviewFinding can have it populated (e.g. SourceReview, stamped by the
// T5.7 critical-refutation path at engine.go:305) without ever having had a
// real Confidence — v1 has no such field. Copying it through would make the
// converted Hallazgo pass renderMergedFinding's "Source != \"\"" gate and
// render a fabricated "(review, confidence 0.00)", exactly the datum that
// gate exists to avoid (T6.5bis review finding: logic WARNING).
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
// públicamente. Vive junto a hallazgoDesdeReviewFinding (su inversa) en vez
// de en el renderer: ambas direcciones de la pareja v1↔v2 son una regla de
// mapeo del dominio, no del renderizado, y mantenerlas juntas evita que un
// campo nuevo en Hallazgo/ReviewFinding obligue a tocar dos archivos con
// riesgo de divergencia silenciosa (T6.5bis review finding: design WARNING).
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
func (l *Ledger) ListarFichas() ([]string, error) {
	coincidencias, err := filepath.Glob(filepath.Join(l.dir, "*.json"))
	if err != nil {
		return nil, err
	}
	shas := make([]string, 0, len(coincidencias))
	for _, ruta := range coincidencias {
		shas = append(shas, strings.TrimSuffix(filepath.Base(ruta), ".json"))
	}
	sort.Strings(shas)
	return shas, nil
}

// GuardarRevision añade una revisión a la ficha del SHA (creándola si es la
// primera) y la persiste con escritura atómica temp + rename. En Windows el
// destino existente se borra antes del rename porque el sistema no permite
// sobrescribir con os.Rename.
func (l *Ledger) GuardarRevision(sha, mensaje, bucket, modelo string, revision Revision) error {
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
	ficha, err := l.LeerFicha(sha)
	if err != nil {
		return err
	}
	if ficha == nil || ficha.FixedIn != "" {
		return nil
	}
	ficha.FixedIn = fixedIn
	return l.guardarFicha(ficha)
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
func (l *Ledger) PurgarHuerfanas() ([]string, error) {
	shas, err := l.ListarFichas()
	if err != nil {
		return nil, err
	}
	eliminados := []string{}
	for _, sha := range shas {
		if git.ContenidoEnAlgunRef(sha) {
			continue
		}
		if err := l.EliminarFicha(sha); err != nil {
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
	if err := os.Remove(destino); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(rutaTemp, destino)
}
