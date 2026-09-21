package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prepareTempRepo creates a temporary git repo with the local identity
// configured and changes into its working directory (the git helpers operate
// on the cwd). Returns the repo path so tests can use it with filepath.Join.
// CAUTION: t.Chdir mutates the process cwd; tests in this package must not
// use t.Parallel(), or the cwd would leak between tests. Prefer os.Chdir with
// a defer-restore if parallelism is ever needed.
func prepareTempRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)

	for _, cmd := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vcsentinel"},
		{"config", "user.name", "vcSentinel Test"},
		// The user's global core.hooksPath installs the volume hook that
		// would reject the test commits: it is disabled for this temp repo
		// only.
		{"config", "core.hooksPath", ""},
	} {
		if out, err := runGitOutput(cmd...); err != nil {
			t.Fatalf("setup %v failed: %v (%s)", cmd, err, out)
		}
	}
	return repo
}

// commitInRepo creates a commit with a new file in the current repo and
// returns its full SHA.
func commitInRepo(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("could not write %s: %v", name, err)
	}
	runGitCommand(t, "add", name)
	runGitCommand(t, "commit", "-m", "feat("+name+"): test content")
	sha, err := runGitOutput("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("could not read HEAD: %v", err)
	}
	return strings.TrimSpace(sha)
}

func runGitCommand(t *testing.T, args ...string) {
	t.Helper()
	if _, err := runGitOutput(args...); err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
}

// TestMergeBase checks the common ancestor between main and a branch created
// from an intermediate commit.
func TestMergeBase(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "a.txt", "base\n")
	intermediate := commitInRepo(t, "b.txt", "second\n")
	runGitCommand(t, "checkout", "-b", "feature")
	commitInRepo(t, "c.txt", "branch\n")

	base, err := MergeBase("main", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase failed: %v", err)
	}
	if base != intermediate {
		t.Errorf("MergeBase = %s, expected %s (the commit the branch was created from)", base, intermediate)
	}
}

// TestNumstatRange sums added + deleted lines of the range, ignoring
// binaries.
func TestNumstatRange(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "a.txt", strings.Repeat("x\n", 10))
	runGitCommand(t, "checkout", "-b", "feature")

	// Replaces the 10 "x" lines with 15 "y" lines in a.txt (10 deleted + 15
	// really added) and creates b.txt with 3 lines.
	if err := os.WriteFile("a.txt", []byte(strings.Repeat("y\n", 15)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.txt", []byte("1\n2\n3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, "add", "-A")
	runGitCommand(t, "commit", "-m", "feat: branch volume")

	vol, err := RangeNumstat("main", "HEAD")
	if err != nil {
		t.Fatalf("NumstatRange failed: %v", err)
	}
	// 15 added in a.txt + 10 deleted in a.txt + 3 added in b.txt.
	if vol != 28 {
		t.Errorf("NumstatRange = %d, expected 28 (15+3 added, 10 deleted)", vol)
	}
}

// TestNumstatRangeEmpty: without changes the volume is zero.
func TestNumstatRangeEmpty(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "a.txt", "x\n")

	vol, err := RangeNumstat("HEAD", "HEAD")
	if err != nil {
		t.Fatalf("NumstatRange failed: %v", err)
	}
	if vol != 0 {
		t.Errorf("NumstatRange = %d, expected 0", vol)
	}
}

// TestMergeBaseWithoutBase: two branches without a common ancestor are an
// explicit error, because there is no base over which to measure the range.
func TestMergeBaseWithoutBase(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "a.txt", "main\n")
	// Orphan branch: a root commit disconnected from main.
	runGitCommand(t, "checkout", "--orphan", "other")
	commitInRepo(t, "b.txt", "other\n")
	runGitCommand(t, "checkout", "main")

	if _, err := MergeBase("main", "other"); err == nil {
		t.Error("MergeBase accepted two branches without a common ancestor")
	}
}

// TestNumstatRangeInvalidRevision: a range with a nonexistent revision fails.
func TestNumstatRangeInvalidRevision(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "a.txt", "x\n")

	if _, err := RangeNumstat("nonexistent", "HEAD"); err == nil {
		t.Error("NumstatRange accepted a nonexistent revision")
	}
}

// TestNumstatRangeBinary: a binary file arrives as "-" in the numstat and
// must neither sum lines nor break the count of the rest.
func TestNumstatRangeBinary(t *testing.T) {
	prepareTempRepo(t)
	commitInRepo(t, "a.txt", "x\n")
	runGitCommand(t, "checkout", "-b", "feature")

	if err := os.WriteFile("bin.dat", []byte{0x00, 0x01, 0x02, 0x00, 0xFF}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("a.txt", []byte("x\ny\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, "add", "-A")
	runGitCommand(t, "commit", "-m", "feat: with binary")

	vol, err := RangeNumstat("main", "HEAD")
	if err != nil {
		t.Fatalf("NumstatRange failed: %v", err)
	}
	// Only counts the added line in a.txt; the binary is ignored.
	if vol != 1 {
		t.Errorf("NumstatRange = %d, expected 1 (the binary adds no lines)", vol)
	}
}
