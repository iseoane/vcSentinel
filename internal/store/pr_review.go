package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

const subdirPRReviews = "pr-reviews"

// UnavailableDimensionCause records why one net-audit dimension was
// unavailable. It is observational only and never changes the verdict,
// following the AuditResult.ContextSkipReason precedent: the class is
// "admission" when the reason carries the admission prefix, "infrastructure"
// otherwise, and "unrecorded" when no cause was recorded at all.
type UnavailableDimensionCause struct {
	Dimension    string `json:"dimension"`
	Class        string `json:"class"`
	Reason       string `json:"reason,omitempty"`
	InvocationID string `json:"invocation_id,omitempty"`
}

// PRReviewEntry is the persisted judgement authored by pr review for one
// branch head. Piece 5 will make pr create consume this entry rather than derive
// a second branch judgement. Attestation remains raw JSON here to avoid coupling
// the storage layer to the renderer package that already depends on Store.
type PRReviewEntry struct {
	Branch      string          `json:"branch"`
	HeadSHA     string          `json:"head_sha"`
	Title       string          `json:"title"`
	Verdict     string          `json:"verdict"`
	Body        string          `json:"body"`
	Attestation json.RawMessage `json:"attestation"`
	Evidence    []string        `json:"evidence,omitempty"`
	// UnavailableCauses carries the per-dimension net-audit causes for the
	// console and machine surfaces only. It never feeds the published body:
	// internal diagnostics such as denied-permission reasons must not end up
	// in the PR description. omitempty keeps existing persisted entries
	// decoding unchanged.
	UnavailableCauses []UnavailableDimensionCause `json:"unavailable_causes,omitempty"`
	At                time.Time                   `json:"at"`
}

// PRReviewKey binds one entry to both a human-readable branch slug and the
// exact head snapshot, preventing a stale branch judgement from being reused.
func PRReviewKey(branch, headSHA string) string {
	identity := sha256.Sum256([]byte(branch))
	return branchSlug(branch) + "-" + hex.EncodeToString(identity[:8]) + "-" + strings.TrimSpace(headSHA)
}

// prReviewLockWait bounds how long a writer waits for another process that is
// replacing the complete PR-review generation. The lock file is intentionally
// retained after release: deleting it while another process still has the file
// open would let a new process create a different inode and bypass the lock.
var prReviewLockWait = 15 * time.Second

// prReviewSaveAfterReadHook is a test seam for coordinating a cross-process
// replacement race. Production code never sets it.
var prReviewSaveAfterReadHook func()

// withPRReviewLock serializes every PR-review read and write for this store
// root. executionLock uses an OS advisory lock, so the kernel releases it when
// a writer exits unexpectedly; a stale lock file therefore needs no guessing
// or unsafe timeout-based stealing.
func (s *Store) withPRReviewLock(action func() error) (err error) {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	lock, err := acquireExecutionLock(filepath.Join(s.dir, ".pr-reviews.lock"), prReviewLockWait)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lock.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return action()
}

// recoverPRReviewGeneration repairs the only two states a writer can leave
// behind while holding the lock: an old generation in the backup with no live
// directory, or a new live generation with an old backup left by a crash during
// cleanup. It never removes a backup until a live generation is present.
func (s *Store) recoverPRReviewGeneration() error {
	dir := filepath.Join(s.dir, subdirPRReviews)
	backup := filepath.Join(s.dir, ".pr-reviews-backup")

	dirInfo, dirErr := os.Stat(dir)
	if dirErr != nil && !errors.Is(dirErr, os.ErrNotExist) {
		return dirErr
	}
	backupInfo, backupErr := os.Stat(backup)
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return backupErr
	}

	if errors.Is(dirErr, os.ErrNotExist) {
		if errors.Is(backupErr, os.ErrNotExist) {
			return nil
		}
		if !backupInfo.IsDir() {
			return fmt.Errorf("store: PR-review backup is not a directory")
		}
		if err := os.Rename(backup, dir); err != nil {
			return fmt.Errorf("store: recover PR-review generation: %w", err)
		}
		return nil
	}
	if !dirInfo.IsDir() {
		return fmt.Errorf("store: PR-review entries path is not a directory")
	}
	if errors.Is(backupErr, os.ErrNotExist) {
		return nil
	}
	if !backupInfo.IsDir() {
		return fmt.Errorf("store: PR-review backup is not a directory")
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("store: remove recovered PR-review backup: %w", err)
	}
	return nil
}

