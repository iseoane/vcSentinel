//go:build linux

package reviewsnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCreateFailsClosedOnInsecureStoreRoot pins the fail-closed posture of
// the store root: a root that exists but is not a private 0700 directory
// owned by the current user must never be used — not leased from, not
// published into. The caller gets an error and nothing else.
func TestCreateFailsClosedOnInsecureStoreRoot(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	if err := os.MkdirAll(storeRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, _, cleanup, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err == nil {
		t.Fatalf("Create used an insecure store root (mode 0755): dir=%q cleanup=%p", dir, cleanup)
	}
	if dir != "" || cleanup != nil {
		t.Fatalf("insecure store: dir=%q cleanup=%p, want empty and nil", dir, cleanup)
	}
}

// TestCreateFailsClosedOnSymlinkedStoreRoot pins the same posture for a
// symlinked final component: even a symlink pointing at a perfectly private
// directory must be rejected, because the store's path identity is part of
// its safety.
func TestCreateFailsClosedOnSymlinkedStoreRoot(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	evil := filepath.Join(tmp, "evil")
	if err := os.MkdirAll(evil, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, storeRoot()); err != nil {
		t.Fatal(err)
	}
	dir, _, cleanup, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err == nil {
		t.Fatalf("Create followed a symlinked store root: dir=%q cleanup=%p", dir, cleanup)
	}
	if dir != "" || cleanup != nil {
		t.Fatalf("symlinked store: dir=%q cleanup=%p, want empty and nil", dir, cleanup)
	}
}

// TestReaperNeverTraversesSymlinkedStoreRoot pins the last fail-closed gap:
// Create runs the reaper BEFORE its own root validation, so the reaper itself
// must refuse to traverse anything but a validated store root. A hostile
// final-component symlink must never turn the reaper into a deletion tool
// inside the directory it points at.
func TestReaperNeverTraversesSymlinkedStoreRoot(t *testing.T) {
	_, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	target := filepath.Join(tmp, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	lastDigit, _ := unusedHexDigits(sha[len(sha)-1])
	deadSHA := sha[:len(sha)-1] + string(lastDigit)
	abandoned := publishedPath(target, deadSHA)
	if err := os.MkdirAll(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	abandonedMarker := markerPath(target, deadSHA)
	if err := os.WriteFile(abandonedMarker, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	abandonedManifest := manifestPath(target, deadSHA)
	if err := os.WriteFile(abandonedManifest, []byte("100644 10 audited.go\x00"), 0o400); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{abandoned, abandonedMarker, abandonedManifest} {
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, storeRoot()); err != nil {
		t.Fatal(err)
	}

	if reaped := reapAbandonedSnapshots(time.Now(), staleSnapshotAge); reaped != 0 {
		t.Fatalf("reaped = %d through a symlinked store root, want 0", reaped)
	}
	for name, path := range map[string]string{
		"published tree":     abandoned,
		"readiness marker":   abandonedMarker,
		"readiness manifest": abandonedManifest,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the reaper deleted the %s inside the symlink target: %v", name, err)
		}
	}
}
