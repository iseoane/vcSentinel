package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repositorioTemporal builds a throwaway repository and returns its path. The
// git-backed readers cannot be exercised against a fake: what is under test is
// exactly how real Git reports absence versus failure.
func repositorioTemporal(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	correr := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if salida, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, salida)
		}
	}
	correr("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "a.txt")
	correr("commit", "-qm", "first")
	return dir
}

func enDirectorio(t *testing.T, dir string) {
	t.Helper()
	previo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previo) })
}

func revisionUnica(t *testing.T, rev string) string {
	t.Helper()
	salida, err := git("rev-parse", rev)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(salida)
}

// TestLeerGitattributesDistinguishesAbsenceFromFailure is the regression for
// the finding that blocked fce2da5. Neither `git show <sha>:.gitattributes` nor
// `git cat-file -e` separates the two cases: both exit non-zero for an absent
// path and for a broken repository, so either one turns a real read failure
// into "no attributes" and loses evidence without incrementing the failure
// count.
func TestLeerGitattributesDistinguishesAbsenceFromFailure(t *testing.T) {
	dir := repositorioTemporal(t)
	enDirectorio(t, dir)

	sinAtributos := revisionUnica(t, "HEAD")
	contenido, err := leerGitattributes(sinAtributos)
	if err != nil {
		t.Fatalf("absent .gitattributes must not be an error, got %v", err)
	}
	if contenido != "" {
		t.Errorf("absent .gitattributes = %q, want empty", contenido)
	}

	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.pb.go linguist-generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".gitattributes")
	cmd.Dir = dir
	if salida, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, salida)
	}
	cmd = exec.Command("git", "commit", "-qm", "attributes")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if salida, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, salida)
	}

	conAtributos := revisionUnica(t, "HEAD")
	contenido, err = leerGitattributes(conAtributos)
	if err != nil {
		t.Fatalf("present .gitattributes returned %v", err)
	}
	if !strings.Contains(contenido, "linguist-generated") {
		t.Errorf("present .gitattributes = %q, want the recorded attribute", contenido)
	}

	// A revision that cannot be resolved is a failure, never an absence.
	if _, err := leerGitattributes("0000000000000000000000000000000000000000"); err == nil {
		t.Error("an unreadable revision returned no error; a read failure must not read as absence")
	}
}

// TestCommitsSinMergeWalksTheGivenTip is the regression for the ref-snapshot
// race: main resolves the tip to a SHA first and walks from that SHA, so a ref
// moving between the two commands cannot label the report with a tip it never
// measured. The walk must therefore honour an explicit SHA rather than
// re-reading a symbolic name.
func TestCommitsSinMergeWalksTheGivenTip(t *testing.T) {
	dir := repositorioTemporal(t)
	enDirectorio(t, dir)

	primero := revisionUnica(t, "HEAD")

	cmd := exec.Command("git", "commit", "-qm", "second", "--allow-empty")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if salida, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, salida)
	}

	// Walking the pinned older SHA must not see the commit made after it, even
	// though HEAD has already moved on.
	shas, merges, err := commitsSinMerge(primero, 10)
	if err != nil {
		t.Fatalf("commitsSinMerge: %v", err)
	}
	if len(shas) != 1 || shas[0] != primero {
		t.Errorf("walking the pinned tip = %v, want exactly [%s]", shas, primero)
	}
	if merges != 0 {
		t.Errorf("merges in a linear history = %d, want 0", merges)
	}

	if _, _, err := commitsSinMerge("no-such-ref", 10); err == nil {
		t.Error("an unresolvable tip returned no error")
	}
}
