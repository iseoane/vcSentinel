package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Unit representa un cambio delimitado auditado como conjunto: un lote de
// slice, o el conjunto de commits de una PR. RunIDs enlaza las ejecuciones
// (runs/<run-id>.json) que se hicieron sobre esa unit.
type Unit struct {
	ID       string   `json:"id"`
	BaseTree string   `json:"base_tree"`
	HeadTree string   `json:"head_tree"`
	PlanID   string   `json:"plan_id,omitempty"`
	RunIDs   []string `json:"run_ids"`
}

// CalcularUnitID deriva el id de una unit como sha256(baseTree+headTree+planID).
// Es determinista a propósito: recalcular sobre el mismo cambio (por ejemplo,
// un reintento) resuelve a la misma unit sin coordinación externa. planID
// vacío sigue siendo válido: una unit no siempre viene de un plan de slice.
func CalcularUnitID(baseTree, headTree, planID string) string {
	suma := sha256.Sum256([]byte(baseTree + headTree + planID))
	return hex.EncodeToString(suma[:])
}

// GuardarUnit persiste u en units/<id>.json.
func (s *Store) GuardarUnit(u *Unit) error {
	if u.ID == "" {
		return errors.New("store: unit sin id")
	}
	return s.guardarJSON(subdirUnits, u.ID, u)
}

// LeerUnit devuelve la unit con ese id, o nil si no existe todavía.
func (s *Store) LeerUnit(id string) (*Unit, error) {
	var u Unit
	ok, err := s.leerJSON(subdirUnits, id, &u)
	if err != nil || !ok {
		return nil, err
	}
	return &u, nil
}
