// Package store implementa el almacenamiento persistente de VAS Sentinel
// para T2.5 (F2): units, runs, findings, commits y decisions, anclados en el
// git-common-dir del repositorio (compartido entre worktrees enlazados desde
// el primer día). Convive con internal/review.Ledger sin sustituirlo: la
// migración de datos existentes es tarea de T2.6, este paquete nace vacío.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	subdirUnits    = "units"
	subdirRuns     = "runs"
	subdirFindings = "findings"
	subdirCommits  = "commits"
)

// Store da acceso al almacenamiento por id dentro de <gitCommonDir>/vas-sentinel.
type Store struct {
	dir string
}

// NuevoStore crea un store anclado a <gitCommonDir>/vas-sentinel. El llamador
// es responsable de pasar el resultado de git.ObtenerGitCommonDir (no
// ObtenerGitDir) para que el store se comparta entre worktrees enlazados; el
// store en sí no impone esa elección, igual que review.NuevoLedger tampoco lo
// hace. No crea el directorio: eso ocurre en la primera escritura.
func NuevoStore(gitCommonDir string) *Store {
	return &Store{dir: filepath.Join(gitCommonDir, "vas-sentinel")}
}

// rutaDecisiones devuelve la ruta del archivo append-only de decisiones.
func (s *Store) rutaDecisiones() string {
	return filepath.Join(s.dir, "decisions.jsonl")
}

// guardarJSON persiste v como <dir>/<subdir>/<id>.json con escritura atómica
// temp + rename: mismo patrón que review.Ledger.guardarFicha (probado en
// T2.x), reutilizado aquí para los cuatro tipos con clave por id (units,
// runs, findings, commits). En Windows el destino existente se borra antes
// del rename porque el sistema no permite sobrescribir con os.Rename.
func (s *Store) guardarJSON(subdir, id string, v any) error {
	dir := filepath.Join(s.dir, subdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	datos, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	destino := filepath.Join(dir, id+".json")
	temp, err := os.CreateTemp(dir, "tmp-*.tmp")
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

// leerJSON lee <dir>/<subdir>/<id>.json en v. Devuelve (false, nil) si el
// archivo no existe todavía; un archivo corrupto es un error explícito, nunca
// un nil silencioso (mismo criterio que review.Ledger.LeerFicha).
func (s *Store) leerJSON(subdir, id string, v any) (bool, error) {
	ruta := filepath.Join(s.dir, subdir, id+".json")
	datos, err := os.ReadFile(ruta)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(datos, v); err != nil {
		return false, err
	}
	return true, nil
}
