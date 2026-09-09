package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPRReviewKeyPreservesExactBranchIdentity(t *testing.T) {
	head := strings.Repeat("a", 40)
	keys := map[string]bool{}
	for _, branch := range []string{"feature/review", "Feature/review", "feature-review"} {
		key := PRReviewKey(branch, head)
		if !strings.HasPrefix(key, "feature-review-") {
			t.Fatalf("PRReviewKey(%q) = %q, want readable slug prefix", branch, key)
		}
		if keys[key] { t.Fatalf("PRReviewKey collision for %q", branch) }
		keys[key] = true
	}
}

func TestSavePRReviewReplacesPreviousHeadForTheSameBranch(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	first := &PRReviewEntry{Branch: "feature/review", HeadSHA: strings.Repeat("a", 40), Body: "first", At: time.Now().UTC()}
	second := &PRReviewEntry{Branch: "feature/review", HeadSHA: strings.Repeat("b", 40), Body: "second", At: time.Now().UTC()}

	if err := store.SavePRReview(first); err != nil {
		t.Fatalf("SavePRReview(first) error = %v", err)
	}
	if err := store.SavePRReview(second); err != nil {
		t.Fatalf("SavePRReview(second) error = %v", err)
	}

	got, err := store.ReadPRReview("feature/review")
	if err != nil {
		t.Fatalf("ReadPRReview() error = %v", err)
	}
	if got == nil || got.Body != "second" || got.HeadSHA != strings.Repeat("b", 40) {
		t.Fatalf("ReadPRReview() = %#v, want the latest entry for the branch", got)
	}
	matches, err := filepath.Glob(filepath.Join(root, "vas-sentinel", "pr-reviews", "*.json"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("persisted entries = %v, want exactly one", matches)
	}
}

func TestSavePRReviewRejectsIncompleteIdentity(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, entry := range []*PRReviewEntry{{}, {Branch: "feature/review"}, {HeadSHA: "head"}} {
		if err := store.SavePRReview(entry); err == nil {
			t.Fatalf("SavePRReview(%#v) succeeded, want identity error", entry)
		}
	}
}
