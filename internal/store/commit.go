package store

import "errors"

// IndiceCommit es el índice de trazabilidad de commits/<sha>.json:
// deliberadamente ligero, solo qué fingerprints de review.Hallazgo tocan a
// ese SHA (el contenido completo de cada hallazgo vive en
// findings/<fingerprint>.json). Message queda como contexto opcional para no
// tener que releer el commit de git al inspeccionar el índice. Blobs (T2.7)
// es opcional y mapea archivo→blob de ese commit: es la base del índice
// invertido blobs/<blob>.json (ver blob.go) que permite reconocer, tras un
// rebase que cambia el SHA sin tocar contenido, que un archivo ya se revisó.
// Un índice guardado antes de T2.7 deserializa con Blobs nil (campo ausente
// en JSON → mapa nil, comportamiento normal de Go), sin romper nada.
type IndiceCommit struct {
	SHA          string            `json:"sha"`
	Message      string            `json:"message,omitempty"`
	Fingerprints []string          `json:"fingerprints"`
	Blobs        map[string]string `json:"blobs,omitempty"`
}

// GuardarIndiceCommit persiste idx en commits/<sha>.json y, si idx.Blobs no
// está vacío, actualiza también el índice invertido blobs/<blob>.json de
// cada blob que declare (ver registrarBlobs en blob.go): sin esto, YaRevisado
// nunca tendría datos con los que responder.
func (s *Store) GuardarIndiceCommit(idx *IndiceCommit) error {
	if idx.SHA == "" {
		return errors.New("store: indice de commit sin sha")
	}
	if err := s.guardarJSON(subdirCommits, idx.SHA, idx); err != nil {
		return err
	}
	return s.registrarBlobs(idx)
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

// RegistrarBlobsCommit guarda (o amplía) el IndiceCommit de sha con los
// blobs de sus archivos, preservando los Fingerprints que ya tuviera: sin
// este merge, una segunda llamada sobre el mismo sha (por ejemplo, reintentar
// un análisis de rama) podría pisar hallazgos ya registrados. Pensada para el
// caller que audita un commit y solo conoce sus blobs, no fingerprints v2
// (T2.7: AnalizarRama sigue emitiendo v1 hasta F5).
//
// blobs se FUSIONA dentro de idx.Blobs (archivo por archivo), no lo
// sustituye: una llamada anterior sobre el mismo sha con un conjunto de
// blobs distinto (hoy AnalizarRama solo llama una vez con el conjunto
// completo, pero el contrato debe ser seguro también si eso cambia) dejaría,
// con una sustitución del mapa completo, sus entradas huérfanas en
// blobs/<blob>.json sin que idx.Blobs volviera a apuntarlas.
func (s *Store) RegistrarBlobsCommit(sha string, blobs map[string]string) error {
	idx, err := s.LeerIndiceCommit(sha)
	if err != nil {
		return err
	}
	if idx == nil {
		idx = &IndiceCommit{SHA: sha}
	}
	if idx.Blobs == nil {
		idx.Blobs = make(map[string]string, len(blobs))
	}
	for archivo, blob := range blobs {
		idx.Blobs[archivo] = blob
	}
	return s.GuardarIndiceCommit(idx)
}
