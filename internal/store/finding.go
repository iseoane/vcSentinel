package store

import (
	"errors"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// GuardarHallazgo persiste h en findings/<fingerprint>.json. La clave es el
// Fingerprint (calculado en T2.3), no un id secuencial: dos ejecuciones que
// producen el mismo hallazgo resuelven al mismo archivo sin coordinación.
func (s *Store) GuardarHallazgo(h *review.Hallazgo) error {
	if h.Fingerprint == "" {
		return errors.New("store: hallazgo sin fingerprint")
	}
	return s.guardarJSON(subdirFindings, h.Fingerprint, h)
}

// LeerHallazgo devuelve el hallazgo con ese fingerprint, o nil si no existe
// todavía. Un archivo corrupto es un error explícito.
func (s *Store) LeerHallazgo(fingerprint string) (*review.Hallazgo, error) {
	var h review.Hallazgo
	ok, err := s.leerJSON(subdirFindings, fingerprint, &h)
	if err != nil || !ok {
		return nil, err
	}
	return &h, nil
}
