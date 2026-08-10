package store

import "github.com/ISeoane-Quental/vas.sentinel/internal/review"

// subdirBlobs es el índice invertido blob→SHAs (T2.7): sin él, responder
// "¿se vio antes este blob?" exigiría escanear todos los commits/*.json uno
// por uno; con él, es una lectura directa por blob.
const subdirBlobs = "blobs"

// indiceBlob es el contenido de blobs/<blob>.json: qué SHAs de commit ya
// registraron ese blob de contenido (vía IndiceCommit.Blobs).
type indiceBlob struct {
	Blob string   `json:"blob"`
	SHAs []string `json:"shas"`
}

// registrarBlobs añade idx.SHA a blobs/<blob>.json para cada blob de
// idx.Blobs, sin duplicar si ese SHA ya estaba registrado para ese blob.
// Reutiliza guardarJSON (mismo patrón atómico temp+rename que el resto del
// store): no hace falta un mecanismo de escritura aparte.
func (s *Store) registrarBlobs(idx *IndiceCommit) error {
	for _, blob := range idx.Blobs {
		var ib indiceBlob
		ok, err := s.leerJSON(subdirBlobs, blob, &ib)
		if err != nil {
			return err
		}
		if !ok {
			ib = indiceBlob{Blob: blob}
		}
		yaRegistrado := false
		for _, sha := range ib.SHAs {
			if sha == idx.SHA {
				yaRegistrado = true
				break
			}
		}
		if !yaRegistrado {
			ib.SHAs = append(ib.SHAs, idx.SHA)
		}
		if err := s.guardarJSON(subdirBlobs, blob, &ib); err != nil {
			return err
		}
	}
	return nil
}

// YaRevisado indica si blob ya apareció en algún IndiceCommit guardado
// anteriormente (revisado=true), y devuelve los review.Hallazgo (v2) cuya
// Location.Blob coincide con él.
//
// Distinción deliberada, el punto donde un diseño ingenuo se equivocaría:
// "revisado sin hallazgos" (el blob apareció en un commit ya auditado, pero
// limpio: ningún fingerprint de ese commit apunta a él) es DISTINTO de
// "nunca revisado" (el blob no aparece en ningún IndiceCommit guardado). Si
// solo se mirara si hallazgos está vacío para decidir "hace falta auditar",
// un archivo limpio se re-auditaría siempre porque "sin hallazgos" se
// confundiría con "no se sabe". Por eso revisado es un booleano explícito,
// independiente de si hallazgos tiene elementos.
func (s *Store) YaRevisado(blob string) (revisado bool, hallazgos []review.Hallazgo, err error) {
	var ib indiceBlob
	ok, err := s.leerJSON(subdirBlobs, blob, &ib)
	if err != nil {
		return false, nil, err
	}
	if !ok || len(ib.SHAs) == 0 {
		return false, nil, nil
	}

	vistos := make(map[string]bool)
	for _, sha := range ib.SHAs {
		idx, err := s.LeerIndiceCommit(sha)
		if err != nil {
			return false, nil, err
		}
		if idx == nil {
			continue
		}
		for _, fp := range idx.Fingerprints {
			if vistos[fp] {
				continue
			}
			h, err := s.LeerHallazgo(fp)
			if err != nil {
				return false, nil, err
			}
			if h != nil && h.Location.Blob == blob {
				hallazgos = append(hallazgos, *h)
				vistos[fp] = true
			}
		}
	}
	return true, hallazgos, nil
}
