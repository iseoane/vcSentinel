package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRangeRenamesMapsSourceToDestination proves NUL-safe token parsing over an
// immutable range: space-bearing rename pairs survive raw -z names, plain
// additions are not mapped, and true deletions yield an empty mapping.
func TestRangeRenamesMapsSourceToDestination(t *testing.T) {
	if testing.Short() {
		t.Skip("skips real Git repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"base.txt":    "base\n",
		"x.go":        "package x\n",
		"old name.go": "package old\n",
	})
	t.Chdir(dir)
	base := runGitInDir(t, dir, "rev-parse", "HEAD")

	runGitInDir(t, dir, "mv", "x.go", "y.go")
	runGitInDir(t, dir, "mv", "old name.go", "new name v2.go") // internal spaces only: portable, proves no whitespace splitting
	if err := os.WriteFile(filepath.Join(dir, "added.go"), []byte("package added\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "-A")
	runGitInDir(t, dir, "commit", "-q", "-m", "feat: renames and addition")

	head := runGitInDir(t, dir, "rev-parse", "HEAD")
	renames, err := RangeRenames(base, head)
	if err != nil {
		t.Fatalf("RangeRenames: %v", err)
	}
	if renames["x.go"] != "y.go" || renames["old name.go"] != "new name v2.go" || renames["added.go"] != "" {
		t.Errorf("renames = %v, want x.go->y.go, old name.go->new name v2.go, and no mapping for added.go", renames)
	}

	runGitInDir(t, dir, "rm", "-q", "y.go")
	runGitInDir(t, dir, "commit", "-q", "-m", "fix: delete y")
	if deleted, err := RangeRenames(head, runGitInDir(t, dir, "rev-parse", "HEAD")); err != nil || len(deleted) != 0 {
		t.Errorf("deletion-range renames = %v/%v, want empty without error", deleted, err)
	}
}
