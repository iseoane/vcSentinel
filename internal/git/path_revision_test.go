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

	dir := prepararRepositorioPrueba(t, map[string]string{
		"base.txt":    "base\n",
		"x.go":        "package x\n",
		"old name.go": "package old\n",
	})
	t.Chdir(dir)
	base := ejecutarGit(t, dir, "rev-parse", "HEAD")

	ejecutarGit(t, dir, "mv", "x.go", "y.go")
	ejecutarGit(t, dir, "mv", "old name.go", "new name v2.go") // internal spaces only: portable, proves no whitespace splitting
	if err := os.WriteFile(filepath.Join(dir, "nuevo.go"), []byte("package nuevo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "add", "-A")
	ejecutarGit(t, dir, "commit", "-q", "-m", "feat: renames and addition")

	head := ejecutarGit(t, dir, "rev-parse", "HEAD")
	renames, err := RangeRenames(base, head)
	if err != nil {
		t.Fatalf("RangeRenames: %v", err)
	}
	if renames["x.go"] != "y.go" || renames["old name.go"] != "new name v2.go" || renames["nuevo.go"] != "" {
		t.Errorf("renames = %v, want x.go->y.go, old name.go->new name v2.go, and no mapping for nuevo.go", renames)
	}

	ejecutarGit(t, dir, "rm", "-q", "y.go")
	ejecutarGit(t, dir, "commit", "-q", "-m", "fix: delete y")
	if deleted, err := RangeRenames(head, ejecutarGit(t, dir, "rev-parse", "HEAD")); err != nil || len(deleted) != 0 {
		t.Errorf("deletion-range renames = %v/%v, want empty without error", deleted, err)
	}
}
