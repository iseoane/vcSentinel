package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/intent"
)

// prepareRepoWithCommits creates a test repo with an initial state
// (base.txt), one commit adding a.go and another adding b.go.
func prepareRepoWithCommits(t *testing.T) string {
	t.Helper()
	dir := prepareTestRepo(t, map[string]string{"base.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatalf("could not create a.go: %v", err)
	}
	runGitInDir(t, dir, "add", "a.go")
	runGitInDir(t, dir, "commit", "-m", "feat(a): first commit")
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644); err != nil {
		t.Fatalf("could not create b.go: %v", err)
	}
	runGitInDir(t, dir, "add", "b.go")
	runGitInDir(t, dir, "commit", "-m", "feat(b): second commit")
	return dir
}

func TestCommitIntentReadsFullMessageTrailers(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "intent.txt"), []byte("intent\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "intent.txt")
	runGitInDir(t, dir, "commit", "-m", "feat: intent", "-m", intent.Render(intent.Intent{Text: "protect the release", Source: intent.SourceDeclared}))
	head, err := SHAHead()
	if err != nil {
		t.Fatal(err)
	}
	got, err := CommitIntent(head)
	if err != nil {
		t.Fatal(err)
	}
	want := intent.Intent{Text: "protect the release", Source: intent.SourceDeclared}
	if got != want {
		t.Fatalf("CommitIntent() = %+v, want %+v", got, want)
	}
}

func TestIntentSurvivesMultiCommitApplyAndRebase(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	prepareTempRepo(t)
	commitInRepo(t, "base.txt", "base\n")
	runGitCommand(t, "checkout", "-q", "-b", "feature/intent")
	if err := os.MkdirAll("web", 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"backend.go":  "package backend\n\nfunc Changed() {}\n",
		"web/app.tsx": "export const changed = true;\n",
		"config.yaml": "changed: true\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.FromSlash(path), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	want := intent.Intent{Text: "protect the release", Source: intent.SourceDeclared}
	plan, err := BuildPlanForAgentWithOptions(nil, SemanticSliceOptions{Intent: want.Text, IntentSource: want.Source})
	if err != nil {
		t.Fatalf("BuildPlanForAgentWithOptions() error = %v", err)
	}
	if len(plan.Batches) != 3 {
		t.Fatalf("production plan batches = %d, want 3: %+v", len(plan.Batches), plan.Batches)
	}
	results, err := ApplyApprovedPlan(plan, PlanAnswers{PlanID: plan.PlanID})
	if err != nil {
		t.Fatalf("ApplyApprovedPlan() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("created commits = %d, want 3", len(results))
	}
	for _, result := range results {
		got, err := CommitIntent(result.Hash)
		if err != nil {
			t.Fatalf("CommitIntent(%s) error = %v", result.Hash, err)
		}
		if got != want {
			t.Fatalf("CommitIntent(%s) = %+v, want %+v", result.Hash, got, want)
		}
	}

	runGitCommand(t, "checkout", "-q", "main")
	newBase := commitInRepo(t, "base-update.txt", "new base\n")
	runGitCommand(t, "checkout", "-q", "feature/intent")
	runGitCommand(t, "rebase", "main")
	rewritten, err := RangeSHAs(newBase, "HEAD")
	if err != nil {
		t.Fatalf("RangeSHAs() after rebase error = %v", err)
	}
	if len(rewritten) != 3 {
		t.Fatalf("rewritten commits = %d, want 3", len(rewritten))
	}
	for _, sha := range rewritten {
		got, err := CommitIntent(sha)
		if err != nil {
			t.Fatalf("CommitIntent(%s) after rebase error = %v", sha, err)
		}
		if got != want {
			t.Fatalf("CommitIntent(%s) after rebase = %+v, want %+v", sha, got, want)
		}
	}
}

func TestCommitMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	head, err := SHAHead()
	if err != nil {
		t.Fatalf("SHAHead returned error: %v", err)
	}
	message, err := CommitMessage(head)
	if err != nil {
		t.Fatalf("CommitMessage returned error: %v", err)
	}
	if message != "feat(b): second commit" {
		t.Errorf("message = %q, expected 'feat(b): second commit'", message)
	}
}

func TestDiffCommitContainsFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	diff, err := DiffCommit(head)
	if err != nil {
		t.Fatalf("DiffCommit returned error: %v", err)
	}
	if !strings.Contains(diff, "b.go") {
		t.Errorf("the diff should mention b.go, got: %s", diff)
	}
}

