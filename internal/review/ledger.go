package review

import (
	"encoding/json"
	"errors"
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
