package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

// ReferencedInvocationIDs returns every durable invocation identity recorded
// as review provenance across the persisted findings. Retention callers feed
// this set into PruneExecutions so a stream cited by any finding is kept.
// A findings file that fails to decode fails closed: retention decisions
// must never run while evidence is unreadable.
func (s *Store) ReferencedInvocationIDs() (map[string]bool, error) {
	references := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(s.dir, subdirFindings))
	if errors.Is(err, os.ErrNotExist) {
		return references, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, subdirFindings, entry.Name()))
		if err != nil {
			return nil, err
		}
		var provenance struct {
			InvocationID string `json:"invocation_id"`
		}
		if err := json.Unmarshal(data, &provenance); err != nil {
			return nil, fmt.Errorf("store: corrupt finding record %s: %v", entry.Name(), err)
		}
		if provenance.InvocationID != "" {
			references[provenance.InvocationID] = true
		}
	}
	return references, nil
}
