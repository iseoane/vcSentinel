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

// RangeRenames returns rename/copy destinations keyed by their source path for
// an immutable revision range. It parses `git diff --name-status -z -M`
// NUL-safely: with -z every record is NUL-terminated and rename/copy records
// carry a score token plus two path tokens, so quoting can never corrupt the mapping.
func RangeRenames(from, to string) (map[string]string, error) {
	listing, err := ejecutarGitSalida("diff", "--name-status", "-z", "-M", from+".."+to)
	if err != nil {
		return nil, fmt.Errorf("name-status %s..%s: %w", from, to, err)
	}
	renames := map[string]string{}
	tokens := strings.Split(strings.TrimSuffix(listing, "\x00"), "\x00")
	for i := 0; i < len(tokens); i++ {
		status := tokens[i]
		switch {
		case status == "":
			continue
		case status[0] == 'R' || status[0] == 'C': // score token + source + destination
			if i+2 >= len(tokens) {
				return nil, fmt.Errorf("name-status %s..%s: truncated record %q", from, to, status)
			}
			if tokens[i+1] != "" && tokens[i+2] != "" {
				renames[tokens[i+1]] = tokens[i+2]
			}
			i += 2
		default:
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("name-status %s..%s: truncated record %q", from, to, status)
			}
			i++
		}
	}
	return renames, nil
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
