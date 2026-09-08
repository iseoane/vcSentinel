package reviewsnapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// assertSharedSnapshotComplete asserts that dir carries the committed bytes of
// the gitInit fixture: the audited file and the committed-but-never-audited
// context file must both be present with their exact committed content, so a
// caller holding the snapshot observes a fully materialized tree, never a
// partially written one.
func assertSharedSnapshotComplete(t *testing.T, dir string) {
	t.Helper()
	for name, want := range map[string]string{
		"audited.go": "package p\n",
		"context.go": "package p\n// context\n",
	} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("snapshot %q is missing %s: %v", dir, name, err)
		}
		if string(data) != want {
			t.Fatalf("snapshot %s = %q, want committed bytes %q", name, data, want)
		}
	}
}

// TestCreateSameSHASharesOneSnapshotDirectory pins the shared-store contract:
// the snapshot for one audited commit SHA is materialized once and every
// caller for that SHA — sequential or concurrent — receives the SAME complete
// directory instead of a private copy. Cleanup is an idempotent lease
// release: one release among several live leases must never disturb the
// callers still holding one, a double release is a no-op, and releasing the
// LAST lease retains the published tree for the next invocation auditing the
// SHA — only the lock-aware stale reaper ever removes it.
func TestCreateSameSHASharesOneSnapshotDirectory(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// Five concurrent callers against an empty store single-flight behind one
	// materialization and observe the same complete directory.
	const callers = 5
	dirs := make([]string, callers)
	releases := make([]func(), callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dir, _, release, err := Create(context.Background(), root, sha, []string{"audited.go"})
			if err != nil {
				t.Errorf("concurrent Create: %v", err)
				return
			}
			dirs[i], releases[i] = dir, release
		}(i)
	}
	wg.Wait()
	for i, dir := range dirs {
		if dir == "" {
			t.Fatalf("concurrent caller %d got no snapshot directory", i)
		}
		if dir != dirs[0] {
			t.Fatalf("concurrent caller %d observed %q, want the shared %q", i, dir, dirs[0])
		}
		assertSharedSnapshotComplete(t, dir)
	}

	// Two sequential callers lease the very same published tree.
	first, _, firstRelease, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	second, _, secondRelease, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if first != second || first != dirs[0] {
		t.Fatalf("sequential Creates returned %q and %q, want the shared %q", first, second, dirs[0])
	}
	assertSharedSnapshotComplete(t, first)

	// An idempotent release among several live leases must not delete the
	// shared tree the other callers still hold.
	firstRelease()
	firstRelease()
	assertSharedSnapshotComplete(t, first)

	// Releasing every lease retains the published snapshot for the next
	// invocation auditing this SHA; the stale reaper owns its removal.
	for _, release := range releases {
		release()
	}
	secondRelease()
	if _, err := os.Stat(dirs[0]); err != nil {
		t.Fatalf("published snapshot was deleted by its last lease release: %v", err)
	}
	assertSharedSnapshotComplete(t, dirs[0])
}

// assertStoreHasNoPublishedOrStaging fails when the shared store holds any
// published tree or staging directory — the residue an aborted or failed
// creation would leak. Plain lock files are not publication state and may
// remain.
func assertStoreHasNoPublishedOrStaging(t *testing.T, where string) {
	t.Helper()
	entries, err := os.ReadDir(storeRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), publishedPrefix) {
			t.Fatalf("%s: published directory %q survived an aborted creation", where, entry.Name())
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), stagingPrefix) {
			t.Fatalf("%s: staging directory %q survived an aborted creation", where, entry.Name())
		}
	}
}

