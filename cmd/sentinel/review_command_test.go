package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// captureStdout redirects os.Stdout during f and returns what it wrote.
// The restoration goes in t.Cleanup (not just after an f() that returns
// normally): a t.Fatalf/panic inside f would otherwise leave os.Stdout
// permanently pointing at an already-closed pipe for the rest of the test
// binary.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })
	f()
	writer.Close()
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the pipe: %v", err)
	}
	return string(output)
}

// TestPrintPendingQuestionsJSON covers the machine-readable contract of the
// --json of T7.6: a question with File emits "file", one without File omits it
// (json:"file,omitempty"), and the object exposes exactly sha/pending_questions.
func TestPrintPendingQuestionsJSON(t *testing.T) {
	pending := []review.AgentQuestion{
		{ID: "q1", Text: "does it use camelCase?", File: "a.go"},
		{ID: "q2", Text: "no associated file"},
	}
	output := captureStdout(t, func() { printPendingQuestionsJSON("abc123", pending) })
	expected := `{"sha":"abc123","pending_questions":[{"id":"q1","text":"does it use camelCase?","file":"a.go"},{"id":"q2","text":"no associated file"}]}` + "\n"
	if output != expected {
		t.Errorf("output = %q, expected %q", output, expected)
	}
}

// TestRunReview_UnknownKeyInYml_Exit1WithLine covers Fix 1 (F1, orchestrator
// finding): runReview used LoadLocalConfig (no error). With a broken
// yml, before it went ahead silently until failing later with a git error
// unrelated to the real problem (the worktree of this test is not a repo);
// with LoadStrictLocalConfig it must cut right here with exit 1 and
// the yml error visible.
func TestRunReview_UnknownKeyInYml_Exit1WithLine(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	writeYmlWithUnknownKey(t, worktree)

	output, exit := runAsSubprocess(t, "runReview", worktree, home)

	if exit != 1 {
		t.Errorf("expected exit 1, got %d (output: %q)", exit, output)
	}
	if !strings.Contains(output, "line") {
		t.Errorf("the output must include the line of the yml error, got: %q", output)
	}
}

// FU-6: review must fail before scheduling an audit when the shared
// disposition history is corrupt. Continuing would silently discard an
// answer that changes the effective finding lifecycle.
func TestRunReviewFailsClosedOnCorruptHumanDisposition(t *testing.T) {
	worktree := worktreeWithCorruptHumanDisposition(t)

	output, exit := runAsSubprocess(t, "runReview", worktree, t.TempDir())
	if exit != 1 {
		t.Fatalf("review exit = %d, want 1; output: %q", exit, output)
	}
	if !strings.Contains(output, "reading human dispositions") {
		t.Fatalf("review did not report the disposition read failure: %q", output)
	}
}

// TestParseAuditAnswers covers T7.6: extracting targeted "id=text" answers
// from --answer without breaking the existing usage (free prose, even with
// literal commas, must be reconstructed byte by byte when no token contains
// "=").
func TestParseAuditAnswers(t *testing.T) {
	cases := []struct {
		name         string
		answer       string
		wantByID     map[string]string
		wantLeftover string
	}{
		{"empty", "", map[string]string{}, ""},
		{"free prose without a comma (pre-T7.6 usage)", "not applicable here", map[string]string{}, "not applicable here"},
		{"free prose with a comma is reconstructed as is", "some prose, with a comma", map[string]string{}, "some prose, with a comma"},
		{"a single targeted answer", "q1=uses camelCase", map[string]string{"q1": "uses camelCase"}, ""},
		{"mix of targeted answers and prose, in order", "id1=text one,some prose,id2=text two",
			map[string]string{"id1": "text one", "id2": "text two"}, "some prose"},
		{"spaces are trimmed on both sides", " q1 = text with spaces ",
			map[string]string{"q1": "text with spaces"}, ""},
		{"= with no id before it is not a targeted answer", "=something", map[string]string{}, "=something"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			byID, leftover := parseAuditAnswers(tc.answer)
			if len(byID) != len(tc.wantByID) {
				t.Fatalf("byID = %+v, expected %+v", byID, tc.wantByID)
			}
			for id, text := range tc.wantByID {
				if byID[id] != text {
					t.Errorf("byID[%q] = %q, expected %q", id, byID[id], text)
				}
			}
			if leftover != tc.wantLeftover {
				t.Errorf("leftover = %q, expected %q", leftover, tc.wantLeftover)
			}
		})
	}
}

func TestVerdictExitCode(t *testing.T) {
	cases := []struct {
		verdict string
		want    int
	}{
		{review.VerdictOK, 0},
		{review.VerdictWarn, 0},
		{review.VerdictBlock, 1},
		{review.VerdictQuestion, 3},
		{review.VerdictUnavailable, 4},
	}
	for _, tc := range cases {
		if got := verdictExitCode(tc.verdict); got != tc.want {
			t.Errorf("verdictExitCode(%q) = %d, expected %d", tc.verdict, got, tc.want)
		}
	}
}

