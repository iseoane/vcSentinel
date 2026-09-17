package reviewsnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// storeDirName names the directory under os.TempDir() that holds every shared
// review snapshot. It deliberately does NOT carry the legacy
// vas-sentinel-review- prefix: that prefix belongs to the per-invocation
// directories old versions of this package created, and the reaper treats
// prefix matches as purely age-based residue, while store entries always get
// the lock-aware treatment below. The platform files make the root per-UID
// and fail closed on insecure layouts.
const storeDirName = "vas-sentinel-snapshots"

// Store entry naming. The published tree for a SHA is "sha-<sha>", its
// readiness marker "sha-<sha>.ready", its readiness manifest
// "sha-<sha>.manifest", and its advisory lock file "sha-<sha>.lock";
// materialization happens in "staging-<sha>~<random>" so the reaper can
// always attribute abandoned staging residue to the SHA whose lock protects
// its removal. "~" separates the SHA from the random suffix because git
// refnames (the one input here that is not guaranteed hex) can never contain
// it, so the split back out is unambiguous.
const (
	publishedPrefix = "sha-"
	stagingPrefix   = "staging-"
	readySuffix     = ".ready"
	manifestSuffix  = ".manifest"
	lockSuffix      = ".lock"
	// ProviderStatePrefix identifies per-invocation provider state roots.
	// Unlike published snapshots, no lease is shared across these directories.
	ProviderStatePrefix = "vas-sentinel-review-provider-"
)

// ProviderStatePattern builds the os.MkdirTemp pattern for a provider state
// directory owned by pid. The owner travels in the directory NAME because that
// is the only thing the reaper can read from a ReadDir alone: a sidecar file
// would add a write that a killed process can skip, which is exactly the case
// this name exists to survive. It is the contract between the creator in
// internal/agentadapter and ProviderStatePID below; changing one changes both.
func ProviderStatePattern(pid int) string {
	return fmt.Sprintf("%s%d-", ProviderStatePrefix, pid)
}

// ProviderStatePID reports the owning process recorded in a provider state
// directory name, and whether one is recorded at all. A false result is a real
// answer rather than a failure: a directory created before this format, or by
// anything else, carries no owner and the caller falls back to the age
// ceiling, so nothing that was collected before stops being collected.
func ProviderStatePID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, ProviderStatePrefix)
	if !ok {
		return 0, false
	}
	owner, suffix, ok := strings.Cut(rest, "-")
	if !ok || suffix == "" {
		return 0, false
	}
	pid, err := strconv.ParseUint(owner, 10, 31)
	if err != nil || pid == 0 {
		return 0, false
	}
	return int(pid), true
}

func publishedPath(root, sha string) string {
	return filepath.Join(root, publishedPrefix+sha)
}

func markerPath(root, sha string) string {
	return filepath.Join(root, publishedPrefix+sha+readySuffix)
}

