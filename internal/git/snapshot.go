package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// snapshotMu serializes in-process snapshot creation and purging. Ticket 14
// diagnosis: the flaky "call N returned a different path" failures were a
// production race, not a test defect. Concurrent CreateSnapshot calls each ran
// their own "git worktree add" into a private temp path and the winner then
// ran "git worktree repair" while sibling calls were still mutating the same
// <git-common-dir>/worktrees administrative area; git does not lock that area
// across commands, so repair intermittently failed with exit status 128 and
// one caller returned an error (empty path) where every caller must receive
// the same deterministic destination. Serializing the whole create-or-reuse
// section removes the intra-process race deterministically without loosening
// any assertion; cross-process safety is unchanged and still relies on the
// atomic temp-checkout + rename publication below.
var snapshotMu sync.Mutex

const snapshotLockDir = "snapshot-locks"

// TreeOf returns the tree OID of a revision.
func TreeOf(revision string) (string, error) {
	// A revision starting with "-" would be interpreted as an option of
	// "git rev-parse" instead of as the revision's name (B14): anyone
	// calling with untrusted input could inject git options.
	if strings.HasPrefix(revision, "-") {
		return "", fmt.Errorf("invalid revision %q: it cannot start with \"-\"", revision)
	}
	out, err := runGitOutput("rev-parse", revision+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// snapshotsDir returns <git-common-dir>/vas-sentinel/snapshots of the
// active repository (the same for every linked worktree, unlike each one's
// private git-dir), creating it if it does not exist.
func snapshotsDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	commonDir, err := GetGitCommonDir(cwd)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(commonDir, "vas-sentinel", "snapshots")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// CreateSnapshot creates (or reuses) a separate Git worktree in detached
// mode for the treeOID tree, inside the repository's common-dir, so that
// any linked worktree of the same repository sees and reuses the same
// snapshot. If the snapshot already exists, it is returned without
// repeating the checkout: that is success, not an error.
//
// Design deviation verified empirically: "git worktree add" requires a
// commit-ish and rejects a bare tree OID ("object ... is a tree, not a
// commit"). That is why the tree is first anchored into a dangling commit
// (no branch, no ref referencing it) with "git commit-tree": that commit
// adds no history, it only serves as a valid entry point for the checkout.
func CreateSnapshot(treeOID string) (string, error) {
	// See snapshotMu: concurrent creation inside one process raced the git
	// worktree administrative area and made repair fail intermittently.
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	snapshots, err := snapshotsDir()
	if err != nil {
		return "", err
	}
	destination := filepath.Join(snapshots, treeOID)
	if info, err := os.Stat(destination); err == nil && info.IsDir() {
		return refreshSnapshot(destination)
	}

	anchorCommit, err := runGitOutput("commit-tree", treeOID, "-m", "vas-sentinel: snapshot")
	if err != nil {
		return "", fmt.Errorf("could not anchor tree %s into a commit for the snapshot: %w", treeOID, err)
	}
	anchorCommit = strings.TrimSpace(anchorCommit)

	// The checkout happens in a temp directory with a unique name (PID +
	// timestamp) and is then renamed to the final name: if two concurrent
	// calls create the same snapshot, each does its own isolated checkout
	// without needing explicit locks, and only one wins the atomic rename;
	// the other discards its temp worktree without error.
	temp := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", treeOID, os.Getpid(), time.Now().UnixNano()))
	if _, err := runGitOutput("worktree", "add", "--detach", temp, anchorCommit); err != nil {
		return "", fmt.Errorf("could not create snapshot worktree %s: %w", treeOID, err)
	}

	if err := os.Rename(temp, destination); err != nil {
		if info, statErr := os.Stat(destination); statErr == nil && info.IsDir() {
			// Another call won the race: the destination already exists. Our
			// own temp worktree is cleaned up (at its original path, still
			// consistent) and the other call's result is reused.
			_, _ = runGitOutput("worktree", "remove", "--force", temp)
			return refreshSnapshot(destination)
		}
		_, _ = runGitOutput("worktree", "remove", "--force", temp)
		return "", fmt.Errorf("could not publish snapshot %s: %w", treeOID, err)
	}

	// The rename leaves Git's reverse reference in its administrative
	// directory stale (it points at the temp path, which no longer
	// exists): without this "repair", "git worktree list"/"remove" do not
	// recognize the final path. Verified empirically with git 2.47.3.
	if _, err := runGitOutput("worktree", "repair", destination); err != nil {
		return "", fmt.Errorf("could not repair snapshot worktree metadata %s: %w", treeOID, err)
	}

	path, err := refreshSnapshot(destination)
	if err != nil {
		return "", err
	}
	// Best-effort hygiene sweep after a successful publish: this is
	// housekeeping, not correctness, so a failed sweep never fails creation.
	_ = purgeSnapshotsLocked(SnapshotRetention)
	return path, nil
}

// AcquireSnapshot keeps a shared OS lock for one validation while it uses a
// snapshot. The release function is idempotent and must be deferred by callers.
func AcquireSnapshot(treeOID string) (string, func(), error) {
	if !treeOIDIsValid(treeOID) {
		return "", nil, fmt.Errorf("invalid snapshot tree object id %q", treeOID)
	}
	snapshots, err := snapshotsDir()
	if err != nil {
		return "", nil, err
	}
	lockPath, err := snapshotLockPath(snapshots, treeOID)
	if err != nil {
		return "", nil, err
	}
	lock, acquired, err := lockSnapshot(lockPath, false, true)
	if err != nil {
		return "", nil, err
	}
	if !acquired {
		return "", nil, errors.New("snapshot shared lock was not acquired")
	}
	path, err := CreateSnapshot(treeOID)
	if err != nil {
		_ = lock.Close()
		return "", nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = lock.Close()
		})
	}
	return path, release, nil
}

