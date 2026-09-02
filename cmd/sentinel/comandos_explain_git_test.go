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
// Two of the three settings genuinely bite, and only those two prove anything
// here. Without the forced flags, diff.noprefix yields "+++ a.txt" and
// diff.srcPrefix with diff.dstPrefix yields "+++ Y/a.txt"; with them the header
// stays "+++ b/a.txt".
//
// diff.mnemonicPrefix is different and its case is deliberately vacuous: this
// command diffs one commit against another, and for a commit-to-commit range
// Git keeps a/ and b/ whether the setting is on or not, so that subtest passes
// with or without the flags. It stays as a cheap guard in case a future caller
// diffs the index or the worktree, where mnemonic prefixes do substitute c/, i/
// and w/ — ticket 05 reaches those callers. It is not evidence that the flags
// override it, and must not be read as such.
//
// The assertion uses argumentosDiffExplain, so the test cannot drift from the
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

	casos := []struct {
		nombre string
		config [][2]string
	}{
		{"no configuration", nil},
		{"diff.noprefix", [][2]string{{"diff.noprefix", "true"}}},
		// Vacuous for a commit-to-commit range; see the doc comment above.
		{"diff.mnemonicPrefix (guard only)", [][2]string{{"diff.mnemonicPrefix", "true"}}},
		{"diff.srcPrefix and diff.dstPrefix", [][2]string{{"diff.srcPrefix", "X/"}, {"diff.dstPrefix", "Y/"}}},
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

			salida := correr(argumentosDiffExplain("HEAD^..HEAD")...)
			var cabecera string
			for _, linea := range strings.Split(salida, "\n") {
				if strings.HasPrefix(linea, "+++ ") {
					cabecera = linea
					break
				}
			}
			if cabecera != "+++ b/a.txt" {
				t.Errorf("post-image header = %q, want %q; the forced prefixes did not override %s",
					cabecera, "+++ b/a.txt", caso.nombre)
			}
		})
	}
}
