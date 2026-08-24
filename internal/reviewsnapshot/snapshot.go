// Package reviewsnapshot owns the read-only review snapshot discipline shared
// by every adapter family: audited paths are materialized from COMMITTED
// content (git ls-tree / git show) into an isolated temporary directory, only
// committed regular files survive filtering, and cleanup stays caller-owned.
//
// It lives outside internal/agentadapter on purpose: both the CLI adapters
// (agentadapter) and the ACP/acpx adapter (acpadapter) must run the exact
// same discipline, and neither package can own it without forcing a dependency
// direction between the two adapter families. This package depends on
// nothing but the standard library, so both families import it freely.
//
// Any semantic change belongs here so every adapter kind stays byte-identical;
// wrappers elsewhere must remain pure delegation.
package reviewsnapshot

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// SafePaths filters and normalizes reviewer-audited paths: absolute paths,
// drive letters, traversal escapes, option-looking strings, and paths
// carrying control or glob metacharacters are dropped; the survivors are
// slash-normalized and cleaned. Shared by every adapter family so the review
// snapshot accepts exactly the same path vocabulary everywhere.
func SafePaths(paths []string) []string {
	safe := make([]string, 0, len(paths))
	for _, p := range paths {
		normalized := strings.ReplaceAll(p, "\\", "/")
		clean := path.Clean(normalized)
		drive := len(clean) >= 2 && clean[1] == ':'
		if p == "" || path.IsAbs(clean) || drive || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\x00\r\n*?[]{}!") {
			continue
		}
		safe = append(safe, clean)
	}
	return safe
}

// Create materializes the read-only review snapshot for sha restricted to
// paths. It returns the snapshot directory, the subset of paths that survived
// the committed-regular-file filter, a caller-owned cleanup func, and an
// error. An empty worktree resolves to the current working directory, which
// matches production usage (review runs from the worktree root).
func Create(worktree, sha string, paths []string) (string, []string, func(), error) {
	if sha == "" {
		return "", nil, nil, fmt.Errorf("semantic review requires an audited commit SHA")
	}
	if worktree == "" {
		var err error
		worktree, err = os.Getwd()
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolve review worktree: %w", err)
		}
	}
	snapshot, err := os.MkdirTemp("", "vas-sentinel-review-")
	if err != nil {
		return "", nil, nil, fmt.Errorf("create review snapshot: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(snapshot) }
	allowed := make([]string, 0, len(paths))
	for _, filePath := range SafePaths(paths) {
		mode, objectType, exists, err := gitTreeEntry(worktree, sha, filePath)
		if err != nil {
			cleanup()
			return "", nil, nil, err
		}
		if !exists || objectType != "blob" || !strings.HasPrefix(mode, "100") {
			continue
		}
		content, err := gitObjectContent(worktree, sha, filePath)
		if err != nil {
			cleanup()
			return "", nil, nil, err
		}
		target := filepath.Join(snapshot, filepath.FromSlash(filePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			cleanup()
			return "", nil, nil, fmt.Errorf("create review snapshot directory: %w", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			cleanup()
			return "", nil, nil, fmt.Errorf("write review snapshot file: %w", err)
		}
		allowed = append(allowed, filePath)
	}
	return snapshot, allowed, cleanup, nil
}

func gitTreeEntry(worktree, sha, filePath string) (mode, objectType string, exists bool, err error) {
	cmd := exec.Command("git", "-C", worktree, "ls-tree", "-z", sha, "--", filePath)
	output, err := cmd.Output()
	if err != nil {
		return "", "", false, fmt.Errorf("read audited tree entry %q: %w", filePath, err)
	}
	if len(output) == 0 {
		return "", "", false, nil
	}
	header, _, ok := bytes.Cut(output, []byte{'\t'})
	if !ok {
		return "", "", false, fmt.Errorf("invalid audited tree entry for %q", filePath)
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 {
		return "", "", false, fmt.Errorf("invalid audited tree entry for %q", filePath)
	}
	return fields[0], fields[1], true, nil
}

func gitObjectContent(worktree, sha, filePath string) ([]byte, error) {
	cmd := exec.Command("git", "-C", worktree, "show", "--no-textconv", sha+":"+filePath)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read audited content for %q: %w", filePath, err)
	}
	return output, nil
}
