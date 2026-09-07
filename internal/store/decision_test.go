package store

import (
	"testing"
	"time"
)

func TestStoreRecordDecisionTwiceAppendNoOverwrite(t *testing.T) {
	s := NewStore(t.TempDir())
	d1 := &Decision{Fingerprint: "fp1", Decision: "accepted_by_user", Actor: "iseoane", At: time.Now().UTC()}
	d2 := &Decision{Fingerprint: "fp2", Decision: "rejected_by_user", Actor: "iseoane", At: time.Now().UTC()}

	if err := s.RecordDecision(d1); err != nil {
		t.Fatalf("first decision: %v", err)
	}
	if err := s.RecordDecision(d2); err != nil {
		t.Fatalf("second decision: %v", err)
	}

	decisions, err := s.ReadDecisions()
	if err != nil {
		t.Fatalf("ReadDecisions: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions = %d, want 2 (append, neither overwrites the other)", len(decisions))
	}
	if decisions[0].Fingerprint != "fp1" || decisions[1].Fingerprint != "fp2" {
		t.Errorf("append order not respected: %+v", decisions)
	}
}

func TestStoreReadDecisionsMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	decisions, err := s.ReadDecisions()
	if err != nil {
		t.Fatalf("ReadDecisions: %v", err)
	}
	if decisions != nil {
		t.Error("ReadDecisions should return nil when decisions.jsonl does not exist")
	}
}
