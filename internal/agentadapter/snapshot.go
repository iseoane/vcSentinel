package agentadapter

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func createReviewSnapshot(worktree, sha string, paths []string) (string, []string, func(), error) {
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
	for _, filePath := range rutasRevisionSeguras(paths) {
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
