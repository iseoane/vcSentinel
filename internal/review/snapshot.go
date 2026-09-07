package review

import (
	"fmt"
	"os/exec"
	"strings"
)

// SnapshotReader reads one path from the immutable commit being audited.
type SnapshotReader func(sha, file string) (string, error)

func readSnapshotContent(sha, file string) (string, error) {
	return readSnapshotContentFrom("", sha, file)
}

// NewSnapshotReader returns a reader bound to one repository's immutable Git objects.
func NewSnapshotReader(repo string) SnapshotReader {
	return func(sha, file string) (string, error) {
		return readSnapshotContentFrom(repo, sha, file)
	}
}

func readSnapshotContentFrom(repo, sha, file string) (string, error) {
	if strings.HasPrefix(sha, "-") {
		return "", fmt.Errorf("invalid audited commit SHA")
	}
	args := []string{"show", "--no-textconv", sha + ":" + file}
	if repo != "" {
		args = append([]string{"-C", repo}, args...)
	}
	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("read audited content for %q: %w", file, err)
	}
	return string(output), nil
}
