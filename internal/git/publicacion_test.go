// T9.5 publication boundary: a commit is published when it is an ancestor
// of origin/main. The predicate answers merge-base and nothing else; any
// unanswerable query (unknown object, missing remote ref) is an error,
// never a negative, so callers fail closed instead of collecting on a
// guess (FU-15).
package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicadoEnRemotoSeparatesPublishedFromUnpublished(t *testing.T) {
	dir := prepararRepositorioPrueba(t, map[string]string{"root.txt": "root\n"})
	ejecutarGit(t, dir, "branch", "-M", "main")
	base := ejecutarGit(t, dir, "rev-parse", "HEAD")
	ejecutarGit(t, dir, "update-ref", "refs/remotes/origin/main", base)

	ejecutarGit(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "add", "feature.txt")
	ejecutarGit(t, dir, "commit", "-q", "-m", "feature")
	feature := ejecutarGit(t, dir, "rev-parse", "HEAD")

	if ok, err := PublicadoEnRemoto(dir, base); err != nil || !ok {
		t.Fatalf("PublicadoEnRemoto(base) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := PublicadoEnRemoto(dir, "origin/main"); err != nil || !ok {
		t.Fatalf("PublicadoEnRemoto(origin/main) = %v, %v; want true, nil (boundary is reflexive)", ok, err)
	}
	if ok, err := PublicadoEnRemoto(dir, feature); err != nil || ok {
		t.Fatalf("PublicadoEnRemoto(feature) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := PublicadoEnRemoto(dir, "0000000000000000000000000000000000000000"); err == nil || ok {
		t.Fatalf("PublicadoEnRemoto(unknown) = %v, %v; want false with an error, never a bare negative", ok, err)
	}
}

func TestPublicadoEnRemotoFailsClosedWithoutRemoteRef(t *testing.T) {
	dir := prepararRepositorioPrueba(t, map[string]string{"root.txt": "root\n"})
	base := ejecutarGit(t, dir, "rev-parse", "HEAD")
	if ok, err := PublicadoEnRemoto(dir, base); err == nil || ok {
		t.Fatalf("PublicadoEnRemoto without origin/main = %v, %v; want false with an error", ok, err)
	}
}