func TestDimsResultsForRecord(t *testing.T) {
	rd := []review.DimensionOutcome{
		{Dim: review.DimLogic, Result: &review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK}},
		{Dim: review.DimSecurity, Error: errors.New("failure")},
		{Dim: review.DimSpec, Result: &review.DimensionResult{Dim: review.DimSpec, Verdict: review.VerdictBlock}},
	}
	results := review.DimensionResultsForRecord(rd)
	if len(results) != 2 {
		t.Fatalf("DimensionResultsForRecord = %d results, expected 2 (filters nil)", len(results))
	}
	if results[0].Verdict != review.VerdictOK || results[1].Verdict != review.VerdictBlock {
		t.Errorf("verdicts = %q/%q, expected ok/block", results[0].Verdict, results[1].Verdict)
	}
}

func TestHasCriticalFindings(t *testing.T) {
	build := func(severity string) review.AuditResult {
		return review.AuditResult{
			Dims: []review.DimensionOutcome{{
				Dim: review.DimSecurity,
				Result: &review.DimensionResult{
					Dim:     review.DimSecurity,
					Verdict: review.VerdictWarn,
					Findings: []review.ReviewFinding{{
						Severity: severity,
						File:     "a.go",
					}},
				},
			}},
		}
	}

	if !hasCriticalFindings(build(review.SevCritical)) {
		t.Error("should detect CRITICAL")
	}
	if hasCriticalFindings(build(review.SevWarning)) {
		t.Error("WARNING should not count as critical")
	}
	if hasCriticalFindings(review.AuditResult{}) {
		t.Error("without findings there should be no criticals")
	}
}

func TestRevisionCorrectsPreviousBlock(t *testing.T) {
	dir := t.TempDir()
	ledger := review.NewLedger(dir)

	// No previous record: no correction.
	if review.RevisionFixesPriorBlock(ledger, "abc123", review.VerdictOK) {
		t.Error("without a previous record it should not mark a correction")
	}

	// Previous in block and new without block: corrects.
	rev := review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock}
	if err := ledger.SaveRevision("abc123", "msg", "backend", "m", rev); err != nil {
		t.Fatal(err)
	}
	if !review.RevisionFixesPriorBlock(ledger, "abc123", review.VerdictOK) {
		t.Error("previous in block and new ok should mark a correction")
	}
	if review.RevisionFixesPriorBlock(ledger, "abc123", review.VerdictBlock) {
		t.Error("a new one in block corrects nothing")
	}
}

func TestRecordFixesMarksRecord(t *testing.T) {
	// Real repository with real commits. The fixture used fabricated SHAs and a
	// directory that is not a repository, which stopped being enough when
	// attribution started requiring the audited commit to be an ancestor of the
	// fix (FU-17). The rules it covers — the fix must touch a file named in the
	// findings, and a fix that itself blocks attributes nothing — are unchanged.
	repo, commit := repoWithRealCommits(t)
	gitDir, err := git.GetGitDirFrom(repo)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NewLedger(gitDir)

	blocked := commit("internal/a.go", "package a\n", "feat(x): with bug")

	// Previous record in block with a finding in internal/a.go.
	rev := review.Revision{
		At: time.Now().UTC(), Result: review.VerdictBlock,
		Dims: []review.DimensionResult{{
			Dim: review.DimLogic, Verdict: review.VerdictBlock,
			Findings: []review.ReviewFinding{{
				Severity: review.SevCritical, File: "internal/a.go", Line: 10,
				Description: "real bug",
			}},
		}},
	}
	if err := ledger.SaveRevision(blocked, "feat(x): with bug", "backend", "m", rev); err != nil {
		t.Fatal(err)
	}

	// A fix touching internal/a.go exits without criticals: it must mark the
	// record.
	fix := commit("internal/a.go", "package a // fixed\n", "fix(x): fixes")
	recordFixes(ledger, gitDir, fix, []string{"internal/a.go"}, "fix(x): fixes", 0, repo)
	record, err := ledger.ReadRecord(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if record.FixedIn != fix {
		t.Errorf("FixedIn = %q, expected %q", record.FixedIn, fix)
	}

	// A fix NOT touching the finding's files marks nothing.
	other := commit("internal/b.go", "package b\n", "feat(y): other")
	if err := ledger.SaveRevision(other, "feat(y): other", "backend", "m",
		review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock,
			Dims: []review.DimensionResult{{Dim: review.DimLogic, Verdict: review.VerdictBlock,
				Findings: []review.ReviewFinding{{Severity: review.SevCritical, File: "internal/b.go", Line: 1, Description: "other"}}}}},
	); err != nil {
		t.Fatal(err)
	}
	unrelated := commit("internal/a.go", "package a // something else\n", "fix(y): fixes")
	recordFixes(ledger, gitDir, unrelated, []string{"internal/a.go"}, "fix(y): fixes", 0, repo)
	record, err = ledger.ReadRecord(other)
	if err != nil {
		t.Fatal(err)
	}
	if record.FixedIn != "" {
		t.Errorf("FixedIn = %q, expected empty (the fix does not touch b.go)", record.FixedIn)
	}

	// A commit that exits in block never records corrections.
	inBlock := commit("internal/b.go", "package b // attempt\n", "fix(z): attempt")
	recordFixes(ledger, gitDir, inBlock, []string{"internal/b.go"}, "fix(z): attempt", 1, repo)
	record, err = ledger.ReadRecord(other)
	if err != nil {
		t.Fatal(err)
	}
	if record.FixedIn != "" {
		t.Errorf("FixedIn = %q, expected empty (the fix exited in block)", record.FixedIn)
	}
}

