package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestArgumentosDiffExplainForcePrefixesAgainstRealGit answers the review
// finding against b7ac0cf and 0269350 with Git itself rather than with a
// fabricated diff. The parser recognises a path by its b/ prefix, and a header
// it does not recognise loses its added lines silently, which starves every
// content-reading detector.
//
// Each case runs the diff twice, once without the forced flags and once with
// them, and asserts both headers. Asserting only the forced one would prove the
// header is right without proving the flags are what makes it right, and would
// leave every comparative claim resting on prose.
//
// What the two runs establish: diff.noprefix and the diff.srcPrefix and
// diff.dstPrefix pair each change the unforced header, so the flags are load
// bearing. diff.mnemonicPrefix does not, because this command diffs one commit
// against another and Git keeps a/ and b/ for a commit-to-commit range whether
// the setting is on or not. That case is a guard for a future caller diffing
// the index or the worktree, where mnemonic prefixes do substitute c/, i/ and
// w/ — ticket 05 reaches those callers — and the test now proves it is a guard
// rather than claiming it.
//
// The forced run uses argumentosDiffExplain, so the test cannot drift from the
// invocation the command actually issues.
func TestArgumentosDiffExplainForcePrefixesAgainstRealGit(t *testing.T) {
	dir := t.TempDir()
	entorno := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	correr := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, entorno
		var errores strings.Builder
		cmd.Stderr = &errores
		salida, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errores.String())
		}
		return string(salida)
	}

	correr("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "a.txt")
	correr("commit", "-qm", "first")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "a.txt")
	correr("commit", "-qm", "second")

	const forzada = "+++ b/a.txt"
	casos := []struct {
		nombre       string
		config       [][2]string
		sinForzar    string // the header this setting produces when the flags are absent
		esGuardaSola bool   // true when the setting cannot change a commit-to-commit header
	}{
		{nombre: "no configuration", sinForzar: forzada, esGuardaSola: true},
		{nombre: "diff.noprefix", config: [][2]string{{"diff.noprefix", "true"}}, sinForzar: "+++ a.txt"},
		{nombre: "diff.mnemonicPrefix", config: [][2]string{{"diff.mnemonicPrefix", "true"}}, sinForzar: forzada, esGuardaSola: true},
		{nombre: "diff.srcPrefix and diff.dstPrefix", config: [][2]string{{"diff.srcPrefix", "X/"}, {"diff.dstPrefix", "Y/"}}, sinForzar: "+++ Y/a.txt"},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			for _, ajuste := range caso.config {
				correr("config", ajuste[0], ajuste[1])
			}
			defer func() {
				for _, ajuste := range caso.config {
					correr("config", "--unset", ajuste[0])
				}
			}()

			if got := cabeceraPostimagen(correr("diff", "--no-color", "--unified=0", "-M", "HEAD^..HEAD")); got != caso.sinForzar {
				t.Errorf("unforced header under %s = %q, want %q", caso.nombre, got, caso.sinForzar)
			}
			if got := cabeceraPostimagen(correr(argumentosDiffExplain("HEAD^..HEAD")...)); got != forzada {
				t.Errorf("forced header under %s = %q, want %q; the flags did not override the setting",
					caso.nombre, got, forzada)
			}
			// Stated rather than left implicit: a case whose unforced header is
			// already correct proves nothing about the flags, and is kept only
			// so a future caller that can break it fails here.
			if caso.esGuardaSola != (caso.sinForzar == forzada) {
				t.Errorf("%s is marked guard-only=%t but its unforced header is %q",
					caso.nombre, caso.esGuardaSola, caso.sinForzar)
			}
		})
	}
}

// cabeceraPostimagen returns the first +++ header of a diff, or the empty string
// when there is none.
func cabeceraPostimagen(diff string) string {
	for _, linea := range strings.Split(diff, "\n") {
		if strings.HasPrefix(linea, "+++ ") {
			return linea
		}
	}
	return ""
}
