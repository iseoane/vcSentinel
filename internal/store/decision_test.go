package store

import (
	"testing"
	"time"
)

func TestStoreRegistrarDecisionDosVecesAppendNoPisa(t *testing.T) {
	s := NuevoStore(t.TempDir())
	d1 := &Decision{Fingerprint: "fp1", Decision: "accepted_by_user", Actor: "iseoane", At: time.Now().UTC()}
	d2 := &Decision{Fingerprint: "fp2", Decision: "rejected_by_user", Actor: "iseoane", At: time.Now().UTC()}

	if err := s.RegistrarDecision(d1); err != nil {
		t.Fatalf("primera decision: %v", err)
	}
	if err := s.RegistrarDecision(d2); err != nil {
		t.Fatalf("segunda decision: %v", err)
	}

	decisiones, err := s.LeerDecisiones()
	if err != nil {
		t.Fatalf("LeerDecisiones: %v", err)
	}
	if len(decisiones) != 2 {
		t.Fatalf("decisiones = %d, esperado 2 (append, ninguna pisa a la otra)", len(decisiones))
	}
	if decisiones[0].Fingerprint != "fp1" || decisiones[1].Fingerprint != "fp2" {
		t.Errorf("el orden append no se respetó: %+v", decisiones)
	}
}

func TestStoreLeerDecisionesInexistente(t *testing.T) {
	s := NuevoStore(t.TempDir())
	decisiones, err := s.LeerDecisiones()
	if err != nil {
		t.Fatalf("LeerDecisiones: %v", err)
	}
	if decisiones != nil {
		t.Error("LeerDecisiones debería devolver nil si decisions.jsonl no existe")
	}
}
