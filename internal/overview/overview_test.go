package overview

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/inventory"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
	"github.com/ISeoane-Quental/vas.sentinel/internal/registry"
)

// runGit runs git in dir and fails the test on any error (inventory style).
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

// initRepo creates a throwaway repository with a committed base file on main.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@vas.sentinel")
	runGit(t, dir, "config", "user.name", "VAS Sentinel Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "base.txt"), "base\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "chore: base")
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// writeRegistry persists entries in the exact document shape internal/registry
// reads (version 1, slash-separated absolute paths).
func writeRegistry(t *testing.T, path string, entries ...registry.Entry) {
	t.Helper()
	slashEntries := make([]registry.Entry, len(entries))
	for i, entry := range entries {
		entry.Path = filepath.ToSlash(entry.Path)
		slashEntries[i] = entry
	}
	data, err := json.MarshalIndent(struct {
		Version      int              `json:"version"`
		Repositories []registry.Entry `json:"repositories"`
	}{Version: 1, Repositories: slashEntries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(data)+"\n")
}

func registryPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "repositories.json")
}

func stopped() presence.Presence { return presence.Presence{} }

func TestCollectFullyPopulatesHealthyRepository(t *testing.T) {
	repo := initRepo(t)
	const origin = "https://example.test/repo.git"
	runGit(t, repo, "remote", "add", "origin", origin)

	path := registryPath(t)
	writeRegistry(t, path, registry.Entry{Path: repo, Name: filepath.Base(repo), Enabled: true})

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []Repo{{
		Path:      repo,
		Name:      filepath.Base(repo),
		Enabled:   true,
		Worktrees: []inventory.Worktree{{Path: repo, Branch: "main", Clean: true}},
		Origin:    origin,
		Daemon:    stopped(),
	}}
	if !reflect.DeepEqual(repos, want) {
		t.Fatalf("repos mismatch:\n got: %#v\nwant: %#v", repos, want)
	}
}

func TestCollectFlagsMissingDirectoryWithoutProbes(t *testing.T) {
	repo := t.TempDir()
	path := registryPath(t)
	writeRegistry(t, path, registry.Entry{Path: repo, Name: "gone", Enabled: true})
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []Repo{{Path: repo, Name: "gone", Enabled: true, Missing: true}}
	if !reflect.DeepEqual(repos, want) {
		t.Fatalf("repos mismatch:\n got: %#v\nwant: %#v", repos, want)
	}
}

func TestCollectPlainDirectoryRecordsErrorWithoutFailingView(t *testing.T) {
	healthy := initRepo(t)
	plain := t.TempDir() // exists but is not a git repository
	path := registryPath(t)
	writeRegistry(t, path,
		registry.Entry{Path: plain, Name: "plain", Enabled: true},
		registry.Entry{Path: healthy, Name: filepath.Base(healthy), Enabled: true},
	)

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("one broken repository must not fail Collect: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("Collect returned %d repos, want 2: %#v", len(repos), repos)
	}
	var plainRepo, healthyRepo Repo
	for _, repo := range repos {
		switch repo.Path {
		case plain:
			plainRepo = repo
		case healthy:
			healthyRepo = repo
		}
	}
	if plainRepo.Error == "" {
		t.Fatalf("plain directory must record Error, got %#v", plainRepo)
	}
	if plainRepo.Daemon != stopped() || plainRepo.Worktrees != nil || plainRepo.Origin != "" || plainRepo.Missing {
		t.Fatalf("plain directory must keep zero-value probes: %#v", plainRepo)
	}
	if healthyRepo.Error != "" || healthyRepo.Origin != "" || len(healthyRepo.Worktrees) != 1 {
		t.Fatalf("healthy sibling must stay fully populated: %#v", healthyRepo)
	}
}

