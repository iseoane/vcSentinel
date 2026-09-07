package git

import (
	"path/filepath"
	"runtime"
	"strings"
)

// GetWorktreeRoot returns the normalized absolute path of the active Git
// worktree's root (the directory the process runs from). It returns an
// error if the current directory does not belong to a Git repository (or is
// a bare repository), because there is no working root to report.
func GetWorktreeRoot() (string, error) {
	out, err := runGitOutput("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// IsSamePath compares two paths tolerating separator and case differences.
// On Windows the comparison ignores case; on every other system it is
// case-sensitive.
func IsSamePath(a string, b string) bool {
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(cleanA, cleanB)
	}
	return cleanA == cleanB
}
