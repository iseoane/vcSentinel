package review

import (
	"fmt"
	"os/exec"
	"strings"
)

// SnapshotReader reads one path from the immutable commit being audited.
type SnapshotReader func(sha, file string) (string, error)

func leerContenidoSnapshot(sha, archivo string) (string, error) {
	if strings.HasPrefix(sha, "-") {
		return "", fmt.Errorf("invalid audited commit SHA")
	}
	salida, err := exec.Command("git", "show", "--no-textconv", sha+":"+archivo).Output()
	if err != nil {
		return "", fmt.Errorf("read audited content for %q: %w", archivo, err)
	}
	return string(salida), nil
}