func TestCollectPreservesRegistryOrderAndFlags(t *testing.T) {
	// Registration order is deliberately shuffled; the registry sorts by Path.
	first := initRepo(t)
	second := t.TempDir() // plain directory: probes fail but the row remains
	third := initRepo(t)
	gone := t.TempDir()
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	path := registryPath(t)
	writeRegistry(t, path,
		registry.Entry{Path: third, Name: "c-third", Enabled: true},
		registry.Entry{Path: gone, Name: "d-gone", Enabled: false},
		registry.Entry{Path: first, Name: "a-first", Enabled: true},
		registry.Entry{Path: second, Name: "b-second", Enabled: false},
	)

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	wantOrder := []string{first, second, third, gone}
	gotOrder := make([]string, 0, len(repos))
	for _, repo := range repos {
		gotOrder = append(gotOrder, repo.Path)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("order mismatch:\n got: %v\nwant: %v", gotOrder, wantOrder)
	}
	byPath := map[string]Repo{}
	for _, repo := range repos {
		byPath[repo.Path] = repo
	}
	if repo := byPath[gone]; !repo.Missing || repo.Enabled {
		t.Fatalf("gone entry must be Missing and keep its disabled flag: %#v", repo)
	}
	if repo := byPath[second]; repo.Enabled || repo.Error == "" {
		t.Fatalf("plain disabled entry stays included with its Error: %#v", repo)
	}
	for _, healthy := range []string{first, third} {
		if repo := byPath[healthy]; !repo.Enabled || repo.Error != "" || len(repo.Worktrees) != 1 {
			t.Fatalf("healthy entry wrong: %#v", repo)
		}
	}
}

func TestCollectCorruptRegistryFailsExplicitly(t *testing.T) {
	t.Run("corrupt JSON content", func(t *testing.T) {
		path := registryPath(t)
		writeFile(t, path, "{not json")
		repos, err := Collect(path)
		if err == nil {
			t.Fatalf("corrupt registry must return an explicit error, got repos %#v", repos)
		}
		if repos != nil {
			t.Fatalf("failed open must yield a nil slice, got %#v", repos)
		}
	})
	t.Run("registry path is a directory", func(t *testing.T) {
		repos, err := Collect(t.TempDir())
		if err == nil {
			t.Fatalf("directory registry must return an explicit error, got repos %#v", repos)
		}
		if repos != nil {
			t.Fatalf("failed open must yield a nil slice, got %#v", repos)
		}
	})
}

func TestCollectMissingRegistryYieldsEmptySnapshot(t *testing.T) {
	repos, err := Collect(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("absent registry is an empty overview, not a failure: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("absent registry must yield no rows, got %#v", repos)
	}
}

// stubRecentRuns swaps the package seam for stub and restores it on cleanup;
// the returned slice records every directory the seam was asked about.
func stubRecentRuns(t *testing.T, stub func(dir string, limit int) ([]presence.RunSummary, error)) *[]string {
	t.Helper()
	calls := &[]string{}
	previous := recentRuns
	recentRuns = func(dir string, limit int) ([]presence.RunSummary, error) {
		*calls = append(*calls, dir)
		return stub(dir, limit)
	}
	t.Cleanup(func() { recentRuns = previous })
	return calls
}

func TestCollectStoresRecentRunsUpToLimit(t *testing.T) {
	repo := initRepo(t)
	path := registryPath(t)
	writeRegistry(t, path, registry.Entry{Path: repo, Name: "runs", Enabled: true})
	want := []presence.RunSummary{
		{RunID: "aaaaaaaaaaaaaaaaaaaa", State: agentrun.StateRunning, Revision: 4},
		{RunID: "bbbbbbbbbbbbbbbbbbbb", State: agentrun.StateSucceeded, Revision: 5},
	}
	var gotLimit int
	stubRecentRuns(t, func(dir string, limit int) ([]presence.RunSummary, error) {
		gotLimit = limit
		if dir != filepath.Join(repo, ".git") {
			t.Errorf("runs read anchored at %q, want the resolved common dir", dir)
		}
		return want, nil
	})

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("Collect returned %d repos, want 1: %#v", len(repos), repos)
	}
	if !reflect.DeepEqual(repos[0].Runs, want) {
		t.Fatalf("runs mismatch:\n got: %#v\nwant: %#v", repos[0].Runs, want)
	}
	if gotLimit != recentRunLimit {
		t.Fatalf("runs read used limit %d, want the package const %d", gotLimit, recentRunLimit)
	}
	if repos[0].Error != "" || len(repos[0].Worktrees) != 1 || repos[0].Daemon != stopped() {
		t.Fatalf("healthy probes must stay intact beside stored runs: %#v", repos[0])
	}
}

