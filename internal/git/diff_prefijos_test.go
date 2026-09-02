package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffCommitForcesStablePrefixes covers what the plan tests cannot: they
// feed PlanForProfile a hand-written diff, so an incorrect flag here would
// leave them green while the production planner is starved. The planner
// recognises a path by its "b/" prefix, and diff.noprefix or a custom prefix
// changes that header, losing every added line with no error.
func TestDiffCommitForcesStablePrefixes(t *testing.T) {
	dir := t.TempDir()
	entorno := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	}
	correr := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, entorno
		if salida, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, salida)
		}
	}
	escribir := func(nombre, contenido string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, nombre), []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	correr("init", "-q", "-b", "main")
	escribir("a.txt", "one\n")
	correr("add", "a.txt")
	correr("commit", "-qm", "first")
	escribir("a.txt", "one\ntwo\n")
	correr("add", "a.txt")
	correr("commit", "-qm", "second")

	previo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previo) })

	for _, caso := range []struct{ nombre, clave, valor string }{
		{"no configuration", "", ""},
		{"diff.noprefix", "diff.noprefix", "true"},
		{"diff.dstPrefix", "diff.dstPrefix", "Y/"},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			if caso.clave != "" {
				correr("config", caso.clave, caso.valor)
				defer correr("config", "--unset", caso.clave)
			}
			commit, err := DiffCommit("HEAD")
			if err != nil {
				t.Fatalf("DiffCommit: %v", err)
			}
			if !strings.Contains(commit, "+++ b/a.txt") {
				t.Errorf("DiffCommit under %s produced no \"+++ b/a.txt\" header:\n%s", caso.nombre, commit)
			}
			rango, err := DiffRango("HEAD^", "HEAD")
			if err != nil {
				t.Fatalf("DiffRango: %v", err)
			}
			if !strings.Contains(rango, "+++ b/a.txt") {
				t.Errorf("DiffRango under %s produced no \"+++ b/a.txt\" header:\n%s", caso.nombre, rango)
			}
		})
	}
}
