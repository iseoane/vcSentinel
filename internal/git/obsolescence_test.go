package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

func TestStillValidTrueWithoutChanges(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	candidate, err := Freeze()
	if err != nil {
		t.Fatalf("Freeze returned error: %v", err)
	}

	stillValid, err := StillValid(candidate)
	if err != nil {
		t.Fatalf("StillValid returned error: %v", err)
	}
	if !stillValid {
		t.Errorf("StillValid = false with no change at all; expected true")
	}
}

func TestStillValidFalseAfterWorktreeModification(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	candidate, err := Freeze()
	if err != nil {
		t.Fatalf("Freeze returned error: %v", err)
	}

	// Uncommitted modification: the worktree differs from the one frozen,
	// even though HEAD did not change.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar x = 1\n"), 0644); err != nil {
		t.Fatalf("could not modify a.go: %v", err)
	}

	stillValid, err := StillValid(candidate)
	if err != nil {
		t.Fatalf("StillValid returned error: %v", err)
	}
	if stillValid {
		t.Errorf("StillValid = true after modifying the worktree; expected false")
	}
}

func TestStillValidFalseAfterNewCommit(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	candidate, err := Freeze()
	if err != nil {
		t.Fatalf("Freeze returned error: %v", err)
	}

	// The same content is committed: the resulting tree matches the one
	// already frozen, but HEAD changed to a different commit. The candidate
	// remains stale because it referenced the previous HEAD.
	runGitInDir(t, dir, "commit", "--allow-empty", "-q", "-m", "empty commit")

	stillValid, err := StillValid(candidate)
	if err != nil {
		t.Fatalf("StillValid returned error: %v", err)
	}
	if stillValid {
		t.Errorf("StillValid = true after a new commit; expected false even when the tree matches")
	}
}

func TestRequireCleanWorktreeInplaceWithCleanWorktree(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if err := RequireCleanWorktreeForInplace(config.ModeInplace); err != nil {
		t.Errorf("RequireCleanWorktreeForInplace returned error with a clean worktree: %v", err)
	}
}

func TestRequireCleanWorktreeInplaceWithDirtyWorktree(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar x = 1\n"), 0644); err != nil {
		t.Fatalf("could not modify a.go: %v", err)
	}

	if err := RequireCleanWorktreeForInplace(config.ModeInplace); err == nil {
		t.Errorf("RequireCleanWorktreeForInplace returned no error with a dirty worktree; expected an explicit error")
	}
}

func TestRequireCleanWorktreeInplaceIgnoresOtherModes(t *testing.T) {
	requireRealGit(t)

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar x = 1\n"), 0644); err != nil {
		t.Fatalf("could not modify a.go: %v", err)
	}

	if err := RequireCleanWorktreeForInplace("worktree"); err != nil {
		t.Errorf("RequireCleanWorktreeForInplace should not apply outside inplace mode, returned: %v", err)
	}
}