func manifestPath(root, sha string) string {
	return filepath.Join(root, publishedPrefix+sha+manifestSuffix)
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

// isCommittedRegularFile reports whether a git ls-tree entry is a committed
// regular file — the only kind of entry materialized into a snapshot and the
// only kind an audited path may resolve to. It is shared by whole-tree
// materialization and allowed-path filtering so the two can never diverge on
// what counts as committed evidence.
func isCommittedRegularFile(mode, objectType string) bool {
	return objectType == "blob" && strings.HasPrefix(mode, "100")
}

// manifestEntry is one committed regular file's expected on-disk evidence:
// its published permission and its byte size. Contents are deliberately not
// hashed — lease validation compares structure and metadata only, which is
// exactly what catches added, removed, symlinked, mode-changed, and
// size-changed entries without paying a hash per lease.
type manifestEntry struct {
	perm fs.FileMode
	size int64
}

// writeManifest walks the finished staging tree once and, in that single
// pass, tightens every regular file to its published permission and records
// the file's committed mode and size. The directory tree keeps its
// owner-traversable 0700 directories so the snapshot remains a usable working
// directory for the reviewer process. Records are NUL-terminated
// "<git-mode> <size> <path>" lines, so any committed path — spaces, tabs,
// newlines included — round-trips losslessly.
func writeManifest(staging, manifest string, modes map[string]string) error {
	var buf []byte
	walkErr := filepath.WalkDir(staging, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unexpected non-regular entry %q in staged snapshot", p)
		}
		rel, err := filepath.Rel(staging, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		gitMode, ok := modes[rel]
		if !ok {
			return fmt.Errorf("unexpected staged file %q with no committed mode", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		perm := publishedPerm(gitMode)
		if err := os.Chmod(p, perm); err != nil {
			return err
		}
		buf = append(buf, []byte(fmt.Sprintf("%s %d %s\x00", gitMode, info.Size(), rel))...)
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	return os.WriteFile(manifest, buf, 0o400)
}

// readManifest parses a readiness manifest into the expected evidence map.
func readManifest(manifest string) (map[string]manifestEntry, error) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]manifestEntry)
	for _, record := range bytes.Split(data, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		mode, rest, ok := bytes.Cut(record, []byte{' '})
		sizeBytes, path, ok2 := bytes.Cut(rest, []byte{' '})
		if !ok || !ok2 || len(path) == 0 {
			return nil, fmt.Errorf("malformed readiness manifest record %q", record)
		}
		size, err := strconv.ParseInt(string(sizeBytes), 10, 64)
		if err != nil || size < 0 {
			return nil, fmt.Errorf("malformed readiness manifest record %q", record)
		}
		gitMode := string(mode)
		if gitMode != "100644" && gitMode != "100755" {
			return nil, fmt.Errorf("malformed readiness manifest record %q", record)
		}
		entries[string(path)] = manifestEntry{perm: publishedPerm(gitMode), size: size}
	}
	return entries, nil
}

// publishedSnapshotUsable reports whether a published tree may be leased:
// the readiness marker must exist, the manifest must parse, and the on-disk
// tree must match the manifest exactly — no added or removed entries, no
// symlinks or other non-regular files, no mode or size drift. This is
// accidental-corruption defense, not tamper-proofing: the manifest and the
// tree share the same owner's write capability, so a same-UID actor could
// rewrite both in concert — which is also why contents are not hashed per
// lease (full-tree I/O without creating a boundary). A tree that fails here
// is corrupted cache, and the caller rebuilds it from Git under the per-SHA
// transition lock.
func publishedSnapshotUsable(dir, marker, manifest string) bool {
	if _, err := os.Stat(marker); err != nil {
		return false
	}
	expected, err := readManifest(manifest)
	if err != nil {
		return false
	}
	expectedDirs := make(map[string]bool)
	for relPath := range expected {
		for dir := relPath; ; {
			dir = path.Dir(dir)
			if dir == "." || dir == "/" {
				break
			}
			expectedDirs[dir] = true
		}
	}
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if !expectedDirs[rel] {
				return fmt.Errorf("unexpected directory %q in published snapshot", rel)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular entry %q in published snapshot", rel)
		}
		want, ok := expected[rel]
		if !ok {
			return fmt.Errorf("unexpected file %q in published snapshot", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() != want.size {
			return fmt.Errorf("size drift for %q in published snapshot", rel)
		}
		if !publishedPermMatches(info.Mode().Perm(), want.perm) {
			return fmt.Errorf("mode drift for %q in published snapshot", rel)
		}
		delete(expected, rel)
		return nil
	})
	if walkErr != nil {
		return false
	}
	return len(expected) == 0
}

// creating tracks in-flight materializations so concurrent Create calls for
// one SHA single-flight behind one staging directory instead of racing each
// other through git. storeMu guards it; the advisory lock files remain the
// cross-process authority for every other transition.
var (
	storeMu  sync.Mutex
	creating = map[string]*snapshotCreation{}
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
// and every other process's transitions that this tree is still in use.
type snapshotLease struct {
	dir         string
	lock        *snapshotFileLock
	releaseOnce sync.Once
}

// leasePublishedSnapshot takes one caller's lease on the published tree for
// sha when a complete, untampered one exists on disk. It returns a nil lease
// (with a nil error) when nothing usable is published for the SHA right now
// or when an exclusive holder — a creator, the reaper, a rebuild — is
// mid-transition, telling Create to go through publication; it returns an
// error when the store root fails its safety checks or the lock file itself
// cannot be used.
func leasePublishedSnapshot(sha string) (*snapshotLease, error) {
	root := storeRoot()
	if _, err := os.Stat(root); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect review snapshot store: %w", err)
		}
		// No store yet: nothing can be published.
		return nil, nil
	}
	if err := checkStoreRoot(root); err != nil {
		return nil, err
	}
	lock, acquired, err := lockSnapshot(lockPath(root, sha), false)
	if err != nil {
		return nil, fmt.Errorf("lease review snapshot for %q: %w", sha, err)
	}
	if !acquired {
		return nil, nil
	}
	published, marker, manifest := publishedPath(root, sha), markerPath(root, sha), manifestPath(root, sha)
	if !publishedSnapshotUsable(published, marker, manifest) {
		_ = lock.Close()
		return nil, nil
	}
	// Directory mtime records the most recent successful lease. The capacity
	// reaper uses it to evict the least recently leased unleased tree first.
	if err := os.Chtimes(published, time.Now(), time.Now()); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("touch leased review snapshot for %q: %w", sha, err)
	}
	return &snapshotLease{dir: published, lock: lock}, nil
}

