package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

// SaveFinding persists h in findings/<fingerprint>.json. The key is the
// Fingerprint (computed in T2.3), not a sequential id: two runs producing
// the same finding resolve to the same file without coordination.
func (s *Store) SaveFinding(h *review.Finding) error {
	if h.Fingerprint == "" {
		return errors.New("store: finding without fingerprint")
	}
	return s.writeJSON(subdirFindings, h.Fingerprint, h)
}

// ReadFinding returns the finding with that fingerprint, or nil if it does
// not exist yet. A corrupt file is an explicit error.
func (s *Store) ReadFinding(fingerprint string) (*review.Finding, error) {
	var h review.Finding
	ok, err := s.readJSON(subdirFindings, fingerprint, &h)
	if err != nil || !ok {
		return nil, err
	}
	return &h, nil
}

// ReferencedInvocationIDs returns every durable invocation identity recorded
// as review provenance across the persisted findings. Each blob is a full
// review.Finding, so the top-level invocation_id it decodes IS
// Finding.InvocationID — including the admitted refuter identity stamped on
// refutation-downgraded findings — which keeps this surface symmetric with
// the ledger record scan in cmd/vcsentinel's collectProvenanceReferences.
// Retention callers feed this set into PruneExecutions so a stream cited by
// any finding is kept. A findings file that fails to decode fails closed:
// retention decisions must never run while evidence is unreadable.
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
