package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// requireRealGit skips the test in -short mode or when git is not in the
// PATH, like the rest of the package's integration tests.
func requireRealGit(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}
}

func TestTreeOfMatchesRevParse(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	expected := runGitInDir(t, dir, "rev-parse", "HEAD^{tree}")
	got, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned error: %v", err)
	}
	if got != expected {
		t.Errorf("TreeOf(HEAD) = %q, expected %q", got, expected)
	}
}

// TestTreeOfRejectsRevisionStartingWithDash covers B14: a revision that
// starts with "-" would be interpreted as an option of "git rev-parse"
// instead of as a revision name (option injection), so it must be rejected
// before being interpolated into the command.
func TestTreeOfRejectsRevisionStartingWithDash(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if _, err := TreeOf("--upload-pack=touch /tmp/pwned"); err == nil {
		t.Fatal("TreeOf should reject a revision that starts with \"-\"")
	}
}

func TestCreateSnapshotContainsTreeFiles(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned error: %v", err)
	}

	path, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatalf("CreateSnapshot returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("CreateSnapshot should return an existing directory, path=%q err=%v", path, err)
	}
	content, err := os.ReadFile(filepath.Join(path, "a.go"))
	if err != nil {
		t.Fatalf("could not read a.go inside the snapshot: %v", err)
	}
	if string(content) != "package a\n" {
		t.Errorf("a.go content = %q, expected %q", content, "package a\n")
	}
}

func TestCreateSnapshotIsIdempotent(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned error: %v", err)
	}

	first, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatalf("first call to CreateSnapshot returned error: %v", err)
	}
	second, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatalf("second call to CreateSnapshot returned error: %v", err)
	}
	if first != second {
		t.Errorf("CreateSnapshot is not idempotent: %q != %q", first, second)
	}

	listing := runGitInDir(t, dir, "worktree", "list")
	if n := strings.Count(listing, first); n != 1 {
		t.Errorf("git worktree list shows %d entries for %q, expected 1:\n%s", n, first, listing)
	}
}

func TestCreateSnapshotConcurrentDoesNotDuplicateOrFail(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned error: %v", err)
	}

	const calls = 4
	paths := make([]string, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			path, err := CreateSnapshot(tree)
			paths[idx] = path
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d returned error: %v", i, err)
		}
	}
	for i := 1; i < calls; i++ {
		if paths[i] != paths[0] {
			t.Errorf("call %d returned a different path: %q != %q", i, paths[i], paths[0])
		}
	}

	listing := runGitInDir(t, dir, "worktree", "list")
	if n := strings.Count(listing, paths[0]); n != 1 {
		t.Errorf("git worktree list shows %d entries for %q, expected 1:\n%s", n, paths[0], listing)
	}
}

func TestPurgeSnapshotsShortAgePurgesRecent(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned an error: %v", err)
	}
	path, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatalf("CreateSnapshot returned an error: %v", err)
	}

	if err := PurgeSnapshots(0); err != nil {
		t.Fatalf("PurgeSnapshots returned error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the snapshot should have been purged, but it still exists (err=%v)", err)
	}
}

// TestPurgeSnapshotsPropagatesErrorWhenRemoveFails covers B15: today the
// function returns nil unconditionally even though "worktree remove" and its
// --force retry both fail, so the caller believes the disk was left clean
// when in fact the broken snapshot is still there. It is simulated with a
// directory that is NOT a registered worktree (git cannot remove it through
// either route) and an old modification date so it falls into the purge
// range.
func TestPurgeSnapshotsPropagatesErrorWhenRemoveFails(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	snapshots, err := snapshotsDir()
	if err != nil {
		t.Fatalf("snapshotsDir returned error: %v", err)
	}
	brokenPath := filepath.Join(snapshots, "not-a-worktree")
	if err := os.MkdirAll(brokenPath, 0755); err != nil {
		t.Fatalf("could not create the broken directory: %v", err)
	}
	aged := time.Now().Add(-time.Hour)
	if err := os.Chtimes(brokenPath, aged, aged); err != nil {
		t.Fatalf("could not age the broken directory: %v", err)
	}

	if err := PurgeSnapshots(time.Minute); err == nil {
		t.Fatal("PurgeSnapshots should return an error: it could not remove a broken snapshot")
	}
	if _, err := os.Stat(brokenPath); err != nil {
		t.Errorf("the broken directory should still exist after the failure, but: %v", err)
	}
}

func TestPurgeSnapshotsLongAgeDoesNotPurge(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned error: %v", err)
	}
	path, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatalf("CreateSnapshot returned error: %v", err)
	}

	if err := PurgeSnapshots(time.Hour); err != nil {
		t.Fatalf("PurgeSnapshots returned error: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the snapshot should not have been purged (err=%v)", err)
	}
}