func TestRangeSHAsChronological(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	all, err := RangeSHAs("", "HEAD")
	if err != nil {
		t.Fatalf("RangeSHAs returned error: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("RangeSHAs(\"\", HEAD) = %d commits, expected 3 (initial + 2)", len(all))
	}

	// Range from the initial state: only the two working commits.
	shas, err := RangeSHAs(all[0], "HEAD")
	if err != nil {
		t.Fatalf("RangeSHAs returned error: %v", err)
	}
	if len(shas) != 2 {
		t.Fatalf("RangeSHAs = %d commits, expected 2", len(shas))
	}
	first, _ := CommitMessage(shas[0])
	second, _ := CommitMessage(shas[1])
	if first != "feat(a): first commit" || second != "feat(b): second commit" {
		t.Errorf("chronological order broken: %q, %q", first, second)
	}
}

func TestFilesOfCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	shas, _ := RangeSHAs("", "HEAD")
	// shas[1] is "feat(a): first commit" (shas[0] is the initial state).
	files, err := FilesOfCommit(shas[1])
	if err != nil {
		t.Fatalf("FilesOfCommit returned error: %v", err)
	}
	if len(files) != 1 || files[0] != "a.go" {
		t.Errorf("files = %+v, expected [a.go]", files)
	}
}

func TestUpToSHAsChronological(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	shas, err := UpToSHAs("HEAD")
	if err != nil {
		t.Fatalf("UpToSHAs returned error: %v", err)
	}
	if len(shas) != 3 {
		t.Fatalf("UpToSHAs = %d commits, expected 3", len(shas))
	}
	first, _ := CommitMessage(shas[0])
	last, _ := CommitMessage(shas[2])
	if first != "initial state" || last != "feat(b): second commit" {
		t.Errorf("order broken: %q ... %q", first, last)
	}
}

func TestCurrentBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	runGitInDir(t, dir, "checkout", "-b", "feature/x")
	branch, err := CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch returned error: %v", err)
	}
	if branch != "feature/x" {
		t.Errorf("CurrentBranch = %q, expected feature/x", branch)
	}
}

func TestResolveSHA(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	resolved, err := ResolveSHA("HEAD")
	if err != nil {
		t.Fatalf("ResolveSHA(HEAD) returned error: %v", err)
	}
	if resolved != head {
		t.Errorf("ResolveSHA(HEAD) = %q, expected %q", resolved, head)
	}

	if _, err := ResolveSHA("HEAD~1"); err != nil {
		t.Errorf("ResolveSHA(HEAD~1) returned error: %v", err)
	}
	if _, err := ResolveSHA("no-such-ref"); err == nil {
		t.Error("ResolveSHA(invalid ref) should fail")
	}
}

func TestCommitExists(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	if !CommitExists(head) {
		t.Error("CommitExists(HEAD) = false, expected true")
	}
	if CommitExists(strings.Repeat("0", 40)) {
		t.Error("CommitExists(nonexistent sha) = true, expected false")
	}
}

func TestFileContentAtCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	shas, _ := RangeSHAs("", "HEAD")
	// shas[1] is "feat(a): first commit", which adds a.go with "package a\n".
	content, err := FileContentAtCommit(shas[1], "a.go")
	if err != nil {
		t.Fatalf("FileContentAtCommit returned error: %v", err)
	}
	if content != "package a\n" {
		t.Errorf("content = %q, expected %q", content, "package a\n")
	}

	if _, err := FileContentAtCommit(shas[1], "missing.go"); err == nil {
		t.Error("FileContentAtCommit with a file that does not exist in that commit should return an error")
	}

	if c, p, e := ReadPathAtRevision(shas[1], "a.go"); !p || e != nil || c != "package a\n" {
		t.Errorf("ReadPathAtRevision present = %q/%v/%v", c, p, e)
	}
	if _, p, e := ReadPathAtRevision(shas[1], "missing.go"); p || e != nil {
		t.Errorf("ReadPathAtRevision absent = %v %v", p, e)
	}
	if _, _, e := ReadPathAtRevision("not-a-rev", "a.go"); e == nil {
		t.Error("invalid revision must fail, never report absence")
	}
}

func TestBlobFileAtCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available on the PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	shas, _ := RangeSHAs("", "HEAD")
	// shas[1] is "feat(a): first commit", which adds a.go with "package a\n".
	blob, err := BlobFileAtCommit(shas[1], "a.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit returned an error: %v", err)
	}
	if blob == "" {
		t.Fatal("BlobFileAtCommit returned an empty hash")
	}

	// Independent verification: git cat-file -p <blob> must return exactly
	// the content written in the commit.
	content, err := runGitOutput("cat-file", "-p", blob)
	if err != nil {
		t.Fatalf("git cat-file -p %s failed: %v", blob, err)
	}
	if content != "package a\n" {
		t.Errorf("blob content = %q, expected %q", content, "package a\n")
	}

	// b.go has different content: its blob must differ from a.go's
	// (confirms it is not a fixed hash, but the real content's).
	blobB, err := BlobFileAtCommit(shas[2], "b.go")
	if err != nil {
		t.Fatalf("BlobFileAtCommit returned an error: %v", err)
	}
	if blobB == blob {
		t.Errorf("b.go blob matches a.go's: %s", blob)
	}

	if _, err := BlobFileAtCommit(shas[1], "missing.go"); err == nil {
		t.Error("BlobFileAtCommit with a file that does not exist in that commit should return an error")
	}
}

func TestUpstreamOrMainChoosesMain(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareRepoWithCommits(t)
	t.Chdir(dir)

	// Without an upstream: it must fall back to the local main or master
	// branch.
	runGitInDir(t, dir, "checkout", "-b", "main")
	base, err := UpstreamOrMain()
	if err != nil {
		t.Fatalf("UpstreamOrMain returned error: %v", err)
	}
	if base != "main" {
		t.Errorf("UpstreamOrMain = %q, expected main", base)
	}
}

// prepareRepoWithMerge builds a repo with a real two-parent merge commit:
// master changes base.txt and a feat branch adds feature.go; the --no-ff
// merge joins them without conflict.
func prepareRepoWithMerge(t *testing.T) string {
	t.Helper()
	dir := prepareTestRepo(t, map[string]string{"base.txt": "base\n"})
	runGitInDir(t, dir, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package feature\n"), 0644); err != nil {
		t.Fatalf("could not create feature.go: %v", err)
	}
	runGitInDir(t, dir, "add", "feature.go")
	runGitInDir(t, dir, "commit", "-q", "-m", "feat(f): branch change")
	runGitInDir(t, dir, "checkout", "-q", "master")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base v2\n"), 0644); err != nil {
		t.Fatalf("could not modify base.txt: %v", err)
	}
	runGitInDir(t, dir, "commit", "-qam", "chore(m): master change")
	runGitInDir(t, dir, "merge", "-q", "--no-ff", "feat", "-m", "merge: two parents")
	// Verify HEAD really is a two-parent merge commit: if the repo setup
	// changes, the tests must fail here instead of misleading us.
	if parents := strings.Fields(runGitInDir(t, dir, "rev-list", "--parents", "-n", "1", "HEAD")); len(parents) != 3 {
		t.Fatalf("HEAD should be a two-parent merge commit, rev-list gave %d fields", len(parents))
	}
	return dir
}

func TestDiffCommitOnMergeReturnsFirstParentDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available on PATH")
	}

	dir := prepareRepoWithMerge(t)
	t.Chdir(dir)

	head, _ := SHAHead()

	diff, err := DiffCommit(head)
	if err != nil {
		t.Fatalf("DiffCommit returned an error: %v", err)
	}
	if !strings.Contains(diff, "feature.go") || !strings.Contains(diff, "+package feature") {
		t.Errorf("merge diff should show the merged-branch change (feature.go), got: %q", diff)
	}
	if strings.Contains(diff, "base v2") {
		t.Errorf("merge diff must not include the first parent's own change (base v2), got: %q", diff)
	}
}

func TestCommitFilesOnMergeListsBranchFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available on PATH")
	}

	dir := prepareRepoWithMerge(t)
	t.Chdir(dir)

	head, _ := SHAHead()

	files, err := FilesOfCommit(head)
	if err != nil {
		t.Fatalf("FilesOfCommit returned an error: %v", err)
	}
	if len(files) != 1 || files[0] != "feature.go" {
		t.Errorf("merge FilesOfCommit should list exactly feature.go, got: %+v", files)
	}
}
