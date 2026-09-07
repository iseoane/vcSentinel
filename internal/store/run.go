package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Run is a concrete validation/review execution over a Unit at a given
// moment (one gate or review pass). Fingerprints links the findings
// (findings/<fingerprint>.json) that this run produced.
type Run struct {
	ID           string    `json:"id"`
	UnitID       string    `json:"unit_id"`
	At           time.Time `json:"at"`
	Fingerprints []string  `json:"fingerprints"`
}

// ComputeRunID derives the id of a run as sha256(unitID+RFC3339 timestamp
// with nanoseconds). Preferred over an external UUID because it adds no
// new dependency and follows the same deterministic criterion as
// ComputeUnitID: two runs over the same unit at the same instant
// (improbable, but possible in tests) would resolve to the same id, which
// is acceptable because a run is created only once per real invocation.
func ComputeRunID(unitID string, at time.Time) string {
	sum := sha256.Sum256([]byte(unitID + at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

// SaveRun persists r in runs/<id>.json.
func (s *Store) SaveRun(r *Run) error {
	if r.ID == "" {
		return errors.New("store: run without id")
	}
	return s.writeJSON(subdirRuns, r.ID, r)
}

// ReadRun returns the run with that id, or nil if it does not exist yet.
func (s *Store) ReadRun(id string) (*Run, error) {
	var r Run
	ok, err := s.readJSON(subdirRuns, id, &r)
	if err != nil || !ok {
		return nil, err
	}
	return &r, nil
}
