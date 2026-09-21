package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSaveAndReadCommitIndex(t *testing.T) {
	s := NewStore(t.TempDir())
	idx := &CommitIndex{SHA: "abc123", Message: "feat(x)", Fingerprints: []string{"fp1", "fp2"}}

	if err := s.SaveCommitIndex(idx); err != nil {
		t.Fatalf("SaveCommitIndex: %v", err)
	}
	got, err := s.ReadCommitIndex("abc123")
	if err != nil {
		t.Fatalf("ReadCommitIndex: %v", err)
	}
	if got == nil || got.Message != "feat(x)" || len(got.Fingerprints) != 2 {
		t.Errorf("read index = %+v, does not match what was saved", got)
	}
}

func TestStoreReadCommitIndexMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	idx, err := s.ReadCommitIndex("missing")
	if err != nil {
		t.Fatalf("ReadCommitIndex: %v", err)
	}
	if idx != nil {
		t.Error("ReadCommitIndex should return nil for a sha without an index")
	}
}

// TestRegisterCommitBlobsMergesDisjointSets covers the T2.7 fix: two calls
// on the same sha with blob sets WITHOUT overlap must accumulate (union),
// not have the second overwrite the first. Before the fix, idx.Blobs =
// blobs substituted the whole map and the first call's entry was left
// orphaned in blobs/<blob>.json.
func TestRegisterCommitBlobsMergesDisjointSets(t *testing.T) {
	s := NewStore(t.TempDir())

	if err := s.RegisterCommitBlobs("sha1", map[string]string{"a.go": "blobA"}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := s.RegisterCommitBlobs("sha1", map[string]string{"b.go": "blobB"}); err != nil {
		t.Fatalf("second call: %v", err)
	}

	got, err := s.ReadCommitIndex("sha1")
	if err != nil {
		t.Fatalf("ReadCommitIndex: %v", err)
	}
	if got == nil || len(got.Blobs) != 2 {
		t.Fatalf("Blobs = %+v, want the union of both calls (2 entries)", got)
	}
	if got.Blobs["a.go"] != "blobA" || got.Blobs["b.go"] != "blobB" {
		t.Errorf("Blobs = %+v, want {a.go:blobA, b.go:blobB}", got.Blobs)
	}
}

func TestStoreCommitIndexCorruptIsError(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	path := filepath.Join(dir, "vcsentinel", subdirCommits, "abc123.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadCommitIndex("abc123"); err == nil {
		t.Error("a corrupt file should return an error, not nil")
	}
}
