package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Run es una ejecución concreta de validación/revisión sobre una Unit en un
// momento dado (una pasada de gate o de review). Fingerprints enlaza los
// hallazgos (findings/<fingerprint>.json) que produjo esa ejecución.
type Run struct {
	ID           string    `json:"id"`
	UnitID       string    `json:"unit_id"`
	At           time.Time `json:"at"`
	Fingerprints []string  `json:"fingerprints"`
}

// CalcularRunID deriva el id de un run como sha256(unitID+timestamp RFC3339
// con nanosegundos). Se prefiere a un UUID externo porque no añade
// dependencia nueva y sigue el mismo criterio determinista que CalcularUnitID:
// dos runs sobre la misma unit en el mismo instante (improbable, pero posible
// en tests) resolverían al mismo id, lo que es aceptable porque un run se crea
// una sola vez por invocación real.
func CalcularRunID(unitID string, at time.Time) string {
	suma := sha256.Sum256([]byte(unitID + at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(suma[:])
}

// GuardarRun persiste r en runs/<id>.json.
func (s *Store) GuardarRun(r *Run) error {
	if r.ID == "" {
		return errors.New("store: run sin id")
	}
	return s.guardarJSON(subdirRuns, r.ID, r)
}

// LeerRun devuelve el run con ese id, o nil si no existe todavía.
func (s *Store) LeerRun(id string) (*Run, error) {
	var r Run
	ok, err := s.leerJSON(subdirRuns, id, &r)
	if err != nil || !ok {
		return nil, err
	}
	return &r, nil
}
