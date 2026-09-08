package reviewsnapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// storeDirName names the directory under os.TempDir() that holds every shared
// review snapshot. It deliberately does NOT carry the legacy
// vas-sentinel-review- prefix: that prefix belongs to the per-invocation
// directories old versions of this package created, and the reaper treats
// prefix matches as purely age-based residue, while store entries always get
// the lock-aware treatment below.
const storeDirName = "vas-sentinel-snapshots"

// Store entry naming. The published tree for a SHA is "sha-<sha>", its
// readiness marker "sha-<sha>.ready", and its advisory lock file
// "sha-<sha>.lock"; materialization happens in "staging-<sha>~<random>" so
// the reaper can always attribute abandoned staging residue to the SHA whose
// lock protects its removal. "~" separates the SHA from the random suffix
// because git refnames (the one input here that is not guaranteed hex) can
// never contain it, so the split back out is unambiguous.
const (
	publishedPrefix = "sha-"
	stagingPrefix   = "staging-"
	readySuffix     = ".ready"
	lockSuffix      = ".lock"
)

// storeRoot resolves the shared snapshot store for the current os.TempDir(),
// which tests retarget through TMPDIR, at every call.
func storeRoot() string {
	return filepath.Join(os.TempDir(), storeDirName)
}

func publishedPath(root, sha string) string {
	return filepath.Join(root, publishedPrefix+sha)
}

func markerPath(root, sha string) string {
	return filepath.Join(root, publishedPrefix+sha+readySuffix)
}

func lockPath(root, sha string) string {
	return filepath.Join(root, publishedPrefix+sha+lockSuffix)
}

// stagingSHA recovers the SHA a staging directory was created for, so the
// reaper can honor that SHA's lock before touching abandoned staging residue.
// It returns "" for names it cannot attribute, and the reaper then leaves the
// entry alone: nothing unattributable is ever deleted.
func stagingSHA(name string) string {
	rest := strings.TrimPrefix(name, stagingPrefix)
	if rest == name {
		return ""
	}
	if i := strings.LastIndex(rest, "~"); i > 0 {
		return rest[:i]
	}
	return ""
}

