package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWriteEvidenceIsDeterministic(t *testing.T) {
	worktree := t.TempDir()
	logs := []EvidenceLog{{Step: "review", Content: "review output\n"}, {Step: "test", Content: "exit 0\n"}}
	first, err := WriteEvidence(worktree, "feature/evidence", logs)
	if err != nil {
		t.Fatalf("first WriteEvidence() error = %v", err)
	}
	firstBytes, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(first[0])))
	if err != nil {
		t.Fatal(err)
	}
	second, err := WriteEvidence(worktree, "feature/evidence", logs)
	if err != nil {
		t.Fatalf("second WriteEvidence() error = %v", err)
	}
	secondBytes, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(second[0])))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatalf("evidence changed between identical runs: %q != %q", firstBytes, secondBytes)
	}
}

// Two branch names that collapse onto the same readable slug must not share a
// directory: the second review would silently overwrite the first branch's
// logs. The identity is the exact branch name, not its slug.
func TestWriteEvidenceSeparatesBranchesThatShareASlug(t *testing.T) {
	worktree := t.TempDir()
	logs := []EvidenceLog{{Step: "review", Content: "one\n"}}
	first, err := WriteEvidence(worktree, "feature/a", logs)
	if err != nil {
		t.Fatalf("WriteEvidence(feature/a) error = %v", err)
	}
	second, err := WriteEvidence(worktree, "feature-a", []EvidenceLog{{Step: "review", Content: "two\n"}})
	if err != nil {
		t.Fatalf("WriteEvidence(feature-a) error = %v", err)
	}
	if first[0] == second[0] {
		t.Fatalf("both branches wrote to %s", first[0])
	}
	content, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(first[0])))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "one\n" {
		t.Fatalf("feature/a evidence was overwritten: %q", content)
	}
}

// A repository can ship a symlink where the evidence goes. Following it would
// let the reviewed tree decide which file the reviewing process truncates.
func TestWriteEvidenceRefusesToFollowASymlink(t *testing.T) {
	worktree := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("untouched\n"), 0644); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(worktree, ".vas_sentinel", "evidence", evidenceDirName("feature/x"))
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "review.log")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := WriteEvidence(worktree, "feature/x", []EvidenceLog{{Step: "review", Content: "hijacked\n"}}); err == nil {
		t.Fatal("WriteEvidence() followed a symlink instead of refusing")
	}
	content, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "untouched\n" {
		t.Fatalf("the file outside the worktree was written: %q", content)
	}
}

func TestWriteEvidenceRefusesAnInternalSymlinkedAncestor(t *testing.T) {
	worktree := t.TempDir()
	target := filepath.Join(worktree, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(target, "review.log")
	if err := os.WriteFile(logPath, []byte("untouched\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(worktree, ".vas_sentinel", "evidence", evidenceDirName("feature/x"))
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "..", "target"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := WriteEvidence(worktree, "feature/x", []EvidenceLog{{Step: "review", Content: "hijacked\n"}}); err == nil {
		t.Fatal("WriteEvidence() followed an internal symlinked ancestor")
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "untouched\n" {
		t.Fatalf("the symlink target was written: %q", content)
	}
}

func TestEvidenceAtHEADSeparatesAbsenceFromFailure(t *testing.T) {
	notARepository := t.TempDir()
	if _, _, err := EvidenceAtHEAD(notARepository, "any.log"); err == nil {
		t.Fatal("EvidenceAtHEAD() reported a non-repository as a clean absence")
	}

	worktree := initEvidenceRepository(t)
	present, reason, err := EvidenceAtHEAD(worktree, "missing.log")
	if err != nil {
		t.Fatalf("EvidenceAtHEAD(missing) error = %v", err)
	}
	if present || reason != "not in HEAD" {
		t.Fatalf("EvidenceAtHEAD(missing) = %v, %q; want false, %q", present, reason, "not in HEAD")
	}
}

func TestEvidenceAtHEADDistinguishesMatchingFromModified(t *testing.T) {
	worktree := initEvidenceRepository(t)
	tracked := filepath.Join(worktree, "tracked.log")
	if err := os.WriteFile(tracked, []byte("committed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runEvidenceGit(t, worktree, "add", "tracked.log")
	runEvidenceGit(t, worktree, "commit", "-m", "add evidence")

	present, reason, err := EvidenceAtHEAD(worktree, "tracked.log")
	if err != nil {
		t.Fatalf("EvidenceAtHEAD(tracked) error = %v", err)
	}
	if !present || reason != "" {
		t.Fatalf("EvidenceAtHEAD(tracked) = %v, %q; want true, \"\"", present, reason)
	}

	if err := os.WriteFile(tracked, []byte("edited\n"), 0644); err != nil {
		t.Fatal(err)
	}
	present, reason, err = EvidenceAtHEAD(worktree, "tracked.log")
	if err != nil {
		t.Fatalf("EvidenceAtHEAD(edited) error = %v", err)
	}
	if present || reason != "differs from HEAD" {
		t.Fatalf("EvidenceAtHEAD(edited) = %v, %q; want false, %q", present, reason, "differs from HEAD")
	}
}

func initEvidenceRepository(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	runEvidenceGit(t, worktree, "init")
	runEvidenceGit(t, worktree, "config", "user.email", "test@example.com")
	runEvidenceGit(t, worktree, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(worktree, "seed"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runEvidenceGit(t, worktree, "add", "seed")
	runEvidenceGit(t, worktree, "commit", "-m", "seed")
	return worktree
}

func runEvidenceGit(t *testing.T, worktree string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

// The endpoint check alone was not enough: MkdirAll follows a symlinked
// ancestor, and afterwards the endpoint is a real directory at the attacker's
// target. The confinement has to hold at every component of the path.
func TestWriteEvidenceRefusesASymlinkedAncestor(t *testing.T) {
	worktree := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(worktree, ".vas_sentinel")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := WriteEvidence(worktree, "feature/x", []EvidenceLog{{Step: "review", Content: "hijacked\n"}}); err == nil {
		t.Fatal("WriteEvidence() wrote through a symlinked ancestor instead of refusing")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the directory outside the worktree received %d entries", len(entries))
	}
}
