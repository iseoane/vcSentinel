package agentadapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateReviewSnapshotUsesCommittedContentAndRegularFiles(t *testing.T) {
	repo := t.TempDir()
	gitSnapshot(t, repo, "init")
	gitSnapshot(t, repo, "config", "user.email", "test@example.com")
	gitSnapshot(t, repo, "config", "user.name", "Test")
	writeSnapshotFile(t, repo, "safe.go", "committed\n")
	writeSnapshotFile(t, repo, "outside.txt", "host secret\n")
	if err := os.Symlink(filepath.Join(repo, "outside.txt"), filepath.Join(repo, "link.go")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	gitSnapshot(t, repo, "add", "safe.go", "link.go")
	gitSnapshot(t, repo, "commit", "-m", "snapshot")
	sha := strings.TrimSpace(gitSnapshot(t, repo, "rev-parse", "HEAD"))
	writeSnapshotFile(t, repo, "safe.go", "mutated worktree\n")

	snapshot, paths, cleanup, err := createReviewSnapshot(context.Background(), repo, sha, []string{"safe.go", "link.go", "missing.go"})
	if err != nil {
		t.Fatalf("createReviewSnapshot() error = %v", err)
	}
	defer cleanup()
	if strings.Join(paths, ",") != "safe.go" {
		t.Fatalf("paths = %v, expected only committed regular final-state path", paths)
	}
	content, err := os.ReadFile(filepath.Join(snapshot, "safe.go"))
	if err != nil || string(content) != "committed\n" {
		t.Fatalf("snapshot content = %q, error = %v", content, err)
	}
	info, err := os.Lstat(filepath.Join(snapshot, "safe.go"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("snapshot file mode = %v, error = %v", info.Mode(), err)
	}
}

func gitSnapshot(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func writeSnapshotFile(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