// repoWithRealCommits returns a Git repository and a function that writes a
// file, commits it and returns its SHA. It exists because fix attribution can
// no longer be tested with invented SHAs: it requires real ancestry between
// the audited commit and the fix.
func repoWithRealCommits(t *testing.T) (string, func(path, content, message string) string) {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + repo,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	return repo, func(path, content, message string) string {
		t.Helper()
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", path)
		run("commit", "-qm", message)
		return run("rev-parse", "HEAD")
	}
}

// TestFilterAnswersByRealIDs covers the T7.6 WARNING: an "id=text" token of
// --answer whose "id" matches no real question of this pass is not a targeted
// answer, it is free prose containing a literal "=" (e.g. --answer "the flag
// --gate=true is set"). It must be reconstructed byte by byte into the
// leftover, never sneak into the validated map.
func TestFilterAnswersByRealIDs(t *testing.T) {
	realQuestions := []review.AgentQuestion{{ID: "q1", Text: "should it proceed?", File: "a.go"}}

	cases := []struct {
		name      string
		byID      map[string]string
		leftover  string
		wantValid map[string]string
		wantRest  string
	}{
		{
			name:      "real id stays validated",
			byID:      map[string]string{"q1": "yes"},
			leftover:  "",
			wantValid: map[string]string{"q1": "yes"},
			wantRest:  "",
		},
		{
			// The literal reported case: "--answer \"the flag --gate=true is set\""
			// is today cut into byID["the flag --gate"] = "true is set" because
			// it contains a literal "=", but "the flag --gate" is not the ID of
			// any real question of this pass.
			name:      "unknown id is reconstructed as prose (the flag --gate=true is set)",
			byID:      map[string]string{"the flag --gate": "true is set"},
			leftover:  "",
			wantValid: map[string]string{},
			wantRest:  "the flag --gate=true is set",
		},
		{
			name: "mix: real id is validated, foreign id returns to prose along with the existing leftover",
			byID: map[string]string{
				"q1":              "yes",
				"the flag --gate": "true is set",
			},
			leftover:  "some pre-existing prose",
			wantValid: map[string]string{"q1": "yes"},
			wantRest:  "the flag --gate=true is set,some pre-existing prose",
		},
		{
			name:      "without byID the leftover does not change",
			byID:      map[string]string{},
			leftover:  "prose only",
			wantValid: map[string]string{},
			wantRest:  "prose only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validated, rest := filterAnswersByRealIDs(tc.byID, tc.leftover, realQuestions)
			if len(validated) != len(tc.wantValid) {
				t.Fatalf("validated = %+v, expected %+v", validated, tc.wantValid)
			}
			for id, text := range tc.wantValid {
				if validated[id] != text {
					t.Errorf("validated[%q] = %q, expected %q", id, validated[id], text)
				}
			}
			if rest != tc.wantRest {
				t.Errorf("rest = %q, expected %q", rest, tc.wantRest)
			}
		})
	}
}

