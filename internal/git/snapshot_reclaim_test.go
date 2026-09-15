package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPurgeReclaimsDeadHolderFreshSnapshot(t *testing.T) {
	_, tree := snapshotTestTree(t)
	path, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotHolders(t, tree, processThatExited(t))

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("fresh snapshot with a dead holder should be reclaimed, err=%v", err)
	}
	lockPath := snapshotLockPathForTest(t, tree)
	for _, path := range []string{snapshotHoldersPath(lockPath), snapshotHoldersLockPath(lockPath)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("reclaim should remove %s, err=%v", path, err)
		}
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("reclaim should retain the primary lock file, err=%v", err)
	}
}

func TestPurgeKeepsFreshSnapshotsWithoutHolders(t *testing.T) {
	dir, firstTree := snapshotTestTree(t)
	missing, err := CreateSnapshot(firstTree)
	if err != nil {
		t.Fatal(err)
	}
	emptyTree := newTree(t, dir)
	empty, err := CreateSnapshot(emptyTree)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := snapshotLockPathForTest(t, emptyTree)
	if err := os.WriteFile(snapshotHoldersPath(lockPath), nil, 0600); err != nil {
		t.Fatal(err)
	}

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{missing, empty} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("fresh snapshot without a holder should survive: %s: %v", path, err)
		}
	}
}

func TestCreateSnapshotRejectsMalformedTreeOID(t *testing.T) {
	_, _ = snapshotTestTree(t)
	snapshots := snapshotDirectoryForTest(t)
	root := filepath.Dir(snapshots)
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	malformed := "../" + strings.Repeat("a", 40)
	if _, err := CreateSnapshot(malformed); err == nil || !strings.Contains(err.Error(), "invalid snapshot tree object id") {
		t.Fatalf("CreateSnapshot(%q) error = %v, expected invalid tree OID", malformed, err)
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("malformed tree OID changed the snapshot layout: before=%d after=%d", len(before), len(after))
	}
}

func TestPurgeKeepsSnapshotWithLiveHolder(t *testing.T) {
	_, tree := snapshotTestTree(t)
	path, release, err := AcquireSnapshot(tree)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fresh snapshot with a live holder should survive: %v", err)
	}
}

func TestPurgeKeepsPrimarySnapshotLockFile(t *testing.T) {
	_, tree := snapshotTestTree(t)
	path, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-SnapshotRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lockPath := snapshotLockPathForTest(t, tree)

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("the primary snapshot lock file must remain as a stable rendezvous point: %v", err)
	}
}

func TestPurgeDoesNotRemoveAgedTemporaryWithPublicationLock(t *testing.T) {
	requireRealGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := buildGitShim(t)
	dir, tree := snapshotTestTree(t)
	readyPath := filepath.Join(t.TempDir(), "ready")
	releasePath := filepath.Join(t.TempDir(), "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCreateSnapshotPublicationHelper$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"VAS_SENTINEL_CREATE_SNAPSHOT_HELPER=1",
		"VAS_SENTINEL_CREATE_SNAPSHOT_TREE="+tree,
		"VAS_SENTINEL_REAL_GIT="+realGit,
		"VAS_SENTINEL_GIT_SHIM_BLOCK_ADD=1",
		"VAS_SENTINEL_GIT_SHIM_READY="+readyPath,
		"VAS_SENTINEL_GIT_SHIM_RELEASE="+releasePath,
		"PATH="+shimDir+string(os.PathListSeparator)+os.Getenv("PATH"),
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
	waitForFile(t, readyPath)

	snapshots := snapshotDirectoryForTest(t)
	prefix := fmt.Sprintf(".%s.tmp-%d-", tree, cmd.Process.Pid)
	entries, err := os.ReadDir(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			path = filepath.Join(snapshots, entry.Name())
			break
		}
	}
	if path == "" {
		t.Fatalf("CreateSnapshot helper did not publish a temporary worktree")
	}
	old := time.Now().Add(-SnapshotRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("an aged temporary being published by CreateSnapshot must survive: %v", err)
	}
	if err := os.WriteFile(releasePath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("CreateSnapshot helper failed after release: %v", err)
	}
	waited = true
}

