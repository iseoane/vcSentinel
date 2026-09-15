package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, path := range []string{lockPath, snapshotHoldersPath(lockPath), snapshotHoldersLockPath(lockPath)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("reclaim should remove %s, err=%v", path, err)
		}
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

func TestPurgeKeepsSnapshotWithLiveHolder(t *testing.T) {
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
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot with a live holder should survive: %v", err)
	}
}

func TestPurgeRemovesDeadAndKeepsLiveTemporarySnapshots(t *testing.T) {
	_, tree := snapshotTestTree(t)
	snapshots := snapshotDirectoryForTest(t)
	deadPath := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", tree, processThatExited(t), time.Now().UnixNano()))
	livePath := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", tree, os.Getpid(), time.Now().UnixNano()))
	for _, path := range []string{deadPath, livePath} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
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
