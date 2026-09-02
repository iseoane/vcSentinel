package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repositorio is a throwaway Git repository. The git-backed readers cannot be
// exercised against a fake: what is under test is exactly how real Git reports
// absence versus failure.
type repositorio struct {
	t   *testing.T
	dir string
}

// correr runs every Git command through one entry point with a built-from-empty
// environment. Appending to os.Environ() would not isolate anything: it keeps
// GIT_CONFIG_COUNT and the GIT_CONFIG_KEY_*/VALUE_* pairs, which override the
// very neutralisation the other variables state, and it keeps GIT_TRACE, whose
// diagnostics would land in the output this returns.
//
// Only stdout is returned, because callers use the result as an exact revision
// or object ID: folding stderr into it would corrupt the identifier rather than
// fail loudly.
func (r repositorio) correr(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.dir,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	var errores strings.Builder
	cmd.Stderr = &errores
	salida, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errores.String())
	}
	return strings.TrimSpace(string(salida))
}

func (r repositorio) escribir(nombre, contenido string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, nombre), []byte(contenido), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// nuevoRepositorio builds the repository and makes it the working directory for
// the duration of the test, because the readers under test shell out to Git in
// the current directory.
func nuevoRepositorio(t *testing.T) repositorio {
	t.Helper()
	r := repositorio{t: t, dir: t.TempDir()}
	r.correr("init", "-q", "-b", "main")
	r.escribir("a.txt", "one\n")
	r.correr("add", "a.txt")
	r.correr("commit", "-qm", "first")

	previo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(r.dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previo) })
	return r
}

// TestLeerGitattributesDistinguishesAbsenceFromFailure is the regression for
// the finding that blocked fce2da5. Neither `git show <sha>:.gitattributes` nor
// `git cat-file -e` separates the two cases: both exit non-zero for an absent
// path and for a broken repository, so either one turns a real read failure
// into "no attributes" and loses evidence without incrementing the failure
// count.
func TestLeerGitattributesDistinguishesAbsenceFromFailure(t *testing.T) {
	t.Run("absent is not a failure", func(t *testing.T) {
		r := nuevoRepositorio(t)
		contenido, err := leerGitattributes(r.correr("rev-parse", "HEAD"))
		if err != nil {
			t.Fatalf("absent .gitattributes must not be an error, got %v", err)
		}
		if contenido != "" {
			t.Errorf("absent .gitattributes = %q, want empty", contenido)
		}
	})

	t.Run("present is read", func(t *testing.T) {
		r := nuevoRepositorio(t)
		r.escribir(".gitattributes", "*.pb.go linguist-generated\n")
		r.correr("add", ".gitattributes")
		r.correr("commit", "-qm", "attributes")

		contenido, err := leerGitattributes(r.correr("rev-parse", "HEAD"))
		if err != nil {
			t.Fatalf("present .gitattributes returned %v", err)
		}
		if !strings.Contains(contenido, "linguist-generated") {
			t.Errorf("present .gitattributes = %q, want the recorded attribute", contenido)
		}
	})

	t.Run("an unresolvable revision is a failure", func(t *testing.T) {
		nuevoRepositorio(t)
		if _, err := leerGitattributes("0000000000000000000000000000000000000000"); err == nil {
			t.Error("an unreadable revision returned no error; a read failure must not read as absence")
		}
	})

	// The tree entry stays valid and only its blob is destroyed, so the
	// existence probe still succeeds and the read behind it fails. Without this
	// case a regression in the second error path would pass: the all-zero
	// revision never reaches the `git show` that follows a successful ls-tree.
	t.Run("a present but unreadable blob is a failure", func(t *testing.T) {
		r := nuevoRepositorio(t)
		r.escribir(".gitattributes", "*.pb.go linguist-generated\n")
		r.correr("add", ".gitattributes")
		r.correr("commit", "-qm", "attributes")

		sha := r.correr("rev-parse", "HEAD")
		blob := r.correr("rev-parse", "HEAD:.gitattributes")

		// The fixture is freshly created and never repacked, so the blob is
		// loose by construction. Skipping on a removal failure would let this
		// test report success without ever exercising the path it exists for,
		// which is the same silent-emptiness failure the harness itself guards
		// against. It fails instead.
		suelto := filepath.Join(r.dir, ".git", "objects", blob[:2], blob[2:])
		if _, err := os.Stat(suelto); err != nil {
			t.Fatalf("the blob is not loose in the fixture, so the corruption cannot be staged: %v", err)
		}
		if err := os.Remove(suelto); err != nil {
			t.Fatalf("removing the loose blob: %v", err)
		}

		if _, err := leerGitattributes(sha); err == nil {
			t.Error("an unreadable blob behind a valid tree entry returned no error")
		}
	})
}

// TestCommitsSinMergeWalksTheGivenTip is the regression the previous review
// asked for over the ref-snapshot race: main resolves the tip to a SHA first
// and walks from that SHA, so a ref moving between the two commands cannot
// label the report with a tip it never measured. The walk must therefore honour
// an explicit SHA rather than re-reading a symbolic name.
func TestCommitsSinMergeWalksTheGivenTip(t *testing.T) {
	r := nuevoRepositorio(t)
	primero := r.correr("rev-parse", "HEAD")
	r.correr("commit", "-qm", "second", "--allow-empty")

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
