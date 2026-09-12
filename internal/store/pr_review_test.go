package store

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
		if keys[key] {
			t.Fatalf("PRReviewKey collision for %q", branch)
		}
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

func TestSavePRReviewDoesNotMixStateWhenExistingEntryCannotBeCleaned(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	old := &PRReviewEntry{Branch: "feature/review", HeadSHA: strings.Repeat("a", 40), Body: "old"}
	if err := store.SavePRReview(old); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "vas-sentinel", "pr-reviews", PRReviewKey(old.Branch, old.HeadSHA)+".json")
	if err := os.WriteFile(path, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	newEntry := &PRReviewEntry{Branch: old.Branch, HeadSHA: strings.Repeat("b", 40), Body: "new"}
	if err := store.SavePRReview(newEntry); err == nil {
		t.Fatal("SavePRReview succeeded with an unreadable old entry")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if json.Unmarshal(data, &decoded) == nil {
		t.Fatal("failed save unexpectedly replaced the old state")
	}
	if _, err := store.ReadPRReview(old.Branch); err == nil {
		t.Fatal("ReadPRReview unexpectedly accepted the preserved corrupt state")
	}
}

func TestSavePRReviewKeepsDistinctBranchesReadable(t *testing.T) {
	store := NewStore(t.TempDir())
	for _, branch := range []string{"feature/review", "feature-review"} {
		if err := store.SavePRReview(&PRReviewEntry{Branch: branch, HeadSHA: strings.Repeat("c", 40), Body: branch}); err != nil {
			t.Fatal(err)
		}
	}
	for _, branch := range []string{"feature/review", "feature-review"} {
		got, err := store.ReadPRReview(branch)
		if err != nil || got == nil || got.Body != branch {
			t.Fatalf("ReadPRReview(%q) = %#v, %v", branch, got, err)
		}
	}
}

func TestSavePRReviewRecoversBackupBeforeReadingOldState(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	old := &PRReviewEntry{Branch: "feature/old", HeadSHA: strings.Repeat("a", 40), Body: "old"}
	if err := store.SavePRReview(old); err != nil {
		t.Fatal(err)
	}

	storeRoot := filepath.Join(root, "vas-sentinel")
	dir := filepath.Join(storeRoot, subdirPRReviews)
	backup := filepath.Join(storeRoot, ".pr-reviews-backup")
	if err := os.Rename(dir, backup); err != nil {
		t.Fatal(err)
	}
	newEntry := &PRReviewEntry{Branch: "feature/new", HeadSHA: strings.Repeat("b", 40), Body: "new"}
	if err := store.SavePRReview(newEntry); err != nil {
		t.Fatalf("SavePRReview() after interrupted replacement = %v", err)
	}
	assertPRReviewBody(t, store, old.Branch, old.Body)
	assertPRReviewBody(t, store, newEntry.Branch, newEntry.Body)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup path error = %v, want removed", err)
	}
}

func TestReadPRReviewRecoversCompletedReplacementBackup(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	old := &PRReviewEntry{Branch: "feature/old", HeadSHA: strings.Repeat("a", 40), Body: "old"}
	if err := store.SavePRReview(old); err != nil {
		t.Fatal(err)
	}

	storeRoot := filepath.Join(root, "vas-sentinel")
	dir := filepath.Join(storeRoot, subdirPRReviews)
	backup := filepath.Join(storeRoot, ".pr-reviews-backup")
	if err := os.Rename(dir, backup); err != nil {
		t.Fatal(err)
	}
	live := &PRReviewEntry{Branch: "feature/live", HeadSHA: strings.Repeat("b", 40), Body: "live"}
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(live, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PRReviewKey(live.Branch, live.HeadSHA)+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	assertPRReviewBody(t, store, live.Branch, live.Body)
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup path error = %v, want removed", err)
	}
}