// runGitForReviewTest runs git and fails the test if the command does not exit
// successfully, with the combined output in the error message.
func runGitForReviewTest(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// testRepoWithOneCommit creates a real git repository with a single commit
// that adds fileName with real content, and positions the test process in its
// directory: git.BlobFileAtCommit resolves the blob via
// "git rev-parse <sha>:<path>" against the process cwd (it does not accept
// -C), like TestAnalyzeBranchSurvivesRebaseViaBlob in
// internal/review/rebase_test.go (the convention already established in this
// codebase for this kind of fixture).
// It returns the repo directory (= worktree = git common dir, without linked
// worktrees) and the SHA of the commit.
func testRepoWithOneCommit(t *testing.T, fileName, content string) (repo, sha string) {
	t.Helper()
	repo = t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		runGitForReviewTest(t, args...)
	}
	if err := os.WriteFile(fileName, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGitForReviewTest(t, "add", fileName)
	runGitForReviewTest(t, "commit", "-m", "feat(test): test content")

	output, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return repo, strings.TrimSpace(string(output))
}

// fakeSequentialReviewAgent returns its fixed JSONL answers in order, then
// repeats the final one indefinitely. auditWithAgent (internal/review/engine.go)
// makes its own extra round when the first answer is "question" and
// opts.Answers is not empty. A genuinely repeating agent (the T7.6
// deadlock case) must therefore return "question" on every call so that
// internal round trip cannot disguise it as "ok". This duplicates the
// pattern from agenteFake in internal/review/engine_test.go and
// rebaseReviewerStub in internal/review/rebase_test.go because neither type
// is exported from internal/review.
type fakeSequentialReviewAgent struct {
	answers []string
	calls   int
	// prompts records each received prompt in order. It proves that
	// Answers (opts.Answers) reaches the agent in auditWithAgent's
	// second internal sub-round only after the first answer is "question",
	// not merely that the final verdict is expected.
	prompts []string
}

func (a *fakeSequentialReviewAgent) RunPrompt(prompt string) (string, error) {
	a.prompts = append(a.prompts, prompt)
	defer func() { a.calls++ }()
	if len(a.answers) == 0 {
		return `{"dim":"logic","verdict":"ok"}`, nil
	}
	idx := a.calls
	if idx >= len(a.answers) {
		idx = len(a.answers) - 1
	}
	return a.answers[idx], nil
}

// RunReview implements the internal restricted-reviewer interface.
// auditWithAgent requires that type; without it, the injected agent returns
// VerdictUnavailable ("restricted reviewer capability is required") rather
// than parsing the fixture response.
func (a *fakeSequentialReviewAgent) RunReview(prompt, _ string, _ []string) (string, error) {
	return a.RunPrompt(prompt)
}

func (a *fakeSequentialReviewAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

func fakeSequentialReviewFactory(answers []string) review.ReviewerFactory {
	factory, _ := fakeSequentialReviewFactoryCapturing(answers)
	return factory
}

// fakeSequentialReviewFactoryCapturing is fakeSequentialReviewFactory but also
// returns the underlying *fakeSequentialReviewAgent, so the test can inspect
// the prompts it actually received (e.g. confirm which Answers text
// arrived in the second internal sub-round).
func fakeSequentialReviewFactoryCapturing(answers []string) (review.ReviewerFactory, *fakeSequentialReviewAgent) {
	fake := &fakeSequentialReviewAgent{answers: answers}
	return func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		return fake, "test", nil
	}, fake
}

func testReviewBundles() []review.ReviewBundle {
	return []review.ReviewBundle{{Name: "test", Dimensions: []string{review.DimLogic}, Priority: review.PriorityRequired, Cost: 1}}
}

// TestApplyPendingQuestions_RegisteredAnswer_UnblocksWithoutAskingAgain
// is the literal acceptance criterion of T7.6: with an answer already
// registered for (blob, questionID), a second audit of the same blob
// does not include that question in the pending --json and applies the
// registered answer without blocking execution. It also covers CRITICAL #2
// (the final verdict must reflect the deduplicated state, not the raw
// "question" result carried in before deduplication).
func TestApplyPendingQuestions_RegisteredAnswer_UnblocksWithoutAskingAgain(t *testing.T) {
	repo, sha := testRepoWithOneCommit(t, "a.go", "package a\n")
	blob, err := git.BlobFileAtCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit: %v", err)
	}

	// The store must live in the same git common dir that
	// applyPendingQuestions will read (GetGitCommonDir(worktree)), not in
	// the worktree root: in a repo without linked worktrees that is
	// <repo>/.git, but that is a git implementation detail that must not be
	// assumed by hand.
	gitCommonDir, err := git.GetGitCommonDir(repo)
	if err != nil {
		t.Fatalf("GetGitCommonDir: %v", err)
	}
	st := store.NewStore(gitCommonDir)
	if err := st.RecordAnswer(blob, "q1", "yes", "test-actor"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}

	result := review.AuditResult{
		Verdict:   review.VerdictQuestion,
		Questions: []review.AgentQuestion{{ID: "q1", Text: "should the change proceed?", File: "a.go"}},
	}
	// The retry round must resolve cleanly: the agent does not ask again after
	// receiving the answer.
	factory := fakeSequentialReviewFactory([]string{`{"dim":"logic","verdict":"ok"}`})
	options := review.AuditOptions{SHA: sha, Bundles: testReviewBundles()}

	finalResult, pending, applyErr := applyPendingQuestions(
		repo, sha, factory, config.Config{}, modelprobe.NewVerifier(nil), options, result)
	if applyErr != nil {
		t.Fatalf("applyPendingQuestions: %v", applyErr)
	}

	if len(pending) != 0 {
		t.Fatalf("pending = %+v, expected empty", pending)
	}
	if finalResult.Verdict != review.VerdictOK {
		t.Fatalf("Verdict = %q, expected %q (the registered answer unblocks without asking again)",
			finalResult.Verdict, review.VerdictOK)
	}
	if len(finalResult.Questions) != 0 {
		t.Fatalf("finalResult.Questions = %+v, expected empty (it must reflect the deduplicated state)", finalResult.Questions)
	}
}

