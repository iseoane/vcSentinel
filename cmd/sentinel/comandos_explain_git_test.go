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
// fabricated diff. The parser recognises a path by its b/ prefix, and three
// separate configuration settings change that header: diff.noprefix removes it,
// diff.mnemonicPrefix can substitute c/, i/ or w/, and diff.srcPrefix with
// diff.dstPrefix replace it outright. A header the parser does not recognise
// loses its added lines silently, which starves every content-reading detector.
//
// The assertion is that the explicit flags win over each of those settings. It
// uses argumentosDiffExplain, so the test cannot drift from the invocation the
// command actually issues.
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
		{"diff.mnemonicPrefix", [][2]string{{"diff.mnemonicPrefix", "true"}}},
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