func TestSavePRReviewConcurrentProcessesKeepBothBranches(t *testing.T) {
	if os.Getenv("STORE_PR_REVIEW_CHILD") == "1" {
		runPRReviewChild()
	}

	root := t.TempDir()
	store := NewStore(root)
	entries := []struct {
		branch string
		head   string
	}{
		{branch: "feature/a", head: strings.Repeat("a", 40)},
		{branch: "feature/b", head: strings.Repeat("b", 40)},
	}
	for _, entry := range entries {
		if err := store.SavePRReview(&PRReviewEntry{Branch: entry.branch, HeadSHA: entry.head, Body: "old"}); err != nil {
			t.Fatal(err)
		}
	}

	coordination := filepath.Join(root, "coordination")
	if err := os.Mkdir(coordination, 0755); err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, len(entries))
	ready := make([]string, len(entries))
	release := make([]string, len(entries))
	started := make([]string, len(entries))
	begin := make([]string, len(entries))
	for i := range entries {
		ready[i] = filepath.Join(coordination, fmt.Sprintf("ready-%d", i))
		release[i] = filepath.Join(coordination, fmt.Sprintf("release-%d", i))
		started[i] = filepath.Join(coordination, fmt.Sprintf("started-%d", i))
		begin[i] = filepath.Join(coordination, fmt.Sprintf("begin-%d", i))
	}
	startChild := func(i int) {
		t.Helper()
		entry := entries[i]
		command := exec.Command(os.Args[0], "-test.run", "^TestSavePRReviewConcurrentProcessesKeepBothBranches$")
		command.Env = append(os.Environ(),
			"STORE_PR_REVIEW_CHILD=1",
			"STORE_PR_REVIEW_ROOT="+root,
			"STORE_PR_REVIEW_BRANCH="+entry.branch,
			"STORE_PR_REVIEW_HEAD="+entry.head,
			"STORE_PR_REVIEW_STARTED="+started[i],
			"STORE_PR_REVIEW_READY="+ready[i],
			"STORE_PR_REVIEW_RELEASE="+release[i],
			"STORE_PR_REVIEW_BEGIN="+begin[i],
		)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[i] = command
	}
	t.Cleanup(func() {
		for _, command := range commands {
			if command != nil && command.ProcessState == nil {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
		}
	})

	// Hold the first child after its complete old-state read. A second child
	// must not reach the same point until the first replacement is complete.
	startChild(0)
	waitForFile(t, started[0])
	if err := os.WriteFile(begin[0], []byte("begin"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, ready[0])

	startChild(1)
	waitForFile(t, started[1])
	if err := os.WriteFile(begin[1], []byte("begin"), 0600); err != nil {
		t.Fatal(err)
	}
	assertPRReviewFileAbsent(t, ready[1], 2*time.Second)
	if err := os.WriteFile(release[0], []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, ready[1])
	if err := os.WriteFile(release[1], []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("child %d failed: %v", i, err)
		}
	}

	for _, entry := range entries {
		assertPRReviewBody(t, store, entry.branch, "new")
	}
	storeRoot := filepath.Join(root, "vas-sentinel")
	if matches, err := filepath.Glob(filepath.Join(storeRoot, "pr-reviews-*")); err != nil {
		t.Fatal(err)
	} else if len(matches) != 0 {
		t.Fatalf("staging residue = %v, want none", matches)
	}
	if _, err := os.Stat(filepath.Join(storeRoot, ".pr-reviews-backup")); !os.IsNotExist(err) {
		t.Fatalf("backup path error = %v, want removed", err)
	}
}

func runPRReviewChild() {
	if err := os.WriteFile(os.Getenv("STORE_PR_REVIEW_STARTED"), []byte("started"), 0600); err != nil {
		os.Exit(21)
	}
	for {
		if _, err := os.Stat(os.Getenv("STORE_PR_REVIEW_BEGIN")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	prReviewSaveAfterReadHook = func() {
		if err := os.WriteFile(os.Getenv("STORE_PR_REVIEW_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(22)
		}
		for {
			if _, err := os.Stat(os.Getenv("STORE_PR_REVIEW_RELEASE")); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	entry := &PRReviewEntry{
		Branch:  os.Getenv("STORE_PR_REVIEW_BRANCH"),
		HeadSHA: os.Getenv("STORE_PR_REVIEW_HEAD"),
		Body:    "new",
	}
	if err := NewStore(os.Getenv("STORE_PR_REVIEW_ROOT")).SavePRReview(entry); err != nil {
		fmt.Fprintf(os.Stderr, "SavePRReview() error = %v\n", err)
		os.Exit(23)
	}
	os.Exit(0)
}

func assertPRReviewBody(t *testing.T, store *Store, branch, want string) {
	t.Helper()
	got, err := store.ReadPRReview(branch)
	if err != nil {
		t.Fatalf("ReadPRReview(%q) error = %v", branch, err)
	}
	if got == nil || got.Body != want {
		t.Fatalf("ReadPRReview(%q) = %#v, want body %q", branch, got, want)
	}
}

func assertPRReviewFileAbsent(t *testing.T, path string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("file %s appeared while the first save held the lock", path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
