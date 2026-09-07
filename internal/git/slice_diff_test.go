package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPendingDiffForPathsIncludesChangesWithoutMutatingIndex(t *testing.T) {
	dir := prepareTestRepo(t, map[string]string{
		"tracked.go": "package sample\n",
	})
	t.Chdir(dir)

	stagedContent := "package sample\n\nfunc Staged() {}\n"
	if err := os.WriteFile("tracked.go", []byte(stagedContent), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "--", "tracked.go")
	if err := os.WriteFile("tracked.go", []byte(stagedContent+"\nfunc Unstaged() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	newFiles := map[string]string{
		"file with spaces.go": "package new\n\nfunc WithSpaces() {}\n",
		"-option.go":          "package new\n\nfunc LiteralOption() {}\n",
	}
	for path, content := range newFiles {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	indexBefore := runGitInDir(t, dir, "write-tree")
	diff, err := pendingDiffForPaths([]string{"tracked.go", "file with spaces.go", "-option.go"})
	if err != nil {
		t.Fatalf("pendingDiffForPaths returned error: %v", err)
	}

	for _, fragment := range []string{"func Staged() {}", "func Unstaged() {}", "func WithSpaces() {}", "func LiteralOption() {}"} {
		if !strings.Contains(diff, fragment) {
			t.Errorf("the diff does not contain %q:\n%s", fragment, diff)
		}
	}
	if count := strings.Count(diff, "diff --git a/tracked.go b/tracked.go"); count != 1 {
		t.Errorf("the tracked diff appears %d times, expected 1:\n%s", count, diff)
	}
	if indexAfter := runGitInDir(t, dir, "write-tree"); indexAfter != indexBefore {
		t.Errorf("the index changed: before %s, after %s", indexBefore, indexAfter)
	}
}

func TestPendingDiffForPathsRejectsBinaryUntracked(t *testing.T) {
	dir := prepareTestRepo(t, map[string]string{"tracked.go": "package sample\n"})
	t.Chdir(dir)
	if err := os.WriteFile("binary.dat", []byte{'V', 'A', 'S', 0, 'X'}, 0644); err != nil {
		t.Fatal(err)
	}

	diff, err := pendingDiffForPaths([]string{"binary.dat"})
	if err == nil || diff != "" || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("result = (%q, %v), expected binary rejection without content", diff, err)
	}
}

func TestPendingDiffForPathsRejectsTooLargeUntracked(t *testing.T) {
	dir := prepareTestRepo(t, map[string]string{"tracked.go": "package sample\n"})
	t.Chdir(dir)
	if err := os.WriteFile("large.txt", []byte(strings.Repeat("x", microDiffByteLimit+1)), 0644); err != nil {
		t.Fatal(err)
	}

	diff, err := pendingDiffForPaths([]string{"large.txt"})
	if err == nil || diff != "" || !strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("result = (%q, %v), expected size rejection without content", diff, err)
	}
}
