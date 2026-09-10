package review

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteEvidenceIsDeterministic(t *testing.T) {
	worktree := t.TempDir()
	logs := []EvidenceLog{{Step: "review", Content: "review output\n"}, {Step: "test", Content: "exit 0\n"}}
	first, err := WriteEvidence(worktree, "feature/evidence", logs)
	if err != nil { t.Fatalf("first WriteEvidence() error = %v", err) }
	firstBytes, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(first[0])))
	if err != nil { t.Fatal(err) }
	second, err := WriteEvidence(worktree, "feature/evidence", logs)
	if err != nil { t.Fatalf("second WriteEvidence() error = %v", err) }
	secondBytes, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(second[0])))
	if err != nil { t.Fatal(err) }
	if string(firstBytes) != string(secondBytes) { t.Fatalf("evidence changed between identical runs: %q != %q", firstBytes, secondBytes) }
}