// validObjectID reports whether sha is a full hexadecimal Git object id in
// its canonical form: 40 hex characters for SHA-1 repositories, 64 for
// SHA-256. The object id is the store's storage key and is interpolated into
// file and directory names, so anything else — abbreviations, revspecs such
// as HEAD, names carrying separators — must never reach the store: git would
// happily resolve some of them, and a non-canonical key would make the
// layout ambiguous for creation, leasing, and reaping alike.
func validObjectID(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for i := 0; i < len(sha); i++ {
		c := sha[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// snapshotComplete reports whether dir is a fully published tree: the
// directory must exist AND the readiness marker published just before its
// rename must exist beside it. Materialization never writes into the
// published name — the tree appears there only by an atomic rename of a
// finished staging directory — so a crash can leave at most a marker without
// a tree, or residue the reaper owns; it can never leave a directory that
// passes this check while incomplete.
func snapshotComplete(dir, marker string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(marker)
	return err == nil
}

// The in-process side of the store. leaseHolders counts how many live leases
// this process holds per SHA, so the last release knows to attempt removal;
// the advisory lock remains the cross-process authority (a release only
// deletes after its exclusive attempt proves no other process leases the
// tree). creating tracks in-flight materializations so concurrent Create
// calls for one SHA single-flight behind one staging directory instead of
// racing each other through git.
var (
	storeMu      sync.Mutex
	leaseHolders = map[string]int{}
	creating     = map[string]*snapshotCreation{}
)

// snapshotCreation is one in-flight materialization. Followers wait on done;
// err carries the leader's outcome so a follower can retry when the leader
// was merely canceled (its own context may still be alive) but fail fast when
// the leader hit a deterministic error such as an unknown SHA.
type snapshotCreation struct {
	done chan struct{}
	err  error
}

// snapshotLease is one caller's hold on a published tree. The shared lock is
// held for exactly as long as the lease lives: it is what tells the reaper
// and every other process's final release that this tree is still in use.
type snapshotLease struct {
	root        string
	sha         string
	dir         string
	lock        *snapshotFileLock
	releaseOnce sync.Once
}

// leasePublishedSnapshot takes one caller's lease on the published tree for
// sha when a complete one exists on disk. It returns a nil lease (with a nil
// error) when nothing is published for the SHA right now or when an exclusive
// holder — a creator, the reaper, a final release — is mid-transition, telling
// Create to go through publication; it returns an error only when the lock
// file itself cannot be used.
func leasePublishedSnapshot(sha string) (*snapshotLease, error) {
	root := storeRoot()
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		// No store yet: nothing can be published.
		return nil, nil
	}
	lock, acquired, err := lockSnapshot(lockPath(root, sha), false)
	if err != nil {
		return nil, fmt.Errorf("lease review snapshot for %q: %w", sha, err)
	}
	if !acquired {
		return nil, nil
	}
	published, marker := publishedPath(root, sha), markerPath(root, sha)
	if !snapshotComplete(published, marker) {
		_ = lock.Close()
		return nil, nil
	}
	storeMu.Lock()
	leaseHolders[sha]++
	storeMu.Unlock()
	return &snapshotLease{root: root, sha: sha, dir: published, lock: lock}, nil
}

// release is the cleanup Create returns, one lease at a time. It is
// idempotent: a second call does nothing. It only drops this caller's lease —
// the in-process holder count and this handle's shared lock — and never
// deletes anything: the published snapshot, its readiness marker and its
// lock file stay on disk so the next invocation auditing the same SHA (a
// format or transport retry, a second provider, a later review) leases the
// very same tree. The lock-aware stale reaper is the only thing that ever
// removes a published tree, once no lease in any process holds it and its
// mtime is past the stale threshold.
func (l *snapshotLease) release() {
	l.releaseOnce.Do(func() {
		storeMu.Lock()
		leaseHolders[l.sha]--
		if leaseHolders[l.sha] <= 0 {
			delete(leaseHolders, l.sha)
		}
		storeMu.Unlock()
		_ = l.lock.Close()
	})
}

// publishSnapshotForSHA makes sure a complete published tree for sha exists
// by the time it returns without error, materializing it only when nothing
// reusable is on disk. Concurrent Create calls in this process single-flight:
// the first caller for a SHA leads the materialization while the others wait
// on it. A follower whose leader published returns immediately, so Create
// leases the tree the leader just made — re-entering creation here instead
// would deadlock behind that leader's own live lease. A follower whose leader
// was merely canceled retries the cycle, because its own context may still be
// alive; one whose leader hit a deterministic error fails with that same
// error instead of repeating it. Across processes, the SHA's exclusive lock
// serializes creators, and a creator that finds the tree already published
// simply returns.
func publishSnapshotForSHA(ctx context.Context, worktree, sha string) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("review snapshot aborted: %w", err)
		}
		storeMu.Lock()
		creation, waiting := creating[sha]
		if !waiting {
			creation = &snapshotCreation{done: make(chan struct{})}
			creating[sha] = creation
		}
		storeMu.Unlock()

		if waiting {
			select {
			case <-creation.done:
				// The caller's own context outranks the leader's outcome.
				if err := ctx.Err(); err != nil {
					return fmt.Errorf("review snapshot aborted: %w", err)
				}
				if creation.err == nil {
					// Published: back to Create, which leases the tree.
					return nil
				}
				if errors.Is(creation.err, context.Canceled) ||
					errors.Is(creation.err, context.DeadlineExceeded) {
					// The leader was merely canceled; this caller's context
					// is still alive, so retry the cycle.
					continue
				}
				return creation.err
			case <-ctx.Done():
				return fmt.Errorf("review snapshot aborted: %w", ctx.Err())
			}
		}

		err := materializeAndPublish(ctx, worktree, sha)
		storeMu.Lock()
		delete(creating, sha)
		storeMu.Unlock()
		creation.err = err
		close(creation.done)
		return err
	}
}

// materializeAndPublish is the leader's work: under the SHA's exclusive lock,
// reuse any complete published tree, or materialize the committed tree into a
// staging directory and atomically rename it into place. The exclusive lock
// is held across the whole materialization so cross-process creators for one
// SHA take turns instead of duplicating git work, and so the reaper can never
// mistake a live staging directory for stale residue: while the lock is
// held, that directory is work in flight, not garbage.
func materializeAndPublish(ctx context.Context, worktree, sha string) error {
	root := storeRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create review snapshot store: %w", err)
	}
	lock, acquired, err := lockSnapshot(lockPath(root, sha), true)
	if err != nil {
		return fmt.Errorf("lock review snapshot for %q: %w", sha, err)
	}
	for !acquired {
		// Another process is creating, reaping, or releasing this SHA. Wait
		// for it while the caller's context stays alive; whoever holds the
		// lock either publishes the tree or frees the way.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("review snapshot aborted: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
		lock, acquired, err = lockSnapshot(lockPath(root, sha), true)
		if err != nil {
			return fmt.Errorf("lock review snapshot for %q: %w", sha, err)
		}
	}
	defer lock.Close()

	published, marker := publishedPath(root, sha), markerPath(root, sha)
	if snapshotComplete(published, marker) {
		return nil
	}
	// A directory without its readiness marker is residue from a publisher
	// that died before its rename (or a marker someone removed). No lease can
	// hold it — a lease holds the shared lock, and we just acquired the
	// exclusive one — so it is safe to discard and republish over it.
	_ = os.RemoveAll(published)
	_ = os.Remove(marker)

	entries, err := gitTreeEntries(ctx, worktree, sha)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("review snapshot aborted: %w", ctxErr)
		}
		return err
	}
	regularFiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.objectType != "blob" || !strings.HasPrefix(entry.mode, "100") {
			continue
		}
		if clean, ok := safePath(entry.path); ok && clean == entry.path {
			regularFiles = append(regularFiles, entry.path)
		}
	}
	staging, err := os.MkdirTemp(root, stagingPrefix+sha+"~")
	if err != nil {
		return fmt.Errorf("create review snapshot: %w", err)
	}
	if err := materializeTree(ctx, worktree, sha, staging, regularFiles); err != nil {
		_ = os.RemoveAll(staging)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("review snapshot aborted: %w", ctxErr)
		}
		return err
	}
	// Publish the readiness marker BEFORE the rename: once the directory
	// appears under its SHA name it must already be identifiable as complete,
	// because that is the moment any other process may lease it.
	if err := os.WriteFile(marker, []byte("ready\n"), 0o600); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("publish review snapshot: %w", err)
	}
	if err := os.Rename(staging, published); err != nil {
		_ = os.RemoveAll(staging)
		if _, statErr := os.Stat(published); statErr == nil {
			// Lost a rename race that the exclusive lock rules out in
			// theory; the winner's tree is published and complete, which is
			// all this call owed its caller.
			return nil
		}
		return fmt.Errorf("publish review snapshot: %w", err)
	}
	return nil
}

