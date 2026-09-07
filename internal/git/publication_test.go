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

func TestPublishedToRemoteSeparatesPublishedFromUnpublished(t *testing.T) {
	dir := prepareTestRepo(t, map[string]string{"root.txt": "root\n"})
	runGitInDir(t, dir, "branch", "-M", "main")
	base := runGitInDir(t, dir, "rev-parse", "HEAD")
	runGitInDir(t, dir, "update-ref", "refs/remotes/origin/main", base)

	runGitInDir(t, dir, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "feature.txt")
	runGitInDir(t, dir, "commit", "-q", "-m", "feature")
	feature := runGitInDir(t, dir, "rev-parse", "HEAD")

	if ok, err := PublishedToRemote(dir, base); err != nil || !ok {
		t.Fatalf("PublishedToRemote(base) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := PublishedToRemote(dir, "origin/main"); err != nil || !ok {
		t.Fatalf("PublishedToRemote(origin/main) = %v, %v; want true, nil (boundary is reflexive)", ok, err)
	}
	if ok, err := PublishedToRemote(dir, feature); err != nil || ok {
		t.Fatalf("PublishedToRemote(feature) = %v, %v; want false, nil", ok, err)
	}
	if ok, err := PublishedToRemote(dir, "0000000000000000000000000000000000000000"); err == nil || ok {
		t.Fatalf("PublishedToRemote(unknown) = %v, %v; want false with an error, never a bare negative", ok, err)
	}
}

func TestPublishedToRemoteFailsClosedWithoutRemoteRef(t *testing.T) {
	dir := prepareTestRepo(t, map[string]string{"root.txt": "root\n"})
	base := runGitInDir(t, dir, "rev-parse", "HEAD")
	if ok, err := PublishedToRemote(dir, base); err == nil || ok {
		t.Fatalf("PublishedToRemote without origin/main = %v, %v; want false with an error", ok, err)
	}
}