// release is the cleanup Create returns, one lease at a time. It is
// idempotent: a second call does nothing. It only drops this handle's shared
// lock and never deletes anything: the published snapshot, its marker, its
// manifest and its lock file stay on disk so the next invocation auditing
// the same SHA (a format or transport retry, a second provider, a later
// review) leases the very same tree. The lock-aware stale reaper is the only
// thing that ever removes a published tree, once no lease in any process
// holds it and its mtime is past the stale threshold.
func (l *snapshotLease) release() {
	l.releaseOnce.Do(func() {
		_ = l.lock.Close()
	})
}

// publishSnapshotForSHA makes sure a complete, untampered published tree for
// sha exists by the time it returns without error, materializing it only
// when nothing reusable is on disk. Concurrent Create calls in this process
// single-flight: the first caller for a SHA leads the materialization while
// the others wait on it. A follower whose leader published returns
// immediately, so Create leases the tree the leader just made — re-entering
// creation here instead would deadlock behind that leader's own live lease.
// A follower whose leader was merely canceled retries the cycle, because its
// own context may still be alive; one whose leader hit a deterministic error
// fails with that same error instead of repeating it. Across processes, the
// SHA's exclusive lock serializes creators, and a creator that finds a usable
// tree already published simply returns.
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
// reuse any complete, untampered published tree, or materialize the committed
// tree into a staging directory, tighten it to immutable evidence, and
// atomically rename it into place. The exclusive lock is held across the
// whole materialization so cross-process creators for one SHA take turns
// instead of duplicating git work, and so the reaper can never mistake a live
// staging directory for stale residue: while the lock is held, that directory
// is work in flight, not garbage. A published tree that fails validation —
// marker or manifest missing, entries added, removed, symlinked, or drifted —
// is discarded and rebuilt from Git under the same lock, so a corrupted cache
// is never handed out and never serves twice.
func materializeAndPublish(ctx context.Context, worktree, sha string) error {
	root := storeRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create review snapshot store: %w", err)
	}
	if err := checkStoreRoot(root); err != nil {
		return err
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

	published, marker, manifest := publishedPath(root, sha), markerPath(root, sha), manifestPath(root, sha)
	if publishedSnapshotUsable(published, marker, manifest) {
		return nil
	}
	// A published tree that is incomplete, marker-less, or fails its manifest
	// validation is residue from a dead publisher or tampered cache. No lease
	// can hold it — a lease holds the shared lock, and we just acquired the
	// exclusive one — so it is safe to discard and rebuild over it.
	_ = removeReadOnlyStoreEntry(published)
	_ = removeReadOnlyStoreEntry(marker)
	_ = removeReadOnlyStoreEntry(manifest)

	entries, err := gitTreeEntries(ctx, worktree, sha)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("review snapshot aborted: %w", ctxErr)
		}
		return err
	}
	// The committed mode travels with the path from git's own ls-tree
	// listing — `git cat-file --batch` header field 0 is the blob OID, not a
	// mode — so the mapping is built here, once, from the authoritative
	// source.
	regularFiles := make([]string, 0, len(entries))
	modes := make(map[string]string, len(entries))
	for _, entry := range entries {
		if !isCommittedRegularFile(entry.mode, entry.objectType) {
			continue
		}
		if clean, ok := safePath(entry.path); ok && clean == entry.path {
			regularFiles = append(regularFiles, entry.path)
			modes[entry.path] = entry.mode
		}
	}
	staging, err := os.MkdirTemp(root, stagingPrefix+sha+"~")
	if err != nil {
		return fmt.Errorf("create review snapshot: %w", err)
	}
	if err := materializeTree(ctx, worktree, sha, staging, regularFiles); err != nil {
		_ = removeReadOnlyStoreEntry(staging)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("review snapshot aborted: %w", ctxErr)
		}
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		_ = removeReadOnlyStoreEntry(staging)
		return fmt.Errorf("review snapshot aborted: %w", ctxErr)
	}
	// Tighten the staged evidence to non-writable files and record exactly
	// what the tree must contain, then publish the readiness marker BEFORE
	// the rename: once the directory appears under its SHA name it must
	// already be identifiable as complete, because that is the moment any
	// other process may lease it.
	if err := writeManifest(staging, manifest, modes); err != nil {
		_ = removeReadOnlyStoreEntry(staging)
		_ = removeReadOnlyStoreEntry(manifest)
		return fmt.Errorf("publish review snapshot: %w", err)
	}
	if err := os.WriteFile(marker, []byte("ready\n"), 0o400); err != nil {
		_ = removeReadOnlyStoreEntry(staging)
		_ = removeReadOnlyStoreEntry(manifest)
		return fmt.Errorf("publish review snapshot: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		_ = removeReadOnlyStoreEntry(staging)
		_ = removeReadOnlyStoreEntry(marker)
		_ = removeReadOnlyStoreEntry(manifest)
		return fmt.Errorf("review snapshot aborted: %w", ctxErr)
	}
	if err := os.Rename(staging, published); err != nil {
		if _, statErr := os.Stat(published); statErr == nil {
			// A published tree is already in place: concede the transition
			// to the winner and leave its readiness marker and manifest
			// alone — removing them would invalidate a tree we did not
			// publish.
			_ = removeReadOnlyStoreEntry(staging)
			return nil
		}
		// A genuine rename failure: remove the staging tree and the
		// readiness artifacts we published for it, so no orphan survives.
		_ = removeReadOnlyStoreEntry(staging)
		_ = removeReadOnlyStoreEntry(marker)
		_ = removeReadOnlyStoreEntry(manifest)
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

// reapSharedStore is the lock-aware reaper for the shared store. It refuses
// to traverse any root that fails checkStoreRoot — Create runs the reaper
// BEFORE its own fail-closed validation, so a hostile final-component
// symlink must not let the reaper delete stale matching residue inside the
// target it points at. A missing root stays harmless best-effort: nothing to
// reap, no failure. A stale entry — a published tree, a staging directory,
// or an orphaned readiness marker or manifest with no tree behind it — is
// removed only after the reaper acquires that SHA's nonblocking exclusive
// lock, so an entry a live lease still holds in any process is skipped no
// matter how old it looks, and an entry a live creator is materializing is
// protected by the very same exclusive hold. Provider state roots are private
// to one review, so stale ones use age-based cleanup without a lease. Names
// parsed back out of the store must be full canonical object ids; anything
// else is left alone. The legacy per-invocation snapshot directories keep
// their older, purely age-based cleanup in reapAbandonedSnapshots.
func reapSharedStore(root string, now time.Time, maxAge time.Duration) int {
	if err := checkStoreRoot(root); err != nil {
		return 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case entry.IsDir() && strings.HasPrefix(name, ProviderStatePrefix):
			pid, hasOwner := ProviderStatePID(name)
			if !isStaleStoreEntry(entry, now, maxAge) && (!hasOwner || processAlive(pid)) {
				continue
			}
			if removeReadOnlyStoreEntry(filepath.Join(root, name)) == nil {
				removed++
			}
		case entry.IsDir() && strings.HasPrefix(name, publishedPrefix) && !strings.HasSuffix(name, readySuffix) && !strings.HasSuffix(name, manifestSuffix):
			sha := strings.TrimPrefix(name, publishedPrefix)
			if sha == "" || !validObjectID(sha) || !isStaleStoreEntry(entry, now, maxAge) {
				continue
			}
			if removeUnleasedStoreEntry(root, sha, now, maxAge, filepath.Join(root, name), markerPath(root, sha), manifestPath(root, sha)) {
				removed++
			}
		case entry.IsDir() && strings.HasPrefix(name, stagingPrefix):
			sha := stagingSHA(name)
			if sha == "" || !validObjectID(sha) || !isStaleStoreEntry(entry, now, maxAge) {
				continue
			}
			if removeUnleasedStoreEntry(root, sha, now, maxAge, filepath.Join(root, name)) {
				removed++
			}
		case !entry.IsDir() && strings.HasPrefix(name, publishedPrefix) && (strings.HasSuffix(name, readySuffix) || strings.HasSuffix(name, manifestSuffix)):
			sha := strings.TrimSuffix(strings.TrimPrefix(name, publishedPrefix), readySuffix)
			sha = strings.TrimSuffix(sha, manifestSuffix)
			if sha == "" || !validObjectID(sha) || !isStaleStoreEntry(entry, now, maxAge) {
				continue
			}
			// An artifact whose tree exists belongs to that tree and is
			// removed together with it by the published-tree branch above.
			if _, err := os.Stat(publishedPath(root, sha)); err == nil {
				continue
			}
			if removeUnleasedStoreEntry(root, sha, now, maxAge, filepath.Join(root, name)) {
				removed++
			}
		}
	}
	return removed
}

// reapSharedStoreCapacity evicts the least recently leased published trees
// until the snapshot data this package owns fits its capacity-relative ceiling.
// It never makes a removal decision from a pre-lock stat alone: each selected
// tree is re-statted after its SHA's exclusive lock is acquired, so a tree
// leased or republished since the scan survives. Staging trees and readiness
// artifacts do not compete here; the age reaper above owns their abandoned-
// residue cleanup.
func reapSharedStoreCapacity(root string) int {
	if err := checkStoreRoot(root); err != nil {
		return 0
	}
	space, err := storeFilesystemSpace(root)
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	type candidate struct {
		sha     string
		modTime time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	sizingFailed := false
	// Only published trees are candidates, and only published trees are
	// measured. The two sets match on purpose: see sharedStoreSnapshotBytes.
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), publishedPrefix) || strings.HasSuffix(entry.Name(), readySuffix) || strings.HasSuffix(entry.Name(), manifestSuffix) {
			continue
		}
		sha := strings.TrimPrefix(entry.Name(), publishedPrefix)
		if !validObjectID(sha) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// A metadata failure must never make the store look smaller than it
			// is. Other candidates can still be evicted under their own locks.
			sizingFailed = true
			continue
		}
		candidates = append(candidates, candidate{sha: sha, modTime: info.ModTime()})
	}
	ownedBytes, err := sharedStoreSnapshotBytes(root)
	if err != nil {
		// Fail conservatively: an unreadable entry may be arbitrarily large, so
		// retain no capacity-based reuse guarantee while sizing is incomplete.
		sizingFailed = true
	}
	if !sizingFailed && ownedBytes <= storeCapacityLimit(space.capacity) {
		return 0
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime.Before(candidates[j].modTime)
	})

	removed := 0
	for _, candidate := range candidates {
		if !sizingFailed && ownedBytes <= storeCapacityLimit(space.capacity) {
			break
		}
		if removeUnleasedStoreEntryAtMtime(root, candidate.sha, candidate.modTime, publishedPath(root, candidate.sha), markerPath(root, candidate.sha), manifestPath(root, candidate.sha)) {
			removed++
		}
		// Refresh after every attempt, including one that found the candidate
		// already gone. The directory scan predates the SHA lock, so a failed
		// removal says nothing reliable about the current store footprint.
		refreshedBytes, err := sharedStoreSnapshotBytes(root)
		if err != nil {
			sizingFailed = true
			continue
		}
		ownedBytes = refreshedBytes
		sizingFailed = false
	}
	return removed
}

