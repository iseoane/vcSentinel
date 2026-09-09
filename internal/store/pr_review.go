package store

import (
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
	return branchSlug(branch) + "-" + strings.TrimSpace(headSHA)
}

// SavePRReview atomically saves entry and removes every older entry for the
// same branch slug. A branch therefore retains one review entry, not one file
// per historical head.
func (s *Store) SavePRReview(entry *PRReviewEntry) error {
	if entry == nil || strings.TrimSpace(entry.Branch) == "" || strings.TrimSpace(entry.HeadSHA) == "" {
		return errors.New("store: pr review entry requires branch and head sha")
	}
	key := PRReviewKey(entry.Branch, entry.HeadSHA)
	if err := s.writeJSON(subdirPRReviews, key, entry); err != nil {
		return err
	}
	return s.removeOlderPRReviews(branchSlug(entry.Branch), key)
}

// ReadPRReview returns the one current entry for branch's slug, whatever head
// it records. A caller compares HeadSHA with its current tree: returning the
// older entry is what lets pr create distinguish "never reviewed" from
// "reviewed, then changed".
func (s *Store) ReadPRReview(branch string) (*PRReviewEntry, error) {
	dir := filepath.Join(s.dir, subdirPRReviews)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	slug := branchSlug(branch)
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
		if branchSlug(stored.Branch) != slug {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("store: multiple pr review entries for branch slug %q", slug)
		}
		found = &stored
	}
	return found, nil
}

func (s *Store) removeOlderPRReviews(slug, keepKey string) error {
	dir := filepath.Join(s.dir, subdirPRReviews)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		key := strings.TrimSuffix(entry.Name(), ".json")
		if key == keepKey {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		var stored PRReviewEntry
		if err := json.Unmarshal(data, &stored); err != nil {
			return fmt.Errorf("store: read pr review entry %q: %w", entry.Name(), err)
		}
		if branchSlug(stored.Branch) != slug {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
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
