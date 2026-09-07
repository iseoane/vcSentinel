package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestObtainGitDirInsideRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	gitDir, err := GetGitDir()
	if err != nil {
		t.Fatalf("GetGitDir returned error: %v", err)
	}
	expected := filepath.Join(dir, ".git")
	if !IsSamePath(gitDir, expected) {
		t.Errorf("GetGitDir() = %q, expected %q", gitDir, expected)
	}
}

func TestObtainGitDirOutsideRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	t.Chdir(t.TempDir())

	if _, err := GetGitDir(); err == nil {
		t.Error("expected an error when querying the git dir outside a Git repository")
	}
}

func TestObtainGitDirFromSubdirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	sub := filepath.Join(dir, "internal", "package")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("could not create the subdirectory: %v", err)
	}
	t.Chdir(sub)

	gitDir, err := GetGitDir()
	if err != nil {
		t.Fatalf("GetGitDir returned error: %v", err)
	}
	expected := filepath.Join(dir, ".git")
	if !IsSamePath(gitDir, expected) {
		t.Errorf("GetGitDir() = %q, expected %q", gitDir, expected)
	}
}

// TestObtainGitDirFromPathNotCwd pins the missing contract: the
// git dir is resolved from the given path, not from the process working
// directory.
//
// Without it, a command operating on a foreign worktree writes to the
// repository where it CASUALLY runs. That is what filled the real event log
// with 855 of 1082 entries fabricated by the test suite: the tests pass a
// temporary worktree, but GetGitDir() reads the cwd, which during `go
// test` is the repository itself.
func TestObtainGitDirFromPathNotCwd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}
	foreign := t.TempDir()
	if out, err := exec.Command("git", "-C", foreign, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// The cwd is still this repository: exactly a test's situation.
	resolved, err := GetGitDirFrom(foreign)
	if err != nil {
		t.Fatalf("GetGitDirFrom(%q): %v", foreign, err)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(foreign, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Errorf("GetGitDirFrom = %q, expected %q: resolved from the cwd instead of from the path", got, expected)
	}

	// Outside a repository it must fail, so the caller writes nowhere instead
	// of writing to the wrong repository.
	if _, err := GetGitDirFrom(t.TempDir()); err == nil {
		t.Error("GetGitDirFrom outside a repository should fail")
	}
}