// isStaleStoreEntry reports whether a store entry is old enough to be
// considered abandoned. A live review can never outlive review.timeout (300s
// by default), so anything older than maxAge is definitively residue from a
// process that died before its cleanup ran, never work in flight.
func isStaleStoreEntry(entry os.DirEntry, now time.Time, maxAge time.Duration) bool {
	info, err := entry.Info()
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) >= maxAge
}

// reapSharedStore is the lock-aware reaper for the shared store. A stale
// entry — a published tree, a staging directory, or an orphaned readiness
// marker with no tree behind it — is removed only after the reaper acquires
// that SHA's nonblocking exclusive lock, so an entry a live lease still holds
// in any process is skipped no matter how old it looks, and an entry a live
// creator is materializing is protected by the very same exclusive hold. The
// legacy per-invocation snapshot directories keep their older, purely
// age-based cleanup in reapAbandonedSnapshots.
func reapSharedStore(root string, now time.Time, maxAge time.Duration) int {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir() && strings.HasPrefix(name, publishedPrefix) && !strings.HasSuffix(name, readySuffix):
			sha := strings.TrimPrefix(name, publishedPrefix)
			if sha == "" || !validObjectID(sha) || !isStaleStoreEntry(entry, now, maxAge) {
				continue
			}
			if removeUnleasedStoreEntry(root, sha, filepath.Join(root, name), markerPath(root, sha)) {
				removed++
			}
		case entry.IsDir() && strings.HasPrefix(name, stagingPrefix):
			sha := stagingSHA(name)
			if sha == "" || !validObjectID(sha) || !isStaleStoreEntry(entry, now, maxAge) {
				continue
			}
			if removeUnleasedStoreEntry(root, sha, filepath.Join(root, name)) {
				removed++
			}
		case !entry.IsDir() && strings.HasPrefix(name, publishedPrefix) && strings.HasSuffix(name, readySuffix):
			sha := strings.TrimSuffix(strings.TrimPrefix(name, publishedPrefix), readySuffix)
			if sha == "" || !validObjectID(sha) || !isStaleStoreEntry(entry, now, maxAge) {
				continue
			}
			// A marker whose tree exists belongs to that tree and is removed
			// together with it by the published-tree branch above.
			if _, err := os.Stat(publishedPath(root, sha)); err == nil {
				continue
			}
			if removeUnleasedStoreEntry(root, sha, filepath.Join(root, name)) {
				removed++
			}
		}
	}
	return removed
}

// removeUnleasedStoreEntry removes the given store entries for sha only while
// nothing leases them anywhere: the nonblocking exclusive lock on the SHA's
// lock file succeeds only when no shared lease exists, and while it is held,
// no creator or reaper can be inside the same entries either. Only the
// snapshot, marker, and staging targets go away — never the lock file.
// Unlinking a lock file while its inode is locked would break the mutual
// exclusion the whole store rests on: a party holding the old file open
// could lock the orphaned inode while a later opener created a fresh inode,
// and the two would no longer exclude each other. Lock files are therefore
// persistent zero-byte files, one per object id ever audited, keeping the
// lock namespace stable for the lifetime of the store.
func removeUnleasedStoreEntry(root, sha string, targets ...string) bool {
	lock, acquired, err := lockSnapshot(lockPath(root, sha), true)
	if err != nil || !acquired {
		return false
	}
	defer lock.Close()
	for _, target := range targets {
		_ = os.RemoveAll(target)
	}
	return true
}
