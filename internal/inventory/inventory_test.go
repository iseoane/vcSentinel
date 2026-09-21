package inventory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// runGit runs git in dir and fails the test on any error (cmd/vcsentinel style).
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// initRepo creates a throwaway repository with a committed base file on main.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@vcsentinel")
	runGit(t, dir, "config", "user.name", "vcSentinel Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "chore: base")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectSingleWorktree(t *testing.T) {
	repo := initRepo(t)
	runGit(t, repo, "remote", "add", "origin", "https://example.test/repo.git")

	snapshot, err := Inspect(repo)
	if err != nil {
		t.Fatalf("Inspect(%q): %v", repo, err)
	}
	want := Snapshot{
		Repository: filepath.Base(repo),
		Origin:     "https://example.test/repo.git",
		Worktrees:  []Worktree{{Path: repo, Branch: "main", Clean: true}},
	}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot mismatch:\n got: %#v\nwant: %#v", snapshot, want)
	}
}

func TestInspectTwoLinkedWorktreesSortedByPath(t *testing.T) {
	repo := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", linked, "-b", "feature/linked")

	snapshot, err := Inspect(repo)
	if err != nil {
		t.Fatalf("Inspect(%q): %v", repo, err)
	}
	want := []Worktree{
		{Path: repo, Branch: "main", Clean: true},
		{Path: linked, Branch: "feature/linked", Clean: true},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
	if !reflect.DeepEqual(snapshot.Worktrees, want) {
		t.Fatalf("worktrees mismatch:\n got: %#v\nwant: %#v", snapshot.Worktrees, want)
	}
}

func TestInspectDetachedHeadWorktree(t *testing.T) {
	repo := initRepo(t)
	detached := filepath.Join(t.TempDir(), "detached")
	runGit(t, repo, "worktree", "add", "--detach", detached)

	snapshot, err := Inspect(repo)
	if err != nil {
		t.Fatalf("Inspect(%q): %v", repo, err)
	}
	byPath := map[string]Worktree{}
	for _, wt := range snapshot.Worktrees {
		byPath[wt.Path] = wt
	}
	det := byPath[detached]
	if !det.Detached || det.Branch != "" || !det.Clean {
		t.Fatalf("missing or wrong detached entry: %#v (all: %#v)", det, snapshot.Worktrees)
	}
	main := byPath[repo]
	if main.Detached || main.Branch != "main" || !main.Clean {
		t.Fatalf("main entry wrong: %#v", main)
	}
}

func TestInspectDetectsDirtyWorktree(t *testing.T) {
	repo := initRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repo, "worktree", "add", linked, "-b", "feature/dirty")
	writeFile(t, filepath.Join(linked, "dirty.txt"), "pending change\n")

	snapshot, err := Inspect(repo)
	if err != nil {
		t.Fatalf("Inspect(%q): %v", repo, err)
	}
	cleanFlags := map[string]bool{}
	for _, wt := range snapshot.Worktrees {
		cleanFlags[wt.Path] = wt.Clean
	}
	if !cleanFlags[repo] || cleanFlags[linked] {
		t.Fatalf("expected main clean and linked dirty: %#v", snapshot.Worktrees)
	}
}

func TestInspectMissingOriginIsEmptyNotError(t *testing.T) {
	repo := initRepo(t)

	snapshot, err := Inspect(repo)
	if err != nil || snapshot.Origin != "" {
		t.Fatalf("expected success with empty Origin, got err=%v origin=%q", err, snapshot.Origin)
	}
}

func TestInspectRejectsNonRepositoryPath(t *testing.T) {
	plain := t.TempDir()

	if snapshot, err := Inspect(plain); err == nil || !strings.Contains(err.Error(), "rev-parse") {
		t.Fatalf("expected rev-parse failure for %q, got %#v / %v", plain, snapshot, err)
	}
}

// stubResponse is one canned runner reply; stubRunner fails loudly on
// commands it has no canned response for.
type stubResponse struct {
	out []byte
	err error
}

type stubRunner struct {
	commands map[string]stubResponse
}

func (s stubRunner) run(dir string, args ...string) ([]byte, error) {
	resp, ok := s.commands[strings.Join(args, " ")]
	if !ok {
		return nil, fmt.Errorf("stubRunner: unexpected command: git %s", strings.Join(args, " "))
	}
	return resp.out, resp.err
}

