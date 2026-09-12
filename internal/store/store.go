// Package store implements VAS Sentinel's persistent storage for T2.5
// (F2): units, runs, findings, commits, decisions, and PR-review entries,
// anchored in the repository's git common dir (shared across linked
// worktrees from day one). It coexists with internal/review.Ledger without
// replacing it:
// migrating existing data is T2.6's job; this package starts empty.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const (
	subdirUnits    = "units"
	subdirRuns     = "runs"
	subdirFindings = "findings"
	subdirCommits  = "commits"
)

// Store gives access to storage keyed by id inside <gitCommonDir>/vas-sentinel.
type Store struct {
	dir string
}

// NewStore creates a store anchored to <gitCommonDir>/vas-sentinel. The
// caller is responsible for passing the result of git.GetGitCommonDir
// (not GetGitDir) so that the store is shared across linked worktrees;
// the store itself does not enforce that choice, just as review.NewLedger
// does not either. It does not create the directory: that happens on the
// first write.
func NewStore(gitCommonDir string) *Store {
	return &Store{dir: filepath.Join(gitCommonDir, "vas-sentinel")}
}

// decisionsPath returns the path of the append-only decisions file.
func (s *Store) decisionsPath() string {
	return filepath.Join(s.dir, "decisions.jsonl")
}

// writeJSON persists v as <dir>/<subdir>/<id>.json with atomic temp+rename
// writing: same pattern as review.Ledger.saveRecord (proven in T2.x),
// reused here for the four types keyed by id (units, runs, findings,
// commits). On Windows the existing destination is removed before the
// rename because the system does not allow overwriting with os.Rename.
func (s *Store) writeJSON(subdir, id string, v any) error {
	dir := filepath.Join(s.dir, subdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	destination := filepath.Join(dir, id+".json")
	temp, err := os.CreateTemp(dir, "tmp-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, destination)
}

// readJSON reads <dir>/<subdir>/<id>.json into v. Returns (false, nil) if
// the file does not exist yet; a corrupt file is an explicit error, never a
// silent nil (same criterion as review.Ledger.ReadRecord).
func (s *Store) readJSON(subdir, id string, v any) (bool, error) {
	path := filepath.Join(s.dir, subdir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, err
	}
	return true, nil
}
