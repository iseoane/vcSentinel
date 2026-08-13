package review

import (
	"fmt"
	"os/exec"
	"strings"
)

// SnapshotReader reads one path from the immutable commit being audited.
type SnapshotReader func(sha, file string) (string, error)

func leerContenidoSnapshot(sha, archivo string) (string, error) {
	return leerContenidoSnapshotEn("", sha, archivo)
}

// NewSnapshotReader returns a reader bound to one repository's immutable Git objects.
func NewSnapshotReader(repo string) SnapshotReader {
	return func(sha, archivo string) (string, error) {
		return leerContenidoSnapshotEn(repo, sha, archivo)
	}
}

func leerContenidoSnapshotEn(repo, sha, archivo string) (string, error) {
	if strings.HasPrefix(sha, "-") {
		return "", fmt.Errorf("invalid audited commit SHA")
	}
	args := []string{"show", "--no-textconv", sha + ":" + archivo}
	if repo != "" {
		args = append([]string{"-C", repo}, args...)
	}
	salida, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("read audited content for %q: %w", archivo, err)
	}
	return string(salida), nil
}