// sharedStoreSnapshotBytes reports the logical footprint of published snapshot
// trees and their readiness artifacts. Persistent lock files are deliberately
// excluded: removing them would break the lock namespace, and they contain no
// snapshot evidence.
//
// Provider state is excluded for a stronger reason, recorded here because
// counting it looks like an obvious improvement and was tried: eviction can
// only remove published trees, so measuring anything it cannot remove turns
// this ceiling into a shredder. Once an unevictable floor exceeds the limit,
// the caller's loop drops every reusable tree and is still over it, on every
// Create. A ceiling measures what its lever can move. Provider state is bounded
// at its source instead - collected when its owner dies, or by age.
func sharedStoreSnapshotBytes(root string) (uint64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), publishedPrefix) || strings.HasSuffix(entry.Name(), readySuffix) || strings.HasSuffix(entry.Name(), manifestSuffix) {
			continue
		}
		sha := strings.TrimPrefix(entry.Name(), publishedPrefix)
		if !validObjectID(sha) {
			continue
		}
		size, err := snapshotEntrySize(root, sha)
		if err != nil {
			return 0, err
		}
		total += size
	}
	return total, nil
}

// snapshotEntrySize measures one published tree and its optional readiness
// artifacts. Missing artifacts have no bytes to count; other read failures are
// returned so the caller can evict conservatively rather than under-count.
func snapshotEntrySize(root, sha string) (uint64, error) {
	var total uint64
	for _, path := range []string{publishedPath(root, sha), markerPath(root, sha), manifestPath(root, sha)} {
		size, err := storeEntrySize(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("measure review snapshot entry %q: %w", path, err)
		}
		total += size
	}
	return total, nil
}