// TestApplyPendingQuestions_AgentAsksAgain_DoesNotDeadlock covers CRITICAL #2,
// deadlock case: the agent may legitimately re-emit verdict:"question" with
// the SAME question even after receiving the clarification in the retry round
// (a model is not guaranteed to stop asking just because it got the answer).
// Without the verdict reconciliation, this would block forever (exit 3 on
// every future run over the same content) — here it must be downgraded to
// warn.
func TestApplyPendingQuestions_AgentAsksAgain_DoesNotDeadlock(t *testing.T) {
	repo, sha := testRepoWithOneCommit(t, "a.go", "package a\n")
	blob, err := git.BlobFileAtCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit: %v", err)
	}

	// The store must live in the same git common dir that
	// applyPendingQuestions will read (GetGitCommonDir(worktree)), not in
	// the worktree root: in a repo without linked worktrees that is
	// <repo>/.git, but that is a git implementation detail that must not be
	// assumed by hand.
	gitCommonDir, err := git.GetGitCommonDir(repo)
	if err != nil {
		t.Fatalf("GetGitCommonDir: %v", err)
	}
	st := store.NewStore(gitCommonDir)
	if err := st.RecordAnswer(blob, "q1", "yes", "test-actor"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}

	result := review.AuditResult{
		Verdict:   review.VerdictQuestion,
		Questions: []review.AgentQuestion{{ID: "q1", Text: "should the change proceed?", File: "a.go"}},
	}
	// The retry round simulates an agent insisting on the same question even
	// after receiving the already registered answer.
	factory := fakeSequentialReviewFactory([]string{
		`{"dim":"logic","verdict":"question","questions":[{"id":"q1","text":"should the change proceed?","file":"a.go"}]}`,
	})
	options := review.AuditOptions{SHA: sha, Bundles: testReviewBundles()}

	finalResult, pending, applyErr := applyPendingQuestions(
		repo, sha, factory, config.Config{}, modelprobe.NewVerifier(nil), options, result)
	if applyErr != nil {
		t.Fatalf("applyPendingQuestions: %v", applyErr)
	}

	if len(pending) != 0 {
		t.Fatalf("pending = %+v, expected empty (there is already a registered answer for q1)", pending)
	}
	if finalResult.Verdict != review.VerdictWarn {
		t.Fatalf("Verdict = %q, expected %q (never %q: that would be the permanent deadlock)",
			finalResult.Verdict, review.VerdictWarn, review.VerdictQuestion)
	}
}

// TestApplyPendingQuestions_AnsweredSharesIDAcrossFiles_LosesNone is the
// literal regression of the CRITICAL found in the previous fix round:
// SplitPendingQuestions stopped collapsing by ID (T7.6 fix #1), but the only
// caller kept re-collapsing the result into a map[string]string indexed only
// by ID before building the retry prompt — the defect changed no observable
// behavior, only location. Here two questions of different "dimensions" share
// the ID "q1" but have different files and already registered answers: both
// must reach the agent as separate lines, not collapse into one.
func TestApplyPendingQuestions_AnsweredSharesIDAcrossFiles_LosesNone(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		runGitForReviewTest(t, args...)
	}
	if err := os.WriteFile("a.go", []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.go", []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitForReviewTest(t, "add", "a.go", "b.go")
	runGitForReviewTest(t, "commit", "-m", "feat(test): two files")
	output, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(output))

	blobA, err := git.BlobFileAtCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit a.go: %v", err)
	}
	blobB, err := git.BlobFileAtCommit(sha, "b.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit b.go: %v", err)
	}

	gitCommonDir, err := git.GetGitCommonDir(repo)
	if err != nil {
		t.Fatalf("GetGitCommonDir: %v", err)
	}
	st := store.NewStore(gitCommonDir)
	if err := st.RecordAnswer(blobA, "q1", "stored answer for a.go", "test-actor"); err != nil {
		t.Fatalf("RecordAnswer a.go: %v", err)
	}
	if err := st.RecordAnswer(blobB, "q1", "stored answer for b.go", "test-actor"); err != nil {
		t.Fatalf("RecordAnswer b.go: %v", err)
	}

	result := review.AuditResult{
		Verdict: review.VerdictQuestion,
		Questions: []review.AgentQuestion{
			{ID: "q1", Text: "should it proceed in a.go?", File: "a.go"},
			{ID: "q1", Text: "should it proceed in b.go?", File: "b.go"},
		},
	}
	// The first internal sub-round of auditWithAgent always asks WITHOUT
	// Answers. Only a "question" first answer triggers a second sub-round
	// WITH embedded Answers. The first item triggers that round; the second
	// is the assertion target, the prompt received in the second call.
	factory, fake := fakeSequentialReviewFactoryCapturing([]string{
		`{"dim":"logic","verdict":"question"}`,
		`{"dim":"logic","verdict":"ok"}`,
	})
	options := review.AuditOptions{
		SHA: sha, Bundles: testReviewBundles(),
		Answers: "q1@b.go=fresh answer for b.go",
	}

	finalResult, pending, applyErr := applyPendingQuestions(
		repo, sha, factory, config.Config{}, modelprobe.NewVerifier(nil), options, result)
	if applyErr != nil {
		t.Fatalf("applyPendingQuestions: %v", applyErr)
	}

	if len(pending) != 0 {
		t.Fatalf("pending = %+v, expected empty", pending)
	}
	if finalResult.Verdict != review.VerdictOK {
		t.Fatalf("Verdict = %q, expected %q", finalResult.Verdict, review.VerdictOK)
	}
	promptWithAnswers, ok := promptWithClarifications(fake.prompts)
	if !ok {
		t.Fatalf("no received prompt contains the user clarifications section: %+v", fake.prompts)
	}
	if strings.Contains(promptWithAnswers, "stored answer for b.go") {
		t.Errorf("the retry prompt kept the stored answer overridden for b.go:\n%s", promptWithAnswers)
	}
	if !strings.Contains(promptWithAnswers, "q1@a.go: stored answer for a.go") {
		t.Errorf("the retry prompt lost the stored answer for a.go:\n%s", promptWithAnswers)
	}
	if !strings.Contains(promptWithAnswers, "q1@b.go: fresh answer for b.go") {
		t.Errorf("the retry prompt does not contain the fresh qualified answer for b.go:\n%s", promptWithAnswers)
	}
	storedLine := strings.Index(promptWithAnswers, "q1@a.go: stored answer for a.go")
	freshLine := strings.Index(promptWithAnswers, "q1@b.go: fresh answer for b.go")
	if storedLine == -1 || freshLine == -1 {
		t.Fatalf("qualified answer lines are missing from the retry prompt:\n%s", promptWithAnswers)
	}
	if storedLine > freshLine {
		t.Errorf("qualified answer lines are not ordered by (ID, File):\n%s", promptWithAnswers)
	}
	if got, ok, err := st.RecordedAnswer(blobA, "q1"); err != nil || !ok || got != "stored answer for a.go" {
		t.Errorf("stored answer for a.go = %q, %v, %v; want original stored answer", got, ok, err)
	}
	if got, ok, err := st.RecordedAnswer(blobB, "q1"); err != nil || !ok || got != "fresh answer for b.go" {
		t.Errorf("stored answer for b.go = %q, %v, %v; want fresh qualified answer", got, ok, err)
	}
}