func snapshotLockPath(snapshots, treeOID string) (string, error) {
	dir := filepath.Join(filepath.Dir(snapshots), snapshotLockDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, treeOID+".lock"), nil
}

// refreshSnapshot marks a snapshot as actively acquired. Purge uses the
// modification time as its retention boundary, so reusing an old snapshot
// cannot make an overlapping validation look disposable.
func refreshSnapshot(destination string) (string, error) {
	// Retention is a cache optimization, never a reason to reject a usable
	// snapshot when filesystem metadata cannot be updated.
	_ = os.Chtimes(destination, time.Now(), time.Now())
	return destination, nil
}

// SnapshotRetention is the age from which the automatic sweep considers a
// snapshot garbage: snapshots are a disposable per-tree cache
// (CreateSnapshot reuses the existing one), so retention only needs to
// cover reuse within a typical working session, not archiving. It is the
// single source of this threshold.
const SnapshotRetention = 24 * time.Hour

// PurgeSnapshots removes the snapshots of
// <git-common-dir>/vas-sentinel/snapshots whose modification time is older
// than age. They are disposable (recreated by CreateSnapshot), so removal
// is forced when the normal one fails.
func PurgeSnapshots(age time.Duration) error {
	// The purge removes worktrees from the same administrative area the
	// create path mutates, so it shares snapshotMu: a purge running while
	// CreateSnapshot renames or repairs could otherwise make either git
	// command fail on transient administrative state.
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	return purgeSnapshotsLocked(age)
}

// purgeSnapshotsLocked is the purge body proper; the caller must already
// hold snapshotMu. It removes every snapshot directory whose ModTime is
// older than age, best-effort per entry: a snapshot neither the
// normal nor the forced removal can clean accumulates an error instead of
// pretending the disk came out clean (B15), but one broken snapshot never
// blocks the rest of the sweep.
func purgeSnapshotsLocked(age time.Duration) error {
	snapshots, err := snapshotsDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(snapshots)
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-age)
	removedAny := false
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		lockPath, err := snapshotLockPath(snapshots, entry.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		lock, acquired, err := lockSnapshot(lockPath, true, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !acquired {
			continue
		}
		path := filepath.Join(snapshots, entry.Name())
		if _, err := runGitOutput("worktree", "remove", path); err != nil {
			if _, forcedErr := runGitOutput("worktree", "remove", "--force", path); forcedErr != nil {
				// Neither the normal path nor --force could clean this
				// snapshot (B15): the error accumulates instead of
				// returning nil, which would make the caller believe the
				// disk came out clean while the broken directory is still
				// there. The rest of the loop continues: this is
				// per-snapshot best-effort cleaning, one isolated failure
				// must not keep the others from being purged.
				errs = append(errs, fmt.Errorf("could not remove snapshot %s: %w", path, forcedErr))
				_ = lock.Close()
				continue
			}
		}
		_ = lock.Close()
		removedAny = true
	}

	if removedAny {
		_, _ = runGitOutput("worktree", "prune")
	}
	return errors.Join(errs...)
}

func treeOIDIsValid(treeOID string) bool {
	if len(treeOID) != 40 && len(treeOID) != 64 {
		return false
	}
	for _, r := range treeOID {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