// storeEntrySize is replaceable by focused tests that simulate an unreadable
// snapshot entry. Production always measures through measureStoreEntrySize.
var storeEntrySize = measureStoreEntrySize

func measureStoreEntrySize(path string) (uint64, error) {
	var size uint64
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 0 {
			size += uint64(info.Size())
		}
		return nil
	})
	return size, err
}

// removeUnleasedStoreEntryAtMtime is replaceable by focused tests that model a
// concurrent reaper winning the deletion race. Production uses the lock-aware
// implementation below.
var removeUnleasedStoreEntryAtMtime = removeUnleasedStoreEntryAtMtimeLocked

// removeUnleasedStoreEntryAtMtimeLocked removes a published tree only when its
// state remains exactly the state selected for capacity eviction after the SHA's
// exclusive lock is held. A newer mtime means a successful lease or fresh
// publication won the race, so it must remain reusable.
func removeUnleasedStoreEntryAtMtimeLocked(root, sha string, selectedModTime time.Time, targets ...string) bool {
	lock, acquired, err := lockSnapshot(lockPath(root, sha), true)
	if err != nil || !acquired {
		return false
	}
	defer lock.Close()

	published := publishedPath(root, sha)
	info, err := os.Lstat(published)
	if err != nil || !info.IsDir() || !info.ModTime().Equal(selectedModTime) {
		return false
	}
	removed := false
	for _, target := range targets {
		if err := removeReadOnlyStoreEntry(target); err == nil {
			removed = true
		}
	}
	return removed
}