// TestApplyPendingQuestions_AmbiguousBareAnswerPropagatesWithoutRetryOrPersistence
// proves that a bare answer cannot select one of several questions sharing an
// ID: the caller returns the deterministic ambiguity error before retrying the
// audit or persisting an answer for either file.
func TestApplyPendingQuestions_AmbiguousBareAnswerPropagatesWithoutRetryOrPersistence(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		runGitForReviewTest(t, args...)
	}
	if err := os.WriteFile("a.go", []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.go", []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitForReviewTest(t, "add", "a.go", "b.go")
	runGitForReviewTest(t, "commit", "-m", "feat(test): ambiguous question fixtures")
	shaOutput, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(shaOutput))

	blobA, err := git.BlobFileAtCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit a.go: %v", err)
	}
	blobB, err := git.BlobFileAtCommit(sha, "b.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit b.go: %v", err)
	}
	gitCommonDir, err := git.GetGitCommonDir(repo)
	if err != nil {
		t.Fatalf("GetGitCommonDir: %v", err)
	}
	st := store.NewStore(gitCommonDir)

	result := review.AuditResult{
		Verdict: review.VerdictQuestion,
		Questions: []review.AgentQuestion{
			{ID: "q1", Text: "question for b.go", File: "b.go"},
			{ID: "q1", Text: "question for a.go", File: "a.go"},
		},
	}
	fake := &fakeSequentialReviewAgent{answers: []string{`{"dim":"logic","verdict":"ok"}`}}
	factoryCalls := 0
	factory := func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		factoryCalls++
		return fake, "test", nil
	}
	options := review.AuditOptions{
		SHA: sha, Bundles: testReviewBundles(), Answers: "q1=bare answer",
	}

	_, pending, applyErr := applyPendingQuestions(
		repo, sha, factory, config.Config{}, modelprobe.NewVerifier(nil), options, result)
	const wantErr = `answer "q1" is ambiguous; qualify one of: q1@a.go, q1@b.go`
	if applyErr == nil {
		t.Fatal("expected the ambiguous bare answer error")
	}
	if applyErr.Error() != wantErr {
		t.Fatalf("applyPendingQuestions error = %q, want %q", applyErr, wantErr)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %+v, want both original questions", pending)
	}
	if factoryCalls != 0 || fake.calls != 0 || len(fake.prompts) != 0 {
		t.Fatalf("ambiguous answer triggered an audit retry: factory calls=%d, agent calls=%d, prompts=%d", factoryCalls, fake.calls, len(fake.prompts))
	}

	for _, fixture := range []struct {
		file string
		blob string
	}{
		{file: "a.go", blob: blobA},
		{file: "b.go", blob: blobB},
	} {
		answer, ok, lookupErr := st.RecordedAnswer(fixture.blob, "q1")
		if lookupErr != nil {
			t.Fatalf("RecordedAnswer %s: %v", fixture.file, lookupErr)
		}
		if ok {
			t.Errorf("ambiguous answer persisted for %s: %q", fixture.file, answer)
		}
	}
}

