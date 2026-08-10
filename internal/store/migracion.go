package store

import (
	"path/filepath"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// CompatV1 guarda la información de una ficha v1 (review.Ficha, ledger
// pre-T2.6) migrada por MigrarDesdeV1 cuando el IndiceCommit se creó a partir
// de esa ficha en vez de un review v2 real.
//
// Decisión de diseño (T2.6): un review.ReviewFinding v1 no trae Evidence ni
// Confidence, así que construir un review.Hallazgo v2 con Fingerprint real a
// partir de él sería inventar evidencia que nunca existió (regla de oro:
// nunca fabricar evidencia). Por eso la migración NO crea entradas en
// findings/<fingerprint>.json para los ReviewFinding v1: se opta por
// conservar la ficha v1 completa aquí, tal cual, en vez de forzarla al
// modelo v2. Los hallazgos v2 reales (con Evidence y Fingerprint verdaderos)
// solo empiezan a poblarse desde ahora en adelante, cuando el agente los
// emita (F5).
type CompatV1 struct {
	Bucket    string            `json:"bucket,omitempty"`
	Model     string            `json:"model,omitempty"`
	FixedIn   string            `json:"fixed_in,omitempty"`
	Revisions []review.Revision `json:"revisions,omitempty"`
}

// MigracionCorrupto registra un <sha>.json v1 que no se pudo leer (JSON
// inválido). Nunca se borra ni se modifica: solo se reporta.
type MigracionCorrupto struct {
	Ruta  string
	Error string
}

// MigracionResumen resume el resultado de MigrarDesdeV1 para que el llamador
// (un subcomando futuro, fuera de esta tarea) pueda informarlo.
type MigracionResumen struct {
	Migrados   int
	YaMigrados int
	Corruptos  []MigracionCorrupto
}

// MigrarDesdeV1 traduce las fichas v1 (review.Ficha, internal/review.Ledger)
// del checkout principal y de todos los worktrees enlazados hacia el store
// compartido anclado en gitCommonDir. Es idempotente: un SHA con
// IndiceCommit ya existente en el store se cuenta como "ya migrado" y no se
// retoca. Nunca borra ni modifica los archivos v1 originales: son de solo
// lectura para esta función.
//
// El checkout principal ya guarda sus fichas v1 en el sitio compartido
// (<gitCommonDir>/vas-sentinel/*.json: su ObtenerGitDir coincide con
// ObtenerGitCommonDir), solo falta traducir el formato. Los worktrees
// enlazados guardan las suyas en <gitCommonDir>/worktrees/<nombre>/
// vas-sentinel/*.json (estructura que Git crea él mismo por cada worktree
// enlazado): un glob encuentra todas de una vez, sin invocar
// `git worktree list` ni parsear su salida.
func MigrarDesdeV1(gitCommonDir string) (MigracionResumen, error) {
	var resumen MigracionResumen
	s := NuevoStore(gitCommonDir)

	if err := migrarDirectorioV1(s, filepath.Join(gitCommonDir, "vas-sentinel"), &resumen); err != nil {
		return resumen, err
	}

	directoriosWorktrees, err := filepath.Glob(filepath.Join(gitCommonDir, "worktrees", "*", "vas-sentinel"))
	if err != nil {
		return resumen, err
	}
	for _, dir := range directoriosWorktrees {
		if err := migrarDirectorioV1(s, dir, &resumen); err != nil {
			return resumen, err
		}
	}
	return resumen, nil
}

// migrarDirectorioV1 traduce las fichas v1 del ledger anclado en
// filepath.Dir(dirVasSentinel) (el gitDir del checkout, ya sea principal o de
// un worktree enlazado) hacia el store s, acumulando resumen. Un archivo
// corrupto se registra en resumen.Corruptos y no aborta el resto: la
// migración de los demás SHAs válidos continúa.
func migrarDirectorioV1(s *Store, dirVasSentinel string, resumen *MigracionResumen) error {
	ledger := review.NuevoLedger(filepath.Dir(dirVasSentinel))
	shas, err := ledger.ListarFichas()
	if err != nil {
		return err
	}
	for _, sha := range shas {
		ficha, err := ledger.LeerFicha(sha)
		if err != nil {
			resumen.Corruptos = append(resumen.Corruptos, MigracionCorrupto{
				Ruta:  ledger.RutaFicha(sha),
				Error: err.Error(),
			})
			continue
		}
		if ficha == nil {
			// Borrada entre el listado y la lectura: no hay nada que migrar.
			continue
		}

		existente, err := s.LeerIndiceCommit(sha)
		if err != nil {
			return err
		}
		if existente != nil {
			resumen.YaMigrados++
			continue
		}

		idx := &IndiceCommit{
			SHA:     ficha.SHA,
			Message: ficha.Message,
			V1: &CompatV1{
				Bucket:    ficha.Bucket,
				Model:     ficha.Model,
				FixedIn:   ficha.FixedIn,
				Revisions: ficha.Revisions,
			},
		}
		if err := s.GuardarIndiceCommit(idx); err != nil {
			return err
		}
		resumen.Migrados++
	}
	return nil
}