// TestCreateAbortsLeaveNoPublishedOrStagingDirectory pins the atomicity half
// of the shared store: a creation that is canceled mid-materialization or
// fails outright publishes nothing and leaves no staging directory behind, so
// no later caller can ever observe an incomplete tree under the SHA it was
// addressed to.
func TestCreateAbortsLeaveNoPublishedOrStagingDirectory(t *testing.T) {
	root, sha := gitInitManyFiles(t, 30)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// Cancellation in the middle of materialization: the counting context
	// fires inside the whole-tree loop, long before all 30 files are written.
	cancelCtx := newCountingCancelContext(4)
	_, _, cleanup, err := Create(cancelCtx, root, sha, []string{"file0.go"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if cleanup != nil {
		t.Fatalf("cleanup = %p, want nil: Create must clean up before returning on abort", cleanup)
	}
	assertStoreHasNoPublishedOrStaging(t, "canceled creation")

	// A valid but nonexistent object id — git itself fails mid-flow — must be
	// just as clean as a cancellation.
	unknownSHA := strings.Repeat("beef", 10)
	if _, _, cleanup, err := Create(context.Background(), root, unknownSHA, []string{"file0.go"}); err == nil || cleanup != nil {
		t.Fatalf("Create with unknown SHA: err=%v, cleanup=%p, want error and nil cleanup", err, cleanup)
	}
	assertStoreHasNoPublishedOrStaging(t, "failed creation")
}

// TestReaperSkipsActiveLeaseAndRemovesAbandonedStoreEntries pins the
// lock-aware half of reaping: stale store residue is removed only after the
// reaper acquires the entry's nonblocking exclusive lock, so a snapshot whose
// lease is still live is never deleted — even when its directory already
// looks stale — while abandoned published trees, staging directories, and
// orphaned readiness markers go away.
func TestReaperSkipsActiveLeaseAndRemovesAbandonedStoreEntries(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	active, _, release, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Make the live snapshot LOOK stale: only its lease lock can protect it.
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(active, stale, stale); err != nil {
		t.Fatal(err)
	}

	// Residue of dead processes, for SHAs nothing leases: a published tree
	// with its readiness marker, a staging directory, and an orphan marker
	// with no tree behind it — all old enough to reap and all unlocked.
	lastDigit, otherDigit := unusedHexDigits(sha[len(sha)-1])
	deadSHA := sha[:len(sha)-1] + string(lastDigit)
	abandoned := publishedPath(storeRoot(), deadSHA)
	if err := os.MkdirAll(filepath.Join(abandoned, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	abandonedMarker := markerPath(storeRoot(), deadSHA)
	if err := os.WriteFile(abandonedMarker, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	abandonedStaging := filepath.Join(storeRoot(), stagingPrefix+deadSHA+"~1234")
	if err := os.MkdirAll(abandonedStaging, 0o700); err != nil {
		t.Fatal(err)
	}
	orphanMarker := markerPath(storeRoot(), sha[:len(sha)-1]+string(otherDigit))
	if err := os.WriteFile(orphanMarker, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A staging directory too recent to judge: a live materialization
	// elsewhere could own it.
	freshStaging := filepath.Join(storeRoot(), stagingPrefix+deadSHA+"~5678")
	if err := os.MkdirAll(freshStaging, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{abandoned, abandonedMarker, abandonedStaging, orphanMarker} {
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	reaped := reapAbandonedSnapshots(time.Now(), staleSnapshotAge)
	if reaped != 3 {
		t.Errorf("reaped = %d, want 3 (abandoned tree, abandoned staging, orphan marker)", reaped)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Error("an abandoned published snapshot survived the reaper")
	}
	if _, err := os.Stat(abandonedMarker); !os.IsNotExist(err) {
		t.Error("an abandoned readiness marker survived the reaper")
	}
	if _, err := os.Stat(abandonedStaging); !os.IsNotExist(err) {
		t.Error("an abandoned staging directory survived the reaper")
	}
	if _, err := os.Stat(orphanMarker); !os.IsNotExist(err) {
		t.Error("an orphan readiness marker survived the reaper")
	}
	if _, err := os.Stat(freshStaging); err != nil {
		t.Error("a fresh staging directory was deleted: it could be materializing right now")
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatal("the active lease's snapshot was deleted: a stale mtime must not defeat its lock")
	}
	assertSharedSnapshotComplete(t, active)

	// Releasing the live lease retains the tree even though its directory
	// mtime is long past the stale threshold: a release never deletes, the
	// reaper does — and now that nothing holds the lock anymore, the next
	// reaping pass collects exactly this one stale, unlocked tree.
	release()
	release()
	if _, err := os.Stat(active); err != nil {
		t.Fatalf("published snapshot was deleted by its last lease release: %v", err)
	}
	if reaped := reapAbandonedSnapshots(time.Now(), staleSnapshotAge); reaped != 1 {
		t.Errorf("second reaped = %d, want 1 (the released stale tree)", reaped)
	}
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatal("the reaper left a stale, unlocked published snapshot behind")
	}
}

// TestCreateRetainsPublishedSnapshotAfterRelease pins the reuse contract for
// sequential invocations: the published snapshot for a SHA is retained on
// disk after every current lease is released, so a fresh Create for the same
// SHA — a format or transport retry, a second provider auditing the same
// commit — leases the SAME complete directory instead of rematerializing it.
// The stale-and-unlocked tree only goes away when the lock-aware reaper
// collects it, never when a caller cleans up. The canary the test plants in
// the tree is what distinguishes reuse from an identical-path
// rematerialization.
func TestCreateRetainsPublishedSnapshotAfterRelease(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	first, _, firstRelease, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	canary := filepath.Join(first, "lease-canary")
	if err := os.WriteFile(canary, []byte("leased\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstRelease()
	firstRelease() // idempotent

	second, _, secondRelease, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create after release: %v", err)
	}
	if second != first {
		t.Fatalf("Create after release returned %q, want the retained %q", second, first)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("snapshot was rematerialized after release: canary gone: %v", err)
	}
	assertSharedSnapshotComplete(t, second)
	secondRelease()
	secondRelease()

	// Retained, not deleted: only the stale reaper may remove it now.
	if _, err := os.Stat(first); err != nil {
		t.Fatalf("published snapshot was deleted by its last lease release: %v", err)
	}
	assertSharedSnapshotComplete(t, first)
}

// unusedHexDigits returns the two smallest hexadecimal digits different from
// c, so reaper fixtures derive valid object-id keys that are guaranteed never
// to collide with the audited SHA's key.
func unusedHexDigits(c byte) (byte, byte) {
	var found []byte
	for d := byte('0'); d <= '9' && len(found) < 2; d++ {
		if d != c {
			found = append(found, d)
		}
	}
	for d := byte('a'); d <= 'f' && len(found) < 2; d++ {
		if d != c {
			found = append(found, d)
		}
	}
	return found[0], found[1]
}

// TestReaperRetainsLockNamespaceAcrossTransition pins why lock files are
// persistent: the reaper removes stale snapshot and staging residue under the
// SHA's exclusive lock, but never the lock file itself. Unlinking a lock file
// while its inode is locked opens a window in which a party holding the old
// file open locks an orphaned inode while a later opener creates a fresh
// inode — two lock holders that no longer exclude each other, which breaks
// publication and reaping safety. After a reaping transition the lock file
// must still exist and must still arbitrate: one exclusive holder excludes
// the next on the very same file.
func TestReaperRetainsLockNamespaceAcrossTransition(t *testing.T) {
	_, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// Stale, unlocked residue of a dead process, under a valid object-id key.
	lastDigit, _ := unusedHexDigits(sha[len(sha)-1])
	deadSHA := sha[:len(sha)-1] + string(lastDigit)
	abandoned := publishedPath(storeRoot(), deadSHA)
	if err := os.MkdirAll(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath(storeRoot(), deadSHA), []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{abandoned, markerPath(storeRoot(), deadSHA)} {
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	lockFile := lockPath(storeRoot(), deadSHA)

	if reaped := reapAbandonedSnapshots(time.Now(), staleSnapshotAge); reaped != 1 {
		t.Fatalf("reaped = %d, want 1 (the stale abandoned tree)", reaped)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("the reaper unlinked the lock file across its transition: %v", err)
	}
	first, acquiredFirst, err := lockSnapshot(lockFile, true)
	if err != nil {
		t.Fatal(err)
	}
	if !acquiredFirst {
		t.Fatal("lock file unusable right after a reaping transition")
	}
	_, acquiredSecond, err := lockSnapshot(lockFile, true)
	if err != nil {
		t.Fatal(err)
	}
	if acquiredSecond {
		t.Fatal("two exclusive locks were granted after a reaper transition: the lock namespace was replaced")
	}
	_ = first.Close()
}

// TestCreateRejectsNonCanonicalObjectID pins the storage-key contract: the
// SHA is interpolated into the store's file and directory names, so only a
// full hexadecimal Git object id — 40 hex characters for SHA-1 repositories,
// 64 for SHA-256 — may reach the store. Revspecs, abbreviations, separators,
// and non-canonical case are rejected before any store filesystem operation,
// while canonical callers keep working unchanged.
func TestCreateRejectsNonCanonicalObjectID(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	rejections := []string{
		"HEAD",
		"main",
		"../escape",
		"a/b",
		sha + "/../../elsewhere",
		"abc123",                // abbreviated, not a full object id
		strings.Repeat("g", 40), // wrong alphabet
		strings.ToUpper(sha),    // non-canonical case
		sha + "0",               // 41 characters
		sha[:39],                // 39 characters
	}
	for _, bad := range rejections {
		dir, _, cleanup, err := Create(context.Background(), root, bad, []string{"audited.go"})
		if err == nil {
			t.Fatalf("Create accepted non-canonical object id %q", bad)
		}
		if dir != "" || cleanup != nil {
			t.Fatalf("Create with non-canonical object id %q: dir=%q cleanup=%p, want empty and nil", bad, dir, cleanup)
		}
	}
	// Rejection happens before any store filesystem operation: neither the
	// reaper's scan nor a store root may exist afterwards.
	if _, err := os.Stat(storeRoot()); !os.IsNotExist(err) {
		t.Fatalf("store was mutated by rejected object ids: %v", err)
	}

	// A canonical full object id keeps working end to end.
	dir, survived, cleanup, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create with canonical object id: %v", err)
	}
	assertSharedSnapshotComplete(t, dir)
	if len(survived) != 1 || survived[0] != "audited.go" {
		t.Fatalf("survived = %v, want [audited.go]", survived)
	}
	cleanup()
}