// promptWithClarifications searches, among the captured prompts, the first
// one carrying the user clarifications section (literal marker of
// internal/review/prompts.go: answersSection). Searching by content
// instead of indexing by fixed position keeps the test from breaking silently
// if the engine adds an extra round (e.g. a refutation round) that shifts the
// indices.
func promptWithClarifications(prompts []string) (string, bool) {
	for _, p := range prompts {
		if strings.Contains(p, "Clarifications from the user") {
			return p, true
		}
	}
	return "", false
}

// TestApplyPendingQuestions_FreshAnswerPrevailsOverTheStoreOne covers the
// regression found after the ID-collision CRITICAL fix: when building the
// retry prompt directly from `answered` (without collapsing by ID), an id
// present BOTH in the store (answered) AND in a fresh --answer answer (byID)
// for that same id had to keep resolving to a single line, with the fresh one
// winning — not with two contradictory "q1: ..." lines in the same prompt.
func TestApplyPendingQuestions_FreshAnswerPrevailsOverTheStoreOne(t *testing.T) {
	repo, sha := testRepoWithOneCommit(t, "a.go", "package a\n")
	blob, err := git.BlobFileAtCommit(sha, "a.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit: %v", err)
	}
	gitCommonDir, err := git.GetGitCommonDir(repo)
	if err != nil {
		t.Fatalf("GetGitCommonDir: %v", err)
	}
	st := store.NewStore(gitCommonDir)
	if err := st.RecordAnswer(blob, "q1", "old answer from the store", "other-actor"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}

	result := review.AuditResult{
		Verdict:   review.VerdictQuestion,
		Questions: []review.AgentQuestion{{ID: "q1", Text: "should the change proceed?", File: "a.go"}},
	}
	factory, fake := fakeSequentialReviewFactoryCapturing([]string{
		`{"dim":"logic","verdict":"question"}`,
		`{"dim":"logic","verdict":"ok"}`,
	})
	options := review.AuditOptions{SHA: sha, Bundles: testReviewBundles(), Answers: "q1=fresh answer from this invocation"}

	finalResult, pending, applyErr := applyPendingQuestions(
		repo, sha, factory, config.Config{}, modelprobe.NewVerifier(nil), options, result)
	if applyErr != nil {
		t.Fatalf("applyPendingQuestions: %v", applyErr)
	}

	if len(pending) != 0 {
		t.Fatalf("pending = %+v, expected empty", pending)
	}
	if finalResult.Verdict != review.VerdictOK {
		t.Fatalf("Verdict = %q, expected %q", finalResult.Verdict, review.VerdictOK)
	}
	prompt, ok := promptWithClarifications(fake.prompts)
	if !ok {
		t.Fatalf("no received prompt contains the user clarifications section: %+v", fake.prompts)
	}
	if strings.Contains(prompt, "old answer from the store") {
		t.Errorf("the retry prompt includes the store's old answer; it must yield to the fresh one from this invocation:\n%s", prompt)
	}
	if !strings.Contains(prompt, "q1: fresh answer from this invocation") {
		t.Errorf("the retry prompt does not contain the fresh answer:\n%s", prompt)
	}
	if strings.Count(prompt, "q1:") != 1 {
		t.Errorf("the retry prompt has %d \"q1:\" lines, expected exactly 1 (no contradictory lines): %s",
			strings.Count(prompt, "q1:"), prompt)
	}
}

// TestApplyPendingQuestions_EmptyQuestionWithoutRetry_NotDowngradedToWarn
// covers the retried==false branch: a "question" verdict with empty Questions
// from the very first pass (with no known or fresh answer at all) must not be
// downgraded to warn — that would mask malformed model output as an already
// resolved deadlock. This branch had never been exercised: the other
// applyPendingQuestions tests always enter the retry block.
func TestApplyPendingQuestions_EmptyQuestionWithoutRetry_NotDowngradedToWarn(t *testing.T) {
	repo, sha := testRepoWithOneCommit(t, "a.go", "package a\n")

	result := review.AuditResult{
		Verdict:   review.VerdictQuestion,
		Questions: nil,
	}
	factory := fakeSequentialReviewFactory(nil)
	options := review.AuditOptions{SHA: sha, Bundles: testReviewBundles()}

	finalResult, pending, applyErr := applyPendingQuestions(
		repo, sha, factory, config.Config{}, modelprobe.NewVerifier(nil), options, result)
	if applyErr != nil {
		t.Fatalf("applyPendingQuestions: %v", applyErr)
	}

	if len(pending) != 0 {
		t.Fatalf("pending = %+v, expected empty", pending)
	}
	if finalResult.Verdict != review.VerdictQuestion {
		t.Fatalf("Verdict = %q, expected %q (it must not be downgraded to warn without a retry)",
			finalResult.Verdict, review.VerdictQuestion)
	}
}

