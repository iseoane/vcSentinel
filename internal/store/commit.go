package store

import "errors"

// IndiceCommit es el índice de trazabilidad de commits/<sha>.json:
// deliberadamente ligero, solo qué fingerprints de review.Hallazgo tocan a
// ese SHA (el contenido completo de cada hallazgo vive en
// findings/<fingerprint>.json). Message queda como contexto opcional para no
// tener que releer el commit de git al inspeccionar el índice.
type IndiceCommit struct {
	SHA          string   `json:"sha"`
	Message      string   `json:"message,omitempty"`
	Fingerprints []string `json:"fingerprints"`
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
