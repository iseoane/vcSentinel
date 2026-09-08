package reviewsnapshot

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// creation would leak. It also detects orphan readiness artifacts: a marker
// or manifest whose published tree is gone is exactly the atomicity hole a
// crash between artifact publication and rename would leave behind. Plain
// lock files are not publication state and may remain.
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
	for _, name := range orphanReadinessArtifacts(storeRoot()) {
		t.Fatalf("%s: orphan readiness artifact %q survived an aborted creation", where, name)
	}
}

// orphanReadinessArtifacts lists readiness markers and manifests in the store
// whose published tree is gone.
func orphanReadinessArtifacts(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var orphans []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, publishedPrefix) {
			continue
		}
		if !strings.HasSuffix(name, readySuffix) && !strings.HasSuffix(name, manifestSuffix) {
			continue
		}
		sha := strings.TrimSuffix(strings.TrimPrefix(name, publishedPrefix), readySuffix)
		sha = strings.TrimSuffix(sha, manifestSuffix)
		if _, err := os.Stat(publishedPath(root, sha)); err == nil {
			continue
		}
		orphans = append(orphans, name)
	}
	return orphans
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
	orphanManifest := manifestPath(storeRoot(), sha[:len(sha)-1]+string(otherDigit))
	if err := os.WriteFile(orphanManifest, []byte("100644 10 audited.go\x00"), 0o400); err != nil {
		t.Fatal(err)
	}
	// A staging directory too recent to judge: a live materialization
	// elsewhere could own it.
	freshStaging := filepath.Join(storeRoot(), stagingPrefix+deadSHA+"~5678")
	if err := os.MkdirAll(freshStaging, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{abandoned, abandonedMarker, abandonedStaging, orphanMarker, orphanManifest} {
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	reaped := reapAbandonedSnapshots(time.Now(), staleSnapshotAge)
	if reaped != 4 {
		t.Errorf("reaped = %d, want 4 (abandoned tree, abandoned staging, orphan marker, orphan manifest)", reaped)
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
	if _, err := os.Stat(orphanManifest); !os.IsNotExist(err) {
		t.Error("an orphan readiness manifest survived the reaper")
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

// TestCreatePublishesImmutableValidatedSnapshots pins the retained-evidence
// contract: a published snapshot's regular files are non-writable by the
// owner while its directory root stays a usable working directory, a readiness
// manifest beside the tree describes the expected committed files, and every
// lease validates marker, manifest, and on-disk tree — rejecting added,
// removed, symlinked, mode-changed, or size-changed entries without hashing
// contents. A corrupted cache is never handed out: Create rebuilds it from
// Git under the per-SHA transition lock, restoring canonical committed
// content.
func TestCreatePublishesImmutableValidatedSnapshots(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	snapshot, _, release, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, name := range []string{"audited.go", "context.go", ".env.example"} {
		info, err := os.Stat(filepath.Join(snapshot, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("%s is writable (mode %04o), want owner read-only evidence", name, info.Mode().Perm())
		}
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(snapshot); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("snapshot root mode = %v, want a usable 0700 working directory", info)
		}
	}
	if manifest, err := os.ReadFile(manifestPath(storeRoot(), sha)); err != nil || len(manifest) == 0 {
		t.Fatalf("readiness manifest missing or empty: %v", err)
	}
	assertSharedSnapshotComplete(t, snapshot)

	// The publishing lease is dropped before the corruption rounds: each
	// round's rebuild Create must find the tampered cache without waiting
	// behind any live lease.
	release()
	release()

	// Every corruption below is repaired by the next Create: the corrupted
	// cache is rejected at lease time and rebuilt from Git under the
	// transition lock, at the same published path.
	rebuild := func(stage string) {
		dir, _, cleanup, err := Create(context.Background(), root, sha, []string{"audited.go"})
		if err != nil {
			t.Fatalf("Create over %s corruption: %v", stage, err)
		}
		defer cleanup()
		if dir != snapshot {
			t.Fatalf("rebuild after %s corruption returned %q, want the published %q", stage, dir, snapshot)
		}
		assertSharedSnapshotComplete(t, dir)
		if manifest, err := os.ReadFile(manifestPath(storeRoot(), sha)); err != nil || len(manifest) == 0 {
			t.Fatalf("rebuild after %s corruption lost the readiness manifest: %v", stage, err)
		}
	}

	t.Run("added entry rejected", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(snapshot, "extra.go"), []byte("evil\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rebuild("added")
		if _, err := os.Stat(filepath.Join(snapshot, "extra.go")); !os.IsNotExist(err) {
			t.Fatal("an entry added to the published tree survived the rebuild")
		}
	})

	t.Run("removed entry rejected", func(t *testing.T) {
		if err := os.Remove(filepath.Join(snapshot, "audited.go")); err != nil {
			t.Fatal(err)
		}
		rebuild("removed")
	})

	t.Run("symlinked entry rejected", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unprivileged symlinks are not available on Windows")
		}
		if err := os.Symlink("/etc/hostname", filepath.Join(snapshot, "link.go")); err != nil {
			t.Fatal(err)
		}
		rebuild("symlinked")
		if info, err := os.Lstat(filepath.Join(snapshot, "link.go")); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			t.Fatal("a symlink planted in the published tree survived the rebuild")
		}
	})

	t.Run("mode change rejected", func(t *testing.T) {
		if err := os.Chmod(filepath.Join(snapshot, "context.go"), 0o644); err != nil {
			t.Fatal(err)
		}
		rebuild("mode change")
		info, err := os.Stat(filepath.Join(snapshot, "context.go"))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o222 != 0 {
			t.Fatal("a mode-tampered file kept its writable mode after the rebuild")
		}
	})

	t.Run("size change rejected", func(t *testing.T) {
		target := filepath.Join(snapshot, "audited.go")
		if err := os.Chmod(target, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("tampered\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		rebuild("size change")
		data, err := os.ReadFile(target)
		if err != nil || string(data) != "package p\n" {
			t.Fatalf("tampered content survived the rebuild: %q, %v", data, err)
		}
	})

}

// TestValidObjectIDAcceptsOnlyFullCanonicalHex pins the storage-key alphabet
// in isolation: exactly 40 or 64 lowercase hexadecimal characters — the
// canonical form of SHA-1 and SHA-256 Git object ids — and nothing else.
func TestValidObjectIDAcceptsOnlyFullCanonicalHex(t *testing.T) {
	sha1 := strings.Repeat("deadbeef", 5)           // 40 hex characters
	sha256 := strings.Repeat("deadbeef01234567", 4) // 64 hex characters
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"sha-1 object id accepted", sha1, true},
		{"sha-256 object id accepted", sha256, true},
		{"empty rejected", "", false},
		{"39 characters rejected", sha1[:39], false},
		{"41 characters rejected", sha1 + "0", false},
		{"63 characters rejected", sha256[:63], false},
		{"65 characters rejected", sha256 + "0", false},
		{"revspec rejected", "HEAD", false},
		{"branch name rejected", "main", false},
		{"separator rejected", "a/b", false},
		{"traversal rejected", "../escape", false},
		{"non-hex alphabet rejected", strings.Repeat("g", 40), false},
		{"uppercase rejected", strings.ToUpper(sha1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validObjectID(tc.in); got != tc.want {
				t.Fatalf("validObjectID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// gitCommitExecutable commits an owner-executable run.sh on top of gitInit's
// seed commit and returns the new commit's full SHA, so a committed 100755
// git mode reaches the store pipeline.
func gitCommitExecutable(t *testing.T, root string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", "run.sh")
	run("update-index", "--chmod=+x", "run.sh")
	run("commit", "-qm", "executable")
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out[:len(out)-1])
}

// TestCreatePreservesCommittedExecutableMode pins the exec-bit contract: a
// committed 100755 file is published owner-executable (0500) while regular
// evidence is owner read-only (0400), the readiness manifest records the
// committed mode, and a second Create for the same SHA leases the very same
// tree without a rebuild. Leasing deliberately refreshes the directory mtime:
// the capacity reaper uses it to identify the least recently leased tree.
func TestCreatePreservesCommittedExecutableMode(t *testing.T) {
	root, _ := gitInit(t)
	sha := gitCommitExecutable(t, root)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	snapshot, _, release, err := Create(context.Background(), root, sha, []string{"run.sh"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if runtime.GOOS != "windows" {
		execInfo, err := os.Stat(filepath.Join(snapshot, "run.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if got := execInfo.Mode().Perm(); got != 0o500 {
			t.Fatalf("committed 100755 published as %04o, want 0500", got)
		}
		plainInfo, err := os.Stat(filepath.Join(snapshot, "audited.go"))
		if err != nil {
			t.Fatal(err)
		}
		if got := plainInfo.Mode().Perm(); got != 0o400 {
			t.Fatalf("committed 100644 published as %04o, want 0400", got)
		}
	}
	manifest, err := os.ReadFile(manifestPath(storeRoot(), sha))
	if err != nil || !strings.Contains(string(manifest), "100755 10 run.sh\x00") {
		t.Fatalf("readiness manifest does not record the executable mode: %q, %v", manifest, err)
	}

	// Reuse without rebuild: plant an old directory mtime — the live lease
	// below keeps the reaper away from the stale-looking tree — then require
	// the second successful lease to refresh it for capacity ordering.
	pinned := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(snapshot, pinned, pinned); err != nil {
		t.Fatal(err)
	}
	again, _, secondRelease, err := Create(context.Background(), root, sha, []string{"run.sh"})
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if again != snapshot {
		t.Fatalf("second Create returned %q, want the published %q", again, snapshot)
	}
	info, err := os.Stat(snapshot)
	if err != nil {
		t.Fatalf("stat reused published tree: %v", err)
	}
	if info.ModTime().Equal(pinned) {
		t.Fatalf("published tree lease mtime = %v, want a refresh after pinned %v", info.ModTime(), pinned)
	}
	release()
	secondRelease()
}

// TestCreatePublishesOwnerWritableDirectories preserves a usable provider
// working directory: reviewEnvironment assigns HOME and XDG paths beneath the
// snapshot, so its directories cannot be sealed until provider isolation moves
// outside the evidence tree.
func TestCreatePublishesOwnerWritableDirectories(t *testing.T) {
	root, _ := gitInit(t)
	sha := gitCommitNestedFile(t, root)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	snapshot, _, release, err := Create(context.Background(), root, sha, []string{"nested/context.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, dir := range []string{snapshot, filepath.Join(snapshot, "nested")} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if info.Mode().Perm()&0o200 == 0 {
			t.Fatalf("published directory %s is not owner-writable: mode %04o", dir, info.Mode().Perm())
		}
	}

	release()
}

func TestLeaseRejectsPublishedSnapshotWithInvalidManifestMode(t *testing.T) {
	root, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	_, _, release, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	release()

	manifest := manifestPath(storeRoot(), sha)
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupted := strings.Replace(string(data), "100644 ", "100600 ", 1)
	if corrupted == string(data) {
		t.Fatal("fixture manifest has no regular-file mode to corrupt")
	}
	if err := os.WriteFile(manifest, []byte(corrupted), 0o600); err != nil {
		t.Fatal(err)
	}

	lease, err := leasePublishedSnapshot(sha)
	if err != nil {
		t.Fatalf("leasePublishedSnapshot: %v", err)
	}
	if lease != nil {
		lease.release()
		t.Fatal("leasePublishedSnapshot accepted a manifest with an invalid Git mode")
	}
}

func gitCommitNestedFile(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "nested", "context.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", root, "add", "nested/context.go")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add nested/context.go: %v\n%s", err, output)
	}
	cmd = exec.Command("git", "-C", root, "commit", "-qm", "nested")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit nested/context.go: %v\n%s", err, output)
	}
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

// TestReaperRechecksStalenessUnderLock pins the reaper's TOCTOU guard: the
// staleness verdict is re-checked against each target AFTER the exclusive
// lock is held, so a fresh publication is never deleted even when an older
// directory scan had already marked the entry for collection — and a stale,
// unlocked entry still goes away.
// TestCapacityReaperKeepsSmallStoreOnOtherwiseFullFilesystem verifies that
// capacity cleanup reacts only to review snapshots it can actually control. A
// nearly full large disk whose unrelated contents dwarf this small store must
// not destroy reusable evidence merely because deleting it cannot restore a
// global free-space target.
func TestCapacityReaperKeepsSmallStoreOnOtherwiseFullFilesystem(t *testing.T) {
	_, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if err := os.MkdirAll(storeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}

	candidateSHA, _ := unusedHexDigits(sha[len(sha)-1])
	candidate := publishedPath(storeRoot(), sha[:len(sha)-1]+string(candidateSHA))
	if err := os.MkdirAll(candidate, 0o700); err != nil {
		t.Fatal(err)
	}

	originalSpace := storeFilesystemSpace
	storeFilesystemSpace = func(string) (filesystemSpace, error) {
		return filesystemSpace{available: 50 << 30, capacity: 1 << 40}, nil
	}
	t.Cleanup(func() { storeFilesystemSpace = originalSpace })

	if removed := reapSharedStoreCapacity(storeRoot()); removed != 0 {
		t.Fatalf("reapSharedStoreCapacity removed %d entries from a small store", removed)
	}
	if _, err := os.Stat(candidate); err != nil {
		t.Fatalf("small reusable tree was evicted because of unrelated disk use: %v", err)
	}
}

// TestCapacityReaperEvictsLeastRecentlyLeasedUnleasedTree verifies the size
// ceiling's safety and ordering together: an exclusive lock is still required
// before deletion, so a live lease survives even when it is oldest, and the
// oldest unleased published tree is evicted before newer reusable evidence.
func TestCapacityReaperEvictsLeastRecentlyLeasedUnleasedTree(t *testing.T) {
	_, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if err := os.MkdirAll(storeRoot(), 0o700); err != nil {
		t.Fatal(err)
	}

	var replacements []byte
	for digit := byte('0'); digit <= 'f' && len(replacements) < 3; digit++ {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			continue
		}
		if digit != sha[len(sha)-1] {
			replacements = append(replacements, digit)
		}
	}
	activeSHA := sha[:len(sha)-1] + string(replacements[0])
	oldestUnleasedSHA := sha[:len(sha)-1] + string(replacements[1])
	newerUnleasedSHA := sha[:len(sha)-1] + string(replacements[2])
	for _, candidate := range []string{activeSHA, oldestUnleasedSHA, newerUnleasedSHA} {
		tree := publishedPath(storeRoot(), candidate)
		if err := os.MkdirAll(tree, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, "payload"), make([]byte, 9<<10), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	for path, modTime := range map[string]time.Time{
		publishedPath(storeRoot(), activeSHA):         now.Add(-3 * time.Minute),
		publishedPath(storeRoot(), oldestUnleasedSHA): now.Add(-2 * time.Minute),
		publishedPath(storeRoot(), newerUnleasedSHA):  now.Add(-time.Minute),
	} {
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	activeLock, acquired, err := lockSnapshot(lockPath(storeRoot(), activeSHA), false)
	if err != nil || !acquired {
		t.Fatalf("lock active snapshot: acquired=%t err=%v", acquired, err)
	}
	defer activeLock.Close()

	originalSpace := storeFilesystemSpace
	storeFilesystemSpace = func(string) (filesystemSpace, error) {
		return filesystemSpace{capacity: 250 << 10}, nil
	}
	t.Cleanup(func() { storeFilesystemSpace = originalSpace })

	if removed := reapSharedStoreCapacity(storeRoot()); removed != 1 {
		t.Fatalf("reapSharedStoreCapacity removed %d entries, want 1", removed)
	}
	if _, err := os.Stat(publishedPath(storeRoot(), activeSHA)); err != nil {
		t.Fatalf("active leased tree was evicted: %v", err)
	}
	if _, err := os.Stat(publishedPath(storeRoot(), oldestUnleasedSHA)); !os.IsNotExist(err) {
		t.Fatalf("oldest unleased tree survived capacity eviction: %v", err)
	}
	if _, err := os.Stat(publishedPath(storeRoot(), newerUnleasedSHA)); err != nil {
		t.Fatalf("newer unleased tree was evicted before the oldest one: %v", err)
	}
}

func TestReaperRechecksStalenessUnderLock(t *testing.T) {
	_, sha := gitInit(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	lastDigit, otherDigit := unusedHexDigits(sha[len(sha)-1])
	freshSHA := sha[:len(sha)-1] + string(lastDigit)
	staleSHA := sha[:len(sha)-1] + string(otherDigit)

	freshTree := publishedPath(storeRoot(), freshSHA)
	if err := os.MkdirAll(freshTree, 0o700); err != nil {
		t.Fatal(err)
	}
	freshMarker := markerPath(storeRoot(), freshSHA)
	if err := os.WriteFile(freshMarker, []byte("ready\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	staleTree := publishedPath(storeRoot(), staleSHA)
	if err := os.MkdirAll(staleTree, 0o700); err != nil {
		t.Fatal(err)
	}
	staleMarker := markerPath(storeRoot(), staleSHA)
	if err := os.WriteFile(staleMarker, []byte("ready\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{staleTree, staleMarker} {
		if err := os.Chtimes(path, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now()
	if removeUnleasedStoreEntry(storeRoot(), freshSHA, now, staleSnapshotAge, freshTree, freshMarker) {
		t.Fatal("the reaper deleted a fresh publication that was re-checked under the lock")
	}
	if _, err := os.Stat(freshTree); err != nil {
		t.Fatalf("fresh publication vanished: %v", err)
	}
	if _, err := os.Stat(freshMarker); err != nil {
		t.Fatalf("fresh publication's marker vanished: %v", err)
	}
	if !removeUnleasedStoreEntry(storeRoot(), staleSHA, now, staleSnapshotAge, staleTree, staleMarker) {
		t.Fatal("the reaper refused to collect a stale, unlocked entry")
	}
	if _, err := os.Stat(staleTree); !os.IsNotExist(err) {
		t.Fatal("a stale, unlocked entry survived the reaper")
	}
}