func TestResolveQuestionAnswers(t *testing.T) {
	questions := []review.AgentQuestion{
		{ID: "q1", File: "b.go"},
		{ID: "q1", File: "a.go"},
		{ID: "q2", File: "c.go"},
	}
	tests := []struct {
		name      string
		raw       map[string]string
		want      map[questionKey]string
		wantProse string
		wantErr   string
	}{
		{
			name: "bare unique remains compatible",
			raw:  map[string]string{"q2": "unique"},
			want: map[questionKey]string{{ID: "q2", File: "c.go"}: "unique"},
		},
		{
			name: "qualified selector chooses one duplicate",
			raw:  map[string]string{"q1@b.go": "only b"},
			want: map[questionKey]string{{ID: "q1", File: "b.go"}: "only b"},
		},
		{
			name:    "ambiguous bare selector reports deterministic candidates",
			raw:     map[string]string{"q1": "ambiguous"},
			wantErr: `answer "q1" is ambiguous; qualify one of: q1@a.go, q1@b.go`,
		},
		{
			name:      "unknown qualified selector remains prose",
			raw:       map[string]string{"q1@missing.go": "unknown"},
			want:      map[questionKey]string{},
			wantProse: "q1@missing.go=unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, prose, err := resolveQuestionAnswers(tt.raw, "", questions)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("expected an ambiguity error")
				}
				if err.Error() != tt.wantErr {
					t.Errorf("error = %q, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveQuestionAnswers: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("answers = %#v, want %#v", got, tt.want)
			}
			if prose != tt.wantProse {
				t.Errorf("prose = %q, want %q", prose, tt.wantProse)
			}
		})
	}
}

// TestRecordFixesDoesNotCrossBranches closes FU-17. Correction attribution
// tested only whether the fix touched a file named in the record's findings.
// While each checkout had its own ledger that was bounded by accident; once
// review, status and pr shared one ledger per repository, a fix( commit on one
// branch could clear a block recorded on an unrelated branch because both
// happened to touch the same file.
//
// The fixture is two real branches from one root, both changing the same path,
// so the only thing that can separate them is ancestry.
func TestRecordFixesDoesNotCrossBranches(t *testing.T) {
	worktree := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeAndCommit := func(content, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(worktree, "x.go"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "x.go")
		run("commit", "-qm", message)
		return run("rev-parse", "HEAD")
	}
	run("init", "-q", "-b", "main")
	writeAndCommit("package x\n", "feat(x): root")
	root := run("rev-parse", "HEAD")

	// The audited commit, blocked, on main.
	audited := writeAndCommit("package x // audited\n", "feat(x): audited")

	// The fix, on a branch that forked BEFORE the audited commit. It touches the
	// same file, so only ancestry tells the two apart.
	run("checkout", "-q", "-b", "other", root)
	fix := writeAndCommit("package x // unrelated fix\n", "fix(x): unrelated")

	gitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NewLedger(gitDir)
	if err := ledger.SaveRevision(audited, "feat(x): audited", "b", "m", review.Revision{
		At: time.Now(), Result: review.VerdictBlock,
		Dims: []review.DimensionResult{{Dim: review.DimLogic, Verdict: review.VerdictBlock,
			Findings: []review.ReviewFinding{{File: "x.go", Severity: "CRITICAL", Description: "d"}}}},
	}); err != nil {
		t.Fatal(err)
	}

	recordFixes(ledger, gitDir, fix, []string{"x.go"}, "fix(x): unrelated", 0, worktree)

	record, err := ledger.ReadRecord(audited)
	if err != nil || record == nil {
		t.Fatalf("ReadRecord() = %v, %v", record, err)
	}
	if record.FixedIn != "" {
		t.Errorf("FixedIn = %q; a fix on a branch that does not contain the audited commit cleared its block because both touched x.go", record.FixedIn)
	}

	// The same fix, made on the audited commit's own line of history, must still
	// clear it: the check must not have simply disabled attribution.
	run("checkout", "-q", "main")
	descendant := writeAndCommit("package x // real fix\n", "fix(x): real")
	recordFixes(ledger, gitDir, descendant, []string{"x.go"}, "fix(x): real", 0, worktree)

	record, err = ledger.ReadRecord(audited)
	if err != nil || record == nil {
		t.Fatalf("ReadRecord() = %v, %v", record, err)
	}
	if record.FixedIn != descendant {
		t.Errorf("FixedIn = %q, want %q: a fix descending from the audited commit must still clear its block", record.FixedIn, descendant)
	}
}