func TestTreeOIDIsValid(t *testing.T) {
	if !treeOIDIsValid(strings.Repeat("a", 40)) || !treeOIDIsValid(strings.Repeat("B", 64)) {
		t.Fatal("expected SHA-1 and SHA-256 object ids to be valid")
	}
	for _, value := range []string{"", "../" + strings.Repeat("a", 40), strings.Repeat("g", 40), strings.Repeat("a", 39)} {
		if treeOIDIsValid(value) {
			t.Fatalf("treeOIDIsValid(%q) = true", value)
		}
	}
}

func TestPurgeSkipsAcquiredSnapshot(t *testing.T) {
	requireRealGit(t)
	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)
	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	path, release, err := AcquireSnapshot(tree)
	if err != nil {
		t.Fatal(err)
	}
	if err := PurgeSnapshots(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("acquired snapshot was purged: %v", err)
	}
	release()
	if err := PurgeSnapshots(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("released snapshot was not purged: %v", err)
	}
}

func TestAcquireSnapshotLockReleasedAfterProcessExit(t *testing.T) {
	requireRealGit(t)
	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)
	tree := runGitInDir(t, dir, "rev-parse", "HEAD^{tree}")
	readyPath := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSnapshotLockHelper$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"VCSENTINEL_SNAPSHOT_LOCK_HELPER=1",
		"VCSENTINEL_SNAPSHOT_LOCK_TREE="+tree,
		"VCSENTINEL_SNAPSHOT_LOCK_READY="+readyPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire the snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pathBytes, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatal(err)
	}
	path := string(pathBytes)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := PurgeSnapshots(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot was purged while another process held it: %v", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	waited = true

	if err := PurgeSnapshots(0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("snapshot remained after its owner process exited: %v", err)
	}
}

func TestSnapshotLockHelper(t *testing.T) {
	if os.Getenv("VCSENTINEL_SNAPSHOT_LOCK_HELPER") != "1" {
		return
	}
	path, release, err := AcquireSnapshot(os.Getenv("VCSENTINEL_SNAPSHOT_LOCK_TREE"))
	if err != nil {
		os.Exit(2)
	}
	readyPath := os.Getenv("VCSENTINEL_SNAPSHOT_LOCK_READY")
	tempPath := readyPath + ".tmp"
	if err := os.WriteFile(tempPath, []byte(path), 0600); err != nil {
		os.Exit(3)
	}
	if err := os.Rename(tempPath, readyPath); err != nil {
		os.Exit(4)
	}
	for {
		runtime.KeepAlive(release)
		time.Sleep(time.Second)
	}
}

// TestCreateSnapshotSweepsStaleSnapshotsAndKeepsFreshOnes pins the slice-13
// hygiene rule: every successful creation sweeps snapshot entries older than
// SnapshotRetention while fresh entries survive. Aging uses os.Chtimes so
// the test never sleeps.
func TestCreateSnapshotSweepsStaleSnapshotsAndKeepsFreshOnes(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned an error: %v", err)
	}
	stale, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatalf("CreateSnapshot returned an error: %v", err)
	}
	old := time.Now().Add(-SnapshotRetention - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("could not age the stale seed entry: %v", err)
	}

	published, err := CreateSnapshot(newTree(t, dir))
	if err != nil {
		t.Fatalf("CreateSnapshot returned an error: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("a snapshot aged past retention must be swept by the next creation (err=%v)", err)
	}
	if _, err := os.Stat(published); err != nil {
		t.Errorf("the just-created snapshot must survive its own sweep (err=%v)", err)
	}

	kept, err := CreateSnapshot(newTree(t, dir))
	if err != nil {
		t.Fatalf("CreateSnapshot returned an error: %v", err)
	}
	if _, err := os.Stat(published); err != nil {
		t.Errorf("the previous fresh snapshot must survive the following sweep (err=%v)", err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Errorf("the newly created snapshot must exist (err=%v)", err)
	}
}

// newTree commits a distinct tree in dir and returns its tree OID, so each
// subsequent snapshot lands in its own destination directory. The seeded
// content carries a nanosecond timestamp: two trees never collide even when
// created back to back.
func newTree(t *testing.T, dir string) string {
	t.Helper()
	content := []byte(time.Now().Format(time.RFC3339Nano) + "\n")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), content, 0644); err != nil {
		t.Fatalf("could not write the tree seed file: %v", err)
	}
	runGitInDir(t, dir, "add", "-A")
	runGitInDir(t, dir, "commit", "-q", "-m", "chore: slice 13 tree seed")
	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatalf("TreeOf returned an error: %v", err)
	}
	return tree
}
