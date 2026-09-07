// Command-level tests for the `sentinel review` admitted-run announcement
// (bounded follow-up): each durable review run identity must surface the
// moment the shared transport admits it — synchronously after successful
// durable admission / Start and before the review path waits for completion,
// while provider launch stays independent of diagnostic observation — so an
// operator can immediately run `sentinel runs attach --run <id> --follow`.
// The announcement is wired ONLY at the sentinel-review command boundary and
// travels on the JSON-safe stderr channel, never on stdout where `review
// --json` consumers parse payloads.
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func attachCommandFor(runID string) string {
	return "sentinel runs attach --run " + runID + " --follow"
}

// TestReviewRunAnnouncerPrintsAttachCommandOnce covers the announcer contract:
// one line per UNIQUE admitted run ID carrying the canonical attach command,
// duplicates suppressed, everything written to the injected writer (stderr in
// production) and nowhere else.
func TestReviewRunAnnouncerPrintsAttachCommandOnce(t *testing.T) {
	var out bytes.Buffer
	announcer := newReviewRunAnnouncer(&out)

	announcer.observe("run-aaa")
	announcer.observe("run-aaa") // duplicate observation: must not print twice
	announcer.observe("run-bbb")

	rendered := out.String()
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("announcer wrote %d lines (%q), want exactly one per unique run", len(lines), rendered)
	}
	for _, id := range []string{"run-aaa", "run-bbb"} {
		if !strings.Contains(rendered, id) {
			t.Errorf("announcement omits run id %q:\n%s", id, rendered)
		}
		if n := strings.Count(rendered, attachCommandFor(id)); n != 1 {
			t.Errorf("attach command for %s printed %d times, want exactly 1:\n%s", id, n, rendered)
		}
	}
}

// TestReviewRunAnnouncerConcurrentObservations hammers one announcer from many
// goroutines with overlapping identities: every unique ID must still be
// announced exactly once. Run under -race to prove the mutex discipline.
func TestReviewRunAnnouncerConcurrentObservations(t *testing.T) {
	const goroutines = 16
	var out bytes.Buffer
	announcer := newReviewRunAnnouncer(&out)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < 8; n++ {
				announcer.observe("run-shared")
				announcer.observe("run-" + string(rune('a'+g)))
			}
		}(g)
	}
	wg.Wait()

	rendered := out.String()
	if n := strings.Count(rendered, attachCommandFor("run-shared")); n != 1 {
		t.Errorf("shared run announced %d times, want exactly 1", n)
	}
	for g := 0; g < goroutines; g++ {
		id := "run-" + string(rune('a'+g))
		if n := strings.Count(rendered, attachCommandFor(id)); n != 1 {
			t.Errorf("%s announced %d times, want exactly 1", id, n)
		}
	}
}

// TestSentinelReviewWiringAnnouncesAdmittedRunsOnStderr drives the EXACT
// production wiring `runReview` builds — the announced transport over the
// repository common-dir store — through a real audit. It proves that every
// store-admitted durable run is announced exactly once on the injected stderr
// writer with its actionable attach command, while the command/result stdout
// path stays free of announcement bytes (so `review --json` consumers keep
// parsing valid payloads).
func TestSentinelReviewWiringAnnouncesAdmittedRunsOnStderr(t *testing.T) {
	worktree := cutoverRepository(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(worktree)
	writeTestGateYml(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), cutoverValidationYml)

	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("config load failed: %v", err)
	}
	sha, err := git.ResolveSHA("HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	commitMessage, err := git.CommitMessage(sha)
	if err != nil {
		t.Fatalf("commit message: %v", err)
	}
	diff, err := git.DiffCommit(sha)
	if err != nil {
		t.Fatalf("commit diff: %v", err)
	}
	touchedFiles, err := git.FilesOfCommit(sha)
	if err != nil {
		t.Fatalf("commit files: %v", err)
	}

	var stderrLive bytes.Buffer

	auditorFactory := func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		return &fixedReviewAgent{output: `{"dim":"logic","verdict":"ok","findings":[]}`}, "logic", nil
	}
	auditOptions := review.AuditOptions{
		SHA:     sha,
		Message: commitMessage,
		Diff:    diff,
		Bundles: []review.ReviewBundle{{Name: "requested", Dimensions: []string{"logic"}, Priority: review.PriorityRequired, Cost: 1}},
		// This is the exact call site `runReview` uses; production passes
		// os.Stderr, this fixture captures it.
		ReviewTransport: announcedReviewTransport(cfg, worktree, sha, touchedFiles, &stderrLive),
	}
	// Capture the command/result stdout path for the whole audit: nothing from
	// the announcement wiring may ever land there.
	stdoutCaptured := captureStdout(t, func() {
		auditResult := review.AuditCommit(auditorFactory, 1, auditOptions)
		if auditResult.Verdict != review.VerdictOK {
			t.Fatalf("audit verdict = %q (%+v), want %q through the durable wiring", auditResult.Verdict, auditResult.Dims, review.VerdictOK)
		}
	})

	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatalf("git common dir: %v", err)
	}
	st := store.NewStore(commonDir)
	ids, err := st.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs() error = %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("the durable store holds no runs; the audit did not route through admission")
	}
	announcements := stderrLive.String()
	for _, id := range ids {
		wantCmd := attachCommandFor(string(id))
		if !strings.Contains(announcements, wantCmd) {
			t.Errorf("admitted run %s was not announced with %q;\nlive stream:\n%s", id, wantCmd, announcements)
		}
		if n := strings.Count(announcements, wantCmd); n != 1 {
			t.Errorf("run %s announced %d times, want exactly 1:\n%s", id, n, announcements)
		}
		operation, err := st.ReadRunOperation(id)
		if err != nil {
			t.Fatalf("ReadRunOperation(%s) error = %v", id, err)
		}
		if !strings.HasPrefix(operation, "review") {
			t.Errorf("run %s operation = %q, want a review label", id, operation)
		}
		commit, err := st.ReadRunCommit(id)
		if err != nil {
			t.Fatalf("ReadRunCommit(%s) error = %v", id, err)
		}
		if commit != shortCommit(sha) {
			t.Errorf("run %s commit = %q, want %q", id, commit, shortCommit(sha))
		}
		admittedWorktree, err := st.ReadRunWorktree(id)
		if err != nil {
			t.Fatalf("ReadRunWorktree(%s) error = %v", id, err)
		}
		if admittedWorktree != worktree {
			t.Errorf("run %s worktree = %q, want exact path %q", id, admittedWorktree, worktree)
		}
	}
	// JSON-safety: zero announcement bytes reached the stdout path.
	if strings.Contains(stdoutCaptured, "vas-sentinel:") || strings.Contains(stdoutCaptured, "--follow") {
		t.Errorf("stdout received announcement bytes, corrupting --json output:\n%q", stdoutCaptured)
	}
	if !strings.Contains(announcements, "vas-sentinel:") {
		t.Errorf("announcement lacks the established vas-sentinel diagnostic prefix:\n%s", announcements)
	}
}
