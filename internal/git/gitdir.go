package git

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
)

// GetGitDir returns the absolute path of the active repository's Git
// directory (`git rev-parse --absolute-git-dir`). In linked worktrees it
// returns the worktree's private directory (`.git/worktrees/<name>`).
// Repository-wide vcSentinel persistence uses GetGitCommonDir instead.
// The literal `.git` path is never assumed: on Windows and in linked
// worktrees the directory may be represented by a gitfile.
func GetGitDir() (string, error) {
	out, err := runGitOutput("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// GetGitDirFrom returns the git dir of the given path, not the one of the
// process's working directory. It is the twin GetGitCommonDir was missing,
// which does accept a path and always has.
//
// That asymmetry had real consequences: a command operating on a foreign
// worktree that records its event with GetGitDir() would write it into
// whatever repository it HAPPENED to run in. During `go test` the cwd is
// this repository, so the gate tests ended up appending their failure
// events to the real operational ledger.
//
// Outside a repository it returns an error: the caller must write nowhere
// at all, never into the wrong repository.
func GetGitDirFrom(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--absolute-git-dir")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out.String())), nil
}

// GetGitCommonDir returns the absolute path of the Git common-dir of the
// repository at path (`git rev-parse --git-common-dir`): the same
// directory for every linked worktree of the repository, unlike
// GetGitDir. It is used to install artifacts shared by the whole
// repository (like the hooks), which Git only reads from the common-dir
// and not from each linked worktree's private directory.
func GetGitCommonDir(path string) (string, error) {
	// Isolated the same way as the reachability probe. With an ambient
	// GIT_DIR, this query selected another repository, ITS ledgers were
	// enumerated and then classified against path's refs: live records of
	// one repository deleted for being orphans in another. Sanitizing only
	// one of the two queries is worse than sanitizing neither, because it
	// splits the identity.
	out, err := GitInIsolated(path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	return filepath.Clean(dir), nil
}
