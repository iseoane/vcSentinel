package git

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ReadPathAtRevision: present=false with nil error is an AUTHORITATIVE absence; err != nil propagates.
func ReadPathAtRevision(rev, path string) (string, bool, error) {
	commit, err := ResolverSHA(rev)
	if err != nil {
		return "", false, fmt.Errorf("resolve %q: %w", rev, err)
	}
	listing, err := ejecutarGitSalida("ls-tree", commit, "--", filepath.ToSlash(path))
	if err != nil {
		return "", false, fmt.Errorf("list %q in %q: %w", path, rev, err)
	}
	if strings.TrimSpace(listing) == "" {
		return "", false, nil
	}
	content, err := ContenidoDeArchivoEnCommit(commit, path)
	if err != nil {
		return "", true, err
	}
	return content, true, nil
}

// RangeEvidence returns the full diff and renamed-aware changed paths of a revision range.
func RangeEvidence(from, to string) (string, []string, error) {
	diff, err := ejecutarGitSalida("diff", "--no-color", from+".."+to)
	if err != nil {
		return "", nil, fmt.Errorf("diff %s..%s: %w", from, to, err)
	}
	listing, err := ejecutarGitSalida("diff", "--name-only", "-z", "-M", from+".."+to)
	if err != nil {
		return "", nil, fmt.Errorf("changes %s..%s: %w", from, to, err)
	}
	var paths []string
	for _, p := range strings.Split(strings.TrimSuffix(listing, "\x00"), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return diff, paths, nil
}
