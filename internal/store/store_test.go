package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// TestStoreAtomicWrite reproduces TestLedgerEscrituraAtomica (T2.x) over
// the new layout: several consecutive writes to the same id never leave
// the destination file half-written.
func TestStoreAtomicWrite(t *testing.T) {
	s := NewStore(t.TempDir())
	for i := 0; i < 20; i++ {
		idx := &CommitIndex{SHA: "sha1", Message: "m", Fingerprints: []string{"fp"}}
		if err := s.SaveCommitIndex(idx); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	idx, err := s.ReadCommitIndex("sha1")
	if err != nil {
		t.Fatalf("after 20 writes the file was corrupt: %v", err)
	}
	if idx == nil || idx.Message != "m" {
		t.Errorf("index = %+v, does not match the last write", idx)
	}
}

func execGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestStoreAnchoredInGitCommonDirSharedAcrossWorktrees confirms that the
// store, anchored with GetGitCommonDir, resolves to the same directory
// for the main checkout and for a linked worktree — unlike GetGitDir,
// which is private per worktree. A write from the store of the main
// checkout must be visible from the store of the worktree.
func TestStoreAnchoredInGitCommonDirSharedAcrossWorktrees(t *testing.T) {
	mainDir := t.TempDir()
	execGit(t, mainDir, "init", "-q")
	execGit(t, mainDir, "config", "user.email", "test@vas.sentinel")
	execGit(t, mainDir, "config", "user.name", "vas-sentinel-test")
	if err := os.WriteFile(filepath.Join(mainDir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	execGit(t, mainDir, "add", "a.txt")
	execGit(t, mainDir, "commit", "-q", "-m", "initial")

	worktree := filepath.Join(t.TempDir(), "worktree")
	execGit(t, mainDir, "worktree", "add", "-q", worktree, "-b", "test-branch")

	mainCommon, err := git.GetGitCommonDir(mainDir)
	if err != nil {
		t.Fatalf("GetGitCommonDir(mainDir): %v", err)
	}
	worktreeCommon, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatalf("GetGitCommonDir(worktree): %v", err)
	}
	if mainCommon != worktreeCommon {
		t.Fatalf("common-dir differs between main checkout and worktree: %s != %s", mainCommon, worktreeCommon)
	}
	// The worktree's private git-dir (--absolute-git-dir) is
	// .git/worktrees/<name>, different from the common-dir: it confirms
	// that anchoring in GetGitCommonDir (and not in GetGitDir) is
	// what makes sharing the store across linked worktrees possible.
	cmd := exec.Command("git", "-C", worktree, "rev-parse", "--absolute-git-dir")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --absolute-git-dir: %v", err)
	}
	worktreePrivate := filepath.Clean(strings.TrimSpace(string(output)))
	if worktreePrivate == worktreeCommon {
		t.Fatalf("the worktree's private git-dir should not match the common-dir")
	}

	sMain := NewStore(mainCommon)
	if err := sMain.SaveCommitIndex(&CommitIndex{SHA: "sha1", Fingerprints: []string{"fp1"}}); err != nil {
		t.Fatalf("SaveCommitIndex from main: %v", err)
	}

	sWorktree := NewStore(worktreeCommon)
	idx, err := sWorktree.ReadCommitIndex("sha1")
	if err != nil {
		t.Fatalf("ReadCommitIndex from worktree: %v", err)
	}
	if idx == nil || len(idx.Fingerprints) != 1 || idx.Fingerprints[0] != "fp1" {
		t.Errorf("the worktree does not see what the main checkout wrote: %+v", idx)
	}
}
