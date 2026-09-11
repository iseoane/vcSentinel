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
)

const subdirPRReviews = "pr-reviews"

// PRReviewEntry is the persisted judgement authored by pr review for one
// branch head. pr create consumes this entry and must not derive a second
// branch judgement. Attestation remains raw JSON here to avoid coupling the
// storage layer to the renderer package that already depends on Store.
type PRReviewEntry struct {
	Branch      string          `json:"branch"`
	HeadSHA     string          `json:"head_sha"`
	Title       string          `json:"title"`
	Verdict     string          `json:"verdict"`
	Body        string          `json:"body"`
	Attestation json.RawMessage `json:"attestation"`
	Evidence    []string        `json:"evidence,omitempty"`
	At          time.Time       `json:"at"`
}

// PRReviewKey binds one entry to both a human-readable branch slug and the
// exact head snapshot, preventing a stale branch judgement from being reused.
func PRReviewKey(branch, headSHA string) string {
	identity := sha256.Sum256([]byte(branch))
	return branchSlug(branch) + "-" + hex.EncodeToString(identity[:8]) + "-" + strings.TrimSpace(headSHA)
}

// SavePRReview atomically saves entry and removes every older entry for the
// same exact branch. A branch therefore retains one review entry, not one file
// per historical head.
func (s *Store) SavePRReview(entry *PRReviewEntry) error {
	if entry == nil || strings.TrimSpace(entry.Branch) == "" || !validGitObjectID(entry.HeadSHA) {
		return errors.New("store: pr review entry requires branch and canonical head sha")
	}

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

	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
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
	_ = os.RemoveAll(backup)
	if err := os.Rename(dir, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(staged, dir); err != nil {
		if restoreErr := os.Rename(backup, dir); restoreErr != nil {
			return fmt.Errorf("store: replace pr review entries: %w (restore failed: %v)", err, restoreErr)
		}
		return err
	}
	return os.RemoveAll(backup)
}

// ReadPRReview returns the one current entry for the exact branch name,
// whatever head it records. A caller compares HeadSHA with its current tree:
// returning the older entry is what lets pr create distinguish "never reviewed"
// from "reviewed, then changed".
func (s *Store) ReadPRReview(branch string) (*PRReviewEntry, error) {
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

func validGitObjectID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
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
