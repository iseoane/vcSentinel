package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestSaveCachePurgesExpiredEntries covers cleanup code that existed written
// and tested but that NOBODY called: PurgeGraphCache did not have a single
// caller in production, so the graph cache grew without bound.
//
// The pattern is the one PurgeSnapshots already uses, wired into
// internal/validation/candidate.go: purge on use, so the residue stays
// bounded by usage instead of growing until something breaks.
func TestSaveCachePurgesExpiredEntries(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	root, err := cacheRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()

	base := filepath.Join(dir, ".git", "vcsentinel", "graph")
	// Purging only removes <oid>.json files with a valid OID, so the fixture
	// uses the real shape.
	expired := filepath.Join(base, "0123456789abcdef0123456789abcdef01234567.json")
	if err := os.WriteFile(expired, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-GraphCacheRetention - time.Hour)
	if err := os.Chtimes(expired, old, old); err != nil {
		t.Fatal(err)
	}

	p := &nativeProvider{dir: dir, identity: "test-oid"}
	if err := p.saveCache(nil); err != nil {
		t.Fatalf("saveCache: %v", err)
	}

	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Error("the expired entry is still in the cache: the purge did not run on save")
	}
}
