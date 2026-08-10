package store

import "errors"

// IndiceCommit es el índice de trazabilidad de commits/<sha>.json:
// deliberadamente ligero, solo qué fingerprints de review.Hallazgo tocan a
// ese SHA (el contenido completo de cada hallazgo vive en
// findings/<fingerprint>.json). Message queda como contexto opcional para no
// tener que releer el commit de git al inspeccionar el índice. V1 (T2.6) es
// opcional y solo se rellena cuando el índice viene de migrar una ficha v1
// (review.Ficha, ledger pre-T2.6): ver CompatV1 en migracion.go.
type IndiceCommit struct {
	SHA          string    `json:"sha"`
	Message      string    `json:"message,omitempty"`
	Fingerprints []string  `json:"fingerprints"`
	V1           *CompatV1 `json:"v1_compat,omitempty"`
}

// GuardarIndiceCommit persiste idx en commits/<sha>.json.
func (s *Store) GuardarIndiceCommit(idx *IndiceCommit) error {
	if idx.SHA == "" {
		return errors.New("store: indice de commit sin sha")
	}
	return s.guardarJSON(subdirCommits, idx.SHA, idx)
}

// LeerIndiceCommit devuelve el índice de ese SHA, o nil si no existe todavía.
func (s *Store) LeerIndiceCommit(sha string) (*IndiceCommit, error) {
	var idx IndiceCommit
	ok, err := s.leerJSON(subdirCommits, sha, &idx)
	if err != nil || !ok {
		return nil, err
	}
	return &idx, nil
}
