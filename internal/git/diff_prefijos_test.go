package git

import (
	"os"
	"strings"
	"testing"
)

// TestDiffCommitForcesStablePrefixes covers what the review-plan tests cannot:
// they feed the plan derivation a hand-written diff, so an incorrect flag here
// would leave them green while the production planner is starved. The planner
// recognises a path by its "b/" prefix, and diff.noprefix or a custom prefix
// changes that header, losing every added line with no error.
//
// It uses prepararRepoTemp, the package fixture, which changes the process
// working directory because the helpers under test operate on it. That is the
// documented convention here and the reason this package must not use
// t.Parallel(); no test in it does.
func TestDiffCommitForcesStablePrefixes(t *testing.T) {
	repo := prepararRepoTemp(t)
	_ = repo

	escribirYCommitear := func(contenido, mensaje string) {
		t.Helper()
		if err := os.WriteFile("a.txt", []byte(contenido), 0o644); err != nil {
			t.Fatal(err)
		}
		if salida, err := ejecutarGitSalida("add", "a.txt"); err != nil {
			t.Fatalf("add: %v (%s)", err, salida)
		}
		if salida, err := ejecutarGitSalida("commit", "-qm", mensaje); err != nil {
			t.Fatalf("commit: %v (%s)", err, salida)
		}
	}
	escribirYCommitear("one\n", "first")
	escribirYCommitear("one\ntwo\n", "second")

	for _, caso := range []struct{ nombre, clave, valor string }{
		{"no configuration", "", ""},
		{"diff.noprefix", "diff.noprefix", "true"},
		{"diff.dstPrefix", "diff.dstPrefix", "Y/"},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			if caso.clave != "" {
				if salida, err := ejecutarGitSalida("config", caso.clave, caso.valor); err != nil {
					t.Fatalf("config: %v (%s)", err, salida)
				}
				defer ejecutarGitSalida("config", "--unset", caso.clave)
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
