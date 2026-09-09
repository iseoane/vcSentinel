package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPRReviewKeyIncludesBranchSlugAndHeadSHA(t *testing.T) {
	if got, want := PRReviewKey("feature/persisted review", "abc123"), "feature-persisted-review-abc123"; got != want {
		t.Fatalf("PRReviewKey() = %q, want %q", got, want)
	}
	if PRReviewKey("feature/persisted review", "abc123") == PRReviewKey("feature/persisted review", "def456") {
		t.Fatal("different heads must produce different PR review keys")
	}
}

func TestSavePRReviewReplacesPreviousHeadForTheSameBranch(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	first := &PRReviewEntry{Branch: "feature/review", HeadSHA: "head-a", Body: "first", At: time.Now().UTC()}
	second := &PRReviewEntry{Branch: "feature/review", HeadSHA: "head-b", Body: "second", At: time.Now().UTC()}

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
	if got == nil || got.Body != "second" || got.HeadSHA != "head-b" {
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