// SavePRReview atomically saves entry and removes every older entry for the
// same exact branch. A branch therefore retains one review entry, not one file
// per historical head. The lock covers recovery, the complete old-state read,
// staging, replacement, and backup cleanup so concurrent processes cannot
// publish generations based on the same old state.
func (s *Store) SavePRReview(entry *PRReviewEntry) error {
	if entry == nil || strings.TrimSpace(entry.Branch) == "" || !IsValidGitObjectID(entry.HeadSHA) {
		return errors.New("store: pr review entry requires branch and canonical head sha")
	}

	return s.withPRReviewLock(func() error {
		if err := s.recoverPRReviewGeneration(); err != nil {
			return err
		}
		return s.savePRReviewLocked(entry)
	})
}

func (s *Store) savePRReviewLocked(entry *PRReviewEntry) error {
	dir := filepath.Join(s.dir, subdirPRReviews)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return err
	}

	// Read and validate the complete old state before mutating anything. The
	// replacement is then assembled in a sibling directory and swapped in as
	// one filesystem operation, so cleanup cannot expose a mixed generation.
	preserved := make(map[string][]byte)
	for _, existing := range entries {
		if existing.IsDir() || filepath.Ext(existing.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, existing.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var stored PRReviewEntry
		if err := json.Unmarshal(data, &stored); err != nil {
			return fmt.Errorf("store: read pr review entry %q: %w", existing.Name(), err)
		}
		if stored.Branch != entry.Branch {
			preserved[existing.Name()] = data
		}
	}
	if prReviewSaveAfterReadHook != nil {
		prReviewSaveAfterReadHook()
	}

	parent := filepath.Dir(dir)
	staged, err := os.MkdirTemp(parent, "pr-reviews-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	for name, data := range preserved {
		if err := os.WriteFile(filepath.Join(staged, name), data, 0644); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	key := PRReviewKey(entry.Branch, entry.HeadSHA)
	if err := os.WriteFile(filepath.Join(staged, key+".json"), data, 0644); err != nil {
		return err
	}

	backup := filepath.Join(parent, ".pr-reviews-backup")
	movedOld := false
	if err := os.Rename(dir, backup); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		movedOld = true
	}
	if err := os.Rename(staged, dir); err != nil {
		if movedOld {
			if restoreErr := os.Rename(backup, dir); restoreErr != nil {
				return fmt.Errorf("store: replace pr review entries: %w (restore failed: %v)", err, restoreErr)
			}
		}
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("store: remove PR-review backup: %w", err)
	}
	return nil
}

// ReadPRReview returns the one current entry for the exact branch name,
// whatever head it records. A caller compares HeadSHA with its current tree:
// returning the older entry is what lets pr create distinguish "never reviewed"
// from "reviewed, then changed". Reads share the writer lock so callers never
// observe the brief path gap between the two directory renames.
func (s *Store) ReadPRReview(branch string) (*PRReviewEntry, error) {
	if _, err := os.Stat(s.dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var found *PRReviewEntry
	err := s.withPRReviewLock(func() error {
		if err := s.recoverPRReviewGeneration(); err != nil {
			return err
		}
		var err error
		found, err = s.readPRReviewLocked(branch)
		return err
	})
	return found, err
}

func (s *Store) readPRReviewLocked(branch string) (*PRReviewEntry, error) {
	dir := filepath.Join(s.dir, subdirPRReviews)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found *PRReviewEntry
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var stored PRReviewEntry
		if err := json.Unmarshal(data, &stored); err != nil {
			return nil, fmt.Errorf("store: read pr review entry %q: %w", entry.Name(), err)
		}
		if stored.Branch != branch {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("store: multiple pr review entries for branch %q", branch)
		}
		found = &stored
	}
	return found, nil
}

// IsValidGitObjectID keeps the store's public validator compatible while the
// canonical object-ID rule lives with the Git identity helpers.
func IsValidGitObjectID(value string) bool {
	return git.IsValidGitObjectID(value)
}

func branchSlug(branch string) string {
	var b strings.Builder
	previousDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(branch)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			previousDash = false
			continue
		}
		if !previousDash && b.Len() > 0 {
			b.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