func TestCreateSnapshotPublicationHelper(t *testing.T) {
	if os.Getenv("VAS_SENTINEL_CREATE_SNAPSHOT_HELPER") == "1" {
		if _, err := CreateSnapshot(os.Getenv("VAS_SENTINEL_CREATE_SNAPSHOT_TREE")); err != nil {
			os.Exit(2)
		}
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper did not signal readiness: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPurgeReclaimsAgedSnapshotWithLiveHolder(t *testing.T) {
	_, tree := snapshotTestTree(t)
	path, err := CreateSnapshot(tree)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-SnapshotRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	writeSnapshotHolders(t, tree, os.Getpid())

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an aged snapshot must be reclaimable despite a live recorded PID, err=%v", err)
	}
}

func TestPurgeReclaimsAgedLiveTemporarySnapshot(t *testing.T) {
	dir, tree := snapshotTestTree(t)
	path := filepath.Join(snapshotDirectoryForTest(t), fmt.Sprintf(".%s.tmp-%d-%d", tree, os.Getpid(), time.Now().UnixNano()))
	runGitInDir(t, dir, "worktree", "add", "--detach", filepath.ToSlash(path), "HEAD")
	old := time.Now().Add(-SnapshotRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("an aged temporary snapshot must be reclaimable despite a live recorded PID, err=%v", err)
	}
	listing := runGitInDir(t, dir, "worktree", "list")
	if strings.Contains(listing, filepath.Base(path)) {
		t.Fatalf("aged temporary snapshot administrative record remains:\n%s", listing)
	}
}

func TestPurgeRepairsInterruptedSnapshotPublication(t *testing.T) {
	dir, tree := snapshotTestTree(t)
	snapshots := snapshotDirectoryForTest(t)
	temp := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", tree, os.Getpid(), time.Now().UnixNano()))
	path := filepath.Join(snapshots, tree)
	runGitInDir(t, dir, "worktree", "add", "--detach", filepath.ToSlash(temp), "HEAD")
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-SnapshotRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatalf("interrupted publication should be repaired and reclaimed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("interrupted snapshot directory should be removed, err=%v", err)
	}
	listing := runGitInDir(t, dir, "worktree", "list")
	if strings.Contains(listing, filepath.ToSlash(temp)) || strings.Contains(listing, filepath.ToSlash(path)) {
		t.Fatalf("interrupted snapshot administrative record remains:\n%s", listing)
	}
}

func TestPurgeCleansSidecarsWhenPruneFails(t *testing.T) {
	requireRealGit(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := buildGitShim(t)
	dir, tree := snapshotTestTree(t)
	snapshots := snapshotDirectoryForTest(t)
	temp := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", tree, os.Getpid(), time.Now().UnixNano()))
	path := filepath.Join(snapshots, tree)
	runGitInDir(t, dir, "worktree", "add", "--detach", filepath.ToSlash(temp), "HEAD")
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-SnapshotRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	lockPath := snapshotLockPathForTest(t, tree)
	for _, sidecar := range []string{snapshotHoldersPath(lockPath), snapshotHoldersLockPath(lockPath)} {
		if err := os.WriteFile(sidecar, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("VAS_SENTINEL_REAL_GIT", realGit)
	t.Setenv("VAS_SENTINEL_GIT_SHIM_FAIL", "1")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := PurgeSnapshots(SnapshotRetention); err == nil {
		t.Fatal("a failed prune must remain visible to the caller")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("snapshot directory should be removed despite the prune failure, err=%v", err)
	}
	for _, sidecar := range []string{snapshotHoldersPath(lockPath), snapshotHoldersLockPath(lockPath)} {
		if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
			t.Errorf("removed snapshot sidecar remains: %s: %v", sidecar, err)
		}
	}
}

func buildGitShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "git-shim.go")
	const program = `package main

import (
	"os"
	"os/exec"
	"time"
)

func main() {
	args := os.Args[1:]
	if os.Getenv("VAS_SENTINEL_GIT_SHIM_FAIL") == "1" && len(args) >= 2 && args[0] == "worktree" && (args[1] == "repair" || args[1] == "prune") {
		os.Exit(42)
	}
	cmd := exec.Command(os.Getenv("VAS_SENTINEL_REAL_GIT"), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
	if os.Getenv("VAS_SENTINEL_GIT_SHIM_BLOCK_ADD") == "1" && len(args) >= 2 && args[0] == "worktree" && args[1] == "add" {
		if err := os.WriteFile(os.Getenv("VAS_SENTINEL_GIT_SHIM_READY"), nil, 0600); err != nil {
			os.Exit(3)
		}
		for {
			if _, err := os.Stat(os.Getenv("VAS_SENTINEL_GIT_SHIM_RELEASE")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	command := exec.Command("go", "build", "-o", path, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("could not build git shim: %v\n%s", err, output)
	}
	return dir
}

func TestPurgeRemovesDeadAndKeepsLiveTemporarySnapshots(t *testing.T) {
	dir, tree := snapshotTestTree(t)
	snapshots := snapshotDirectoryForTest(t)
	deadPath := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", tree, processThatExited(t), time.Now().UnixNano()))
	livePath := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", tree, os.Getpid(), time.Now().UnixNano()))
	for _, path := range []string{deadPath, livePath} {
		runGitInDir(t, dir, "worktree", "add", "--detach", filepath.ToSlash(path), "HEAD")
	}

	if err := PurgeSnapshots(SnapshotRetention); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deadPath); !os.IsNotExist(err) {
		t.Errorf("temporary snapshot with a dead PID should be removed, err=%v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Errorf("temporary snapshot with a live PID should survive, err=%v", err)
	}
	listing := runGitInDir(t, dir, "worktree", "list")
	if strings.Contains(listing, filepath.Base(deadPath)) {
		t.Errorf("removed temporary snapshot administrative record remains:\n%s", listing)
	}
	if !strings.Contains(listing, filepath.Base(livePath)) {
		t.Errorf("live temporary snapshot administrative record disappeared:\n%s", listing)
	}
}

func TestAcquireSnapshotRecordsAndReleasesHolder(t *testing.T) {
	_, tree := snapshotTestTree(t)
	_, release, err := AcquireSnapshot(tree)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := snapshotLockPathForTest(t, tree)
	holdersPath := snapshotHoldersPath(lockPath)
	data, err := os.ReadFile(holdersPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != fmt.Sprint(os.Getpid()) {
		t.Fatalf("holders file = %q, expected current PID %d", data, os.Getpid())
	}

	release()
	release()
	data, err = os.ReadFile(holdersPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "" {
		t.Fatalf("released PID remains in holders file: %q", data)
	}
}

func snapshotTestTree(t *testing.T) (string, string) {
	t.Helper()
	requireRealGit(t)
	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)
	tree, err := TreeOf("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return dir, tree
}

func snapshotDirectoryForTest(t *testing.T) string {
	t.Helper()
	path, err := snapshotsDir()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func snapshotLockPathForTest(t *testing.T, tree string) string {
	t.Helper()
	path, err := snapshotLockPath(snapshotDirectoryForTest(t), tree)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeSnapshotHolders(t *testing.T, tree string, pids ...int) {
	t.Helper()
	lines := make([]string, len(pids))
	for i, pid := range pids {
		lines[i] = fmt.Sprint(pid)
	}
	if err := os.WriteFile(snapshotHoldersPath(snapshotLockPathForTest(t, tree)), []byte(strings.Join(lines, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
}

// processThatExited records a child PID before waiting for the helper to exit;
// the reaped child PID is then certainly dead for the reclaim test.
func processThatExited(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSnapshotExitedProcessHelper$")
	cmd.Env = append(os.Environ(), "VAS_SENTINEL_SNAPSHOT_EXIT_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper process unexpectedly exited successfully")
	}
	return pid
}

func TestSnapshotExitedProcessHelper(t *testing.T) {
	if os.Getenv("VAS_SENTINEL_SNAPSHOT_EXIT_HELPER") == "1" {
		os.Exit(1)
	}
}