func TestCollectRunsFailureRecordsErrorWithoutFailingView(t *testing.T) {
	repo := initRepo(t)
	path := registryPath(t)
	writeRegistry(t, path, registry.Entry{Path: repo, Name: "runs", Enabled: true})
	stubRecentRuns(t, func(string, int) ([]presence.RunSummary, error) {
		return nil, errors.New("store exploded")
	})

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("a runs-read failure must never fail Collect: %v", err)
	}
	repo0 := repos[0]
	if !strings.Contains(repo0.Error, "read recent runs") || !strings.Contains(repo0.Error, "store exploded") {
		t.Fatalf("runs failure must record its cause in Error, got %q", repo0.Error)
	}
	if repo0.Runs != nil {
		t.Fatalf("failed read must leave Runs nil, got %#v", repo0.Runs)
	}
	if len(repo0.Worktrees) != 1 || repo0.Daemon != stopped() {
		t.Fatalf("independent probes must keep their results: %#v", repo0)
	}
}

// breakWorktreeInventory adds a linked worktree and deletes its directory, so
// the strict inventory parser rejects the stale entry while the git common
// dir still resolves: exactly the double-failure shape needed to pin that a
// later runs-read failure cannot steal Repo.Error.
func breakWorktreeInventory(t *testing.T, repo string) {
	t.Helper()
	wt := filepath.Join(t.TempDir(), "doomed")
	runGit(t, repo, "worktree", "add", wt, "-b", "doomed")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
}

func TestCollectPreservesFirstObservedErrorWhenRunsReadAlsoFails(t *testing.T) {
	repo := initRepo(t)
	breakWorktreeInventory(t, repo)
	path := registryPath(t)
	writeRegistry(t, path, registry.Entry{Path: repo, Name: "broken", Enabled: true})
	seen := false
	stubRecentRuns(t, func(string, int) ([]presence.RunSummary, error) {
		seen = true
		return nil, errors.New("store exploded")
	})

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !seen {
		t.Fatal("the runs read must still execute after an inventory failure")
	}
	if !strings.Contains(repos[0].Error, "inspect repository") {
		t.Fatalf("first observed cause must win, got %q", repos[0].Error)
	}
	if strings.Contains(repos[0].Error, "read recent runs") {
		t.Fatalf("the later runs cause must not overwrite the original error: %q", repos[0].Error)
	}
	if repos[0].Runs != nil {
		t.Fatalf("failed read must leave Runs nil, got %#v", repos[0].Runs)
	}
}

func TestCollectStoresSuccessfulRunRowsBesideOriginalError(t *testing.T) {
	repo := initRepo(t)
	breakWorktreeInventory(t, repo)
	path := registryPath(t)
	writeRegistry(t, path, registry.Entry{Path: repo, Name: "broken", Enabled: true})
	want := []presence.RunSummary{
		{RunID: "cccccccccccccccccccc", State: agentrun.StateCanceled, Revision: 9},
	}
	stubRecentRuns(t, func(string, int) ([]presence.RunSummary, error) {
		return want, nil
	})

	repos, err := Collect(path)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !strings.Contains(repos[0].Error, "inspect repository") {
		t.Fatalf("original degradation cause must survive, got %q", repos[0].Error)
	}
	if !reflect.DeepEqual(repos[0].Runs, want) {
		t.Fatalf("successful runs must be stored even beside an earlier error:\n got: %#v\nwant: %#v",
			repos[0].Runs, want)
	}
}