// removeUnleasedStoreEntry removes the given store entries for sha only while
// nothing leases them anywhere: the nonblocking exclusive lock on the SHA's
// lock file succeeds only when no shared lease exists, and while it is held,
// no creator or reaper can be inside the same entries either. Each target is
// re-statted and its staleness re-checked UNDER that lock before deletion —
// the verdict that scheduled this entry was formed from a directory scan
// taken before the lock was held, and a fresh publication may have replaced
// or refreshed the entry since; a fresh publication always survives. Only
// the snapshot, marker, manifest, and staging targets go away — never the
// lock file. Unlinking a lock file while its inode is locked would break the
// mutual exclusion the whole store rests on: a party holding the old file
// open could lock the orphaned inode while a later opener created a fresh
// inode, and the two would no longer exclude each other. Lock files are
// therefore persistent zero-byte files, one per object id ever audited,
// keeping the lock namespace stable for the lifetime of the store.
func removeUnleasedStoreEntry(root, sha string, now time.Time, maxAge time.Duration, targets ...string) bool {
	lock, acquired, err := lockSnapshot(lockPath(root, sha), true)
	if err != nil || !acquired {
		return false
	}
	defer lock.Close()
	removed := false
	for _, target := range targets {
		info, err := os.Lstat(target)
		if err != nil {
			continue // already gone; nothing to remove
		}
		if now.Sub(info.ModTime()) < maxAge {
			continue // refreshed since the scan: a fresh publication survives
		}
		if removeReadOnlyStoreEntry(target) == nil {
			removed = true
		}
	}
	return removed
}
