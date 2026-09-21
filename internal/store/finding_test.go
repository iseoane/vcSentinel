package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

func TestStoreSaveAndReadFinding(t *testing.T) {
	s := NewStore(t.TempDir())
	h := &review.Finding{Fingerprint: "fp1", Title: "something", Severity: "critical"}

	if err := s.SaveFinding(h); err != nil {
		t.Fatalf("SaveFinding: %v", err)
	}
	got, err := s.ReadFinding("fp1")
	if err != nil {
		t.Fatalf("ReadFinding: %v", err)
	}
	if got == nil || got.Title != "something" || got.Severity != "critical" {
		t.Errorf("read finding = %+v, does not match what was saved", got)
	}
}

func TestStoreReadFindingMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	h, err := s.ReadFinding("missing")
	if err != nil {
		t.Fatalf("ReadFinding: %v", err)
	}
	if h != nil {
		t.Error("ReadFinding should return nil for a fingerprint without a finding")
	}
}

func TestStoreFindingCorruptIsError(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	path := filepath.Join(dir, "vcsentinel", subdirFindings, "fp1.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFinding("fp1"); err == nil {
		t.Error("a corrupt file should return an error, not nil")
	}
}
