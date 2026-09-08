package reviewsnapshot

import (
	"path/filepath"
	"testing"
)

// TestRemoveReadOnlyStoreEntryToleratesMissingPath pins the os.RemoveAll
// contract the permission-aware helper stands in for: removing a path that is
// already gone succeeds. filepath.WalkDir does NOT share that tolerance — it
// reports the lstat failure for a missing root — so the helper has to absorb
// it explicitly. Two cleanup sites evaluate the result rather than discarding
// it (reapAbandonedSnapshots counts a reaped entry, and the stale store reaper
// does the same), so an entry that vanished through a concurrent race must
// still count as removed.
func TestRemoveReadOnlyStoreEntryToleratesMissingPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-published")
	if err := removeReadOnlyStoreEntry(missing); err != nil {
		t.Fatalf("removeReadOnlyStoreEntry(%q) = %v, want nil for an absent entry", missing, err)
	}
}
