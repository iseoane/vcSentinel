package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSaveAndReadRun(t *testing.T) {
	s := NewStore(t.TempDir())
	at := time.Now().UTC()
	id := ComputeRunID("unit1", at)
	r := &Run{ID: id, UnitID: "unit1", At: at, Fingerprints: []string{"fp1"}}

	if err := s.SaveRun(r); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	got, err := s.ReadRun(id)
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if got == nil || got.UnitID != "unit1" || len(got.Fingerprints) != 1 {
		t.Errorf("read run = %+v, does not match what was saved", got)
	}
}

func TestStoreReadRunMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	r, err := s.ReadRun("missing")
	if err != nil {
		t.Fatalf("ReadRun: %v", err)
	}
	if r != nil {
		t.Error("ReadRun should return nil for an id without a run")
	}
}

func TestStoreRunCorruptIsError(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	path := filepath.Join(dir, "vcsentinel", subdirRuns, "r1.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadRun("r1"); err == nil {
		t.Error("a corrupt file should return an error, not nil")
	}
}