func newStub(worktreeList string) stubRunner {
	return stubRunner{commands: map[string]stubResponse{
		"rev-parse --git-dir":            {},
		"config --get remote.origin.url": {},
		"worktree list --porcelain":      {out: []byte(worktreeList)},
		"status --porcelain":             {}, // cleanliness probe for parsed entries
	}}
}

func TestInspectStrictPorcelainUnknownTokenFails(t *testing.T) {
	list := fmt.Sprintf("worktree /tmp/repo\nHEAD %s\nfrobnicated yes\n", "0123456789abcdef0123456789abcdef01234567")

	snapshot, err := inspect(filepath.Join(string(filepath.Separator), "tmp", "repo"), newStub(list))
	if err == nil {
		t.Fatalf("expected the unknown token to fail Inspect, got %#v", snapshot)
	}
	if !strings.Contains(err.Error(), "frobnicated") || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error should report the unknown token as malformed, got: %v", err)
	}
}

const stubSha1Hash = "0123456789abcdef0123456789abcdef01234567"

func TestInspectAcceptsSha256HeadHash(t *testing.T) {
	sha256Hash := stubSha1Hash + "0123456789abcdef01234567" // 64 hex characters
	list := fmt.Sprintf("worktree /tmp/repo\nHEAD %s\nbranch refs/heads/main\n", sha256Hash)

	snapshot, err := inspect(filepath.Join(string(filepath.Separator), "tmp", "repo"), newStub(list))
	if err != nil {
		t.Fatalf("a sha256 (64-hex) HEAD hash must be accepted: %v", err)
	}
	wantPath, err := normalizeWorktreePath("/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{
		Repository: "repo",
		Worktrees:  []Worktree{{Path: wantPath, Branch: "main", Clean: true}},
	}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot mismatch:\n got: %#v\nwant: %#v", snapshot, want)
	}
}

func TestInspectAcceptsLockedWithReason(t *testing.T) {
	list := fmt.Sprintf("worktree /tmp/repo\nlocked admin requested prune\nHEAD %s\nbranch refs/heads/main\n", stubSha1Hash)

	snapshot, err := inspect(filepath.Join(string(filepath.Separator), "tmp", "repo"), newStub(list))
	if err != nil {
		t.Fatalf("`locked <reason>` must be accepted: %v", err)
	}
	if len(snapshot.Worktrees) != 1 || snapshot.Worktrees[0].Branch != "main" || !snapshot.Worktrees[0].Clean {
		t.Fatalf("unexpected worktrees from locked block: %#v", snapshot.Worktrees)
	}
}

func TestInspectStrictPorcelainRejections(t *testing.T) {
	tests := []struct {
		name    string
		list    string
		wantErr []string
	}{
		{
			name:    "empty porcelain output",
			list:    "",
			wantErr: []string{"malformed", "zero worktree"},
		},
		{
			name:    "block without any HEAD token",
			list:    "worktree /tmp/repo\nbranch refs/heads/main\n",
			wantErr: []string{"malformed", "no HEAD token"},
		},
		{
			name:    "duplicated HEAD token",
			list:    fmt.Sprintf("worktree /tmp/repo\nHEAD %s\nHEAD %s\nbranch refs/heads/main\n", stubSha1Hash, stubSha1Hash),
			wantErr: []string{"malformed", "unexpected HEAD token"},
		},
		{
			name:    "branch reference without refs/heads prefix",
			list:    fmt.Sprintf("worktree /tmp/repo\nHEAD %s\nbranch refs/tags/v1\n", stubSha1Hash),
			wantErr: []string{"malformed", "refs/heads/"},
		},
		{
			name:    "41-character head hash",
			list:    fmt.Sprintf("worktree /tmp/repo\nHEAD %s0\nbranch refs/heads/main\n", stubSha1Hash),
			wantErr: []string{"malformed", "40 or 64 hexadecimal"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := inspect(filepath.Join(string(filepath.Separator), "tmp", "repo"), newStub(tt.list))
			if err == nil {
				t.Fatalf("expected a malformed-input error, got %#v", snapshot)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error should mention %q, got: %v", want, err)
				}
			}
		})
	}
}
