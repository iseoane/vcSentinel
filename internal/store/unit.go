package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Unit represents a delimited change audited as a whole: a slice batch, or
// the set of commits of a PR. RunIDs links the runs (runs/<run-id>.json)
// that were executed over that unit.
type Unit struct {
	ID       string   `json:"id"`
	BaseTree string   `json:"base_tree"`
	HeadTree string   `json:"head_tree"`
	PlanID   string   `json:"plan_id,omitempty"`
	RunIDs   []string `json:"run_ids"`
}

// ComputeUnitID derives the id of a unit as
// sha256(baseTree+headTree+planID). It is deterministic on purpose:
// recomputing it over the same change (for example, a retry) resolves to
// the same unit without external coordination. An empty planID remains
// valid: a unit does not always come from a slice plan.
func ComputeUnitID(baseTree, headTree, planID string) string {
	sum := sha256.Sum256([]byte(baseTree + headTree + planID))
	return hex.EncodeToString(sum[:])
}

// SaveUnit persists u in units/<id>.json.
func (s *Store) SaveUnit(u *Unit) error {
	if u.ID == "" {
		return errors.New("store: unit without id")
	}
	return s.writeJSON(subdirUnits, u.ID, u)
}

// ReadUnit returns the unit with that id, or nil if it does not exist yet.
func (s *Store) ReadUnit(id string) (*Unit, error) {
	var u Unit
	ok, err := s.readJSON(subdirUnits, id, &u)
	if err != nil || !ok {
		return nil, err
	}
	return &u, nil
}
