package review

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Revision es una auditoría concreta de un SHA. El array revisions[] es
// append-only: re-auditar el mismo SHA añade una revisión, nunca pisa la
// anterior. El veredicto mostrado es el de la última revisión.
type Revision struct {
	At     time.Time         `json:"at"`
	Result string            `json:"result"`
	Dims   []DimensionResult `json:"dims"`
}

// Ficha es el registro completo de auditoría de un commit, guardado como
// <git-dir>/vas-sentinel/<sha>.json.
type Ficha struct {
	SHA       string     `json:"sha"`
	Message   string     `json:"message"`
	Bucket    string     `json:"bucket"`
	Model     string     `json:"model"`
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

	if err := os.MkdirAll(l.dir, 0755); err != nil {
		return err
	}
	datos, err := json.MarshalIndent(ficha, "", "  ")
	if err != nil {
		return err
	}

	destino := l.RutaFicha(sha)
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
