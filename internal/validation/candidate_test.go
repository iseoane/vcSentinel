package validation

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/graph"
)

type errorGraphProvider struct{}

func (errorGraphProvider) Name() string { return "error" }
func (errorGraphProvider) Analyze([]string) (graph.AnalysisResult, error) {
	return graph.AnalysisResult{}, errors.New("graph unavailable")
}

// candidateTestRepo creates a real git repository (with an initial commit) in
// a temporary directory and moves the test process' cwd there: the internal/git
// functions (Freeze, TreeOf, CreateSnapshot...) run git against the process
// cwd, they do not receive an explicit path, so there is no other way to point
// them at a repo isolated per test.
func candidateTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("initial\n"), 0644); err != nil {
		t.Fatalf("could not write file.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "a"), 0755); err != nil {
		t.Fatalf("could not create the test package: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/candidate\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatalf("could not write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "a", "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatalf("could not write a.go: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "initial")

	t.Chdir(dir)
	return dir
}

// candidateTestProfileConfig returns a minimal config with a single profile
// ("profile") pointing at a single capability ("cap1"): enough for RunProfile
// not to fall into agent delegation (profile with no capabilities
// configured), which is not what T1.6 wants to test here.
func candidateTestProfileConfig(mode string) config.Config {
	var cfg config.Config
	cfg.Validation.Mode = mode
	cfg.Validation.Profiles = map[string][]string{"profile": {"cap1"}}
	cfg.Validation.Capabilities = map[string]config.CapabilityConfig{
		"cap1": {Command: "true", FailsWhen: config.FailsWhenExitCode},
	}
	return cfg
}

// countSnapshots counts the entries of the repo's snapshot directory in dir
// (0 if the directory does not exist yet): used to check that inplace mode
// never creates one.
func countSnapshots(t *testing.T, dir string) int {
	t.Helper()
	commonDir, err := git.GetGitCommonDir(dir)
	if err != nil {
		t.Fatalf("could not get the common-dir: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(commonDir, "vas-sentinel", "snapshots"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("could not read the snapshot directory: %v", err)
	}
	return len(entries)
}

func TestRunProfileOnCandidate_WorktreeUnchanged_RunsOnSnapshotAndStaysFresh(t *testing.T) {
	dir := candidateTestRepo(t)

	opts := RunOptions{Worktree: dir, Cfg: candidateTestProfileConfig(config.ModeWorktree)}
	var receivedCommands []string
	opts.Run = func(command string) (int, string, error) {
		receivedCommands = append(receivedCommands, command)
		return 0, "ok", nil
	}

	runs, err := RunProfileOnCandidate("profile", nil, opts)
	if err != nil {
		t.Fatalf("did not expect an error, got: %v", err)
	}
	if len(runs) != 1 || runs[0].Command != "true" {
		t.Fatalf("unexpected runs: %+v", runs)
	}
	if len(receivedCommands) != 1 {
		t.Fatalf("expected exactly 1 executed command, got %d", len(receivedCommands))
	}

	// The snapshot of HEAD's tree must exist after the call (CreateSnapshot is
	// idempotent: if it already exists, this second call just reuses it).
	tree, err := git.TreeOf("HEAD")
	if err != nil {
		t.Fatalf("could not resolve HEAD's tree: %v", err)
	}
	snapshot, err := git.CreateSnapshot(tree)
	if err != nil {
		t.Fatalf("could not verify the snapshot: %v", err)
	}
	info, err := os.Stat(snapshot)
	if err != nil || !info.IsDir() {
		t.Fatalf("expected snapshot %q to exist as a directory", snapshot)
	}
}

// TestRunProfileOnCandidate_DirtyWorktreeAtFreeze_SnapshotReflects reproduces
// the finding of the phase F1 review: if the worktree was ALREADY dirty before
// calling this function (not during execution, which is the case StillValid
// already covers), the snapshot must reflect that uncommitted content —the
// one Freeze() anchors via the stash-anchor—, not HEAD's clean tree. Before
// the fix, the snapshot was always created with TreeOf("HEAD") and this test
// would have seen "initial" instead of "dirty".
func TestRunProfileOnCandidate_DirtyWorktreeAtFreeze_SnapshotReflects(t *testing.T) {
	dir := candidateTestRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatalf("could not dirty the worktree: %v", err)
	}

	opts := RunOptions{Worktree: dir, Cfg: candidateTestProfileConfig(config.ModeWorktree)}
	opts.Run = func(command string) (int, string, error) { return 0, "ok", nil }

	if _, err := RunProfileOnCandidate("profile", nil, opts); err != nil {
		t.Fatalf("did not expect an error, got: %v", err)
	}

	// Inspect the snapshot the function under test REALLY created (not one
	// recomputed separately, which CreateSnapshot would generate all the same
	// no matter what the function did: that would prove nothing about its real
	// behavior). A freshly created test repo can only have one snapshot.
	commonDir, err := git.GetGitCommonDir(dir)
	if err != nil {
		t.Fatalf("could not get the common-dir: %v", err)
	}
	snapshotsDir := filepath.Join(commonDir, "vas-sentinel", "snapshots")
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		t.Fatalf("could not read the snapshot directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 snapshot, there are %d", len(entries))
	}
	content, err := os.ReadFile(filepath.Join(snapshotsDir, entries[0].Name(), "file.txt"))
	if err != nil {
		t.Fatalf("could not read file.txt from the real snapshot: %v", err)
	}
	if string(content) != "dirty\n" {
		t.Fatalf("the real snapshot contains %q, expected the dirty content of the frozen candidate, not HEAD's", content)
	}
}

func TestRunProfileOnCandidate_WorktreeModifiedDuringRun_GoesStale(t *testing.T) {
	dir := candidateTestRepo(t)

	opts := RunOptions{Worktree: dir, Cfg: candidateTestProfileConfig(config.ModeWorktree)}
	opts.Run = func(command string) (int, string, error) {
		// Simulates that, while the validation runs, something modifies the
		// real worktree (e.g. a concurrent agent): the frozen tree stops
		// matching the current one.
		if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("modified\n"), 0644); err != nil {
			t.Fatalf("could not modify file.txt: %v", err)
		}
		return 0, "ok", nil
	}

	runs, err := RunProfileOnCandidate("profile", nil, opts)
	if !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("expected ErrStaleCandidate, got err=%v", err)
	}
	if runs != nil {
		t.Fatalf("expected nil runs when the candidate goes stale, got %+v", runs)
	}
}

func TestRunProfileOnCandidate_InplaceWithDirtyWorktree_AbortsWithoutRunning(t *testing.T) {
	dir := candidateTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatalf("could not dirty the worktree: %v", err)
	}

	opts := RunOptions{Worktree: dir, Cfg: candidateTestProfileConfig(config.ModeInplace)}
	opts.Run = func(command string) (int, string, error) {
		t.Fatalf("no command should run in inplace mode with a dirty worktree")
		return 0, "", nil
	}

	runs, err := RunProfileOnCandidate("profile", nil, opts)
	if err == nil {
		t.Fatal("expected an error for a dirty worktree in inplace mode")
	}
	if runs != nil {
		t.Fatalf("expected nil runs when aborting, got %+v", runs)
	}
}

func TestRunProfileOnCandidate_InplaceWithCleanWorktree_RunsOnRealWorktreeWithoutSnapshot(t *testing.T) {
	dir := candidateTestRepo(t)
	before := countSnapshots(t, dir)

	opts := RunOptions{Worktree: dir, Cfg: candidateTestProfileConfig(config.ModeInplace)}
	var receivedCommands []string
	opts.Run = func(command string) (int, string, error) {
		receivedCommands = append(receivedCommands, command)
		return 0, "ok", nil
	}

	runs, err := RunProfileOnCandidate("profile", nil, opts)
	if err != nil {
		t.Fatalf("did not expect an error, got: %v", err)
	}
	if len(runs) != 1 || runs[0].Command != "true" {
		t.Fatalf("unexpected runs: %+v", runs)
	}
	if len(receivedCommands) != 1 {
		t.Fatalf("expected exactly 1 executed command, got %d", len(receivedCommands))
	}

	after := countSnapshots(t, dir)
	if after != before {
		t.Fatalf("inplace mode must not create any snapshot: before=%d after=%d", before, after)
	}
}

func TestRunProfileOnCandidate_ScopeOnlyWithCompleteGraph(t *testing.T) {
	dir := candidateTestRepo(t)
	cfg := candidateTestProfileConfig(config.ModeWorktree)
	cfg.Validation.Capabilities["cap1"] = config.CapabilityConfig{
		Command:       "go test ./...",
		SupportsScope: true,
		ScopedCommand: "go test {packages}",
	}

	cases := []struct {
		name, path, command, scope, scoped string
		graphError                         bool
	}{
		{"incomplete runs the exact full command", "file.txt", "go test ./...", ScopeFull, "go test {packages}", false},
		{"complete substitutes the authorized package", "internal/a/a.go", "go test example.com/candidate/internal/a", ScopePartial, "go test {packages}", false},
		{"invalid scoped_command runs the exact full command", "internal/a/a.go", "go test ./...", ScopeFull, "go test ./internal/...", false},
		{"error runs the exact full command", "internal/a/a.go", "go test ./...", ScopeFull, "go test {packages}", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseCfg := cfg
			capability := cfg.Validation.Capabilities["cap1"]
			capability.ScopedCommand = tc.scoped
			caseCfg.Validation.Capabilities = map[string]config.CapabilityConfig{"cap1": capability}
			var executed string
			runs, err := RunProfileOnCandidate("profile", []string{tc.path}, RunOptions{
				Worktree: dir,
				Cfg:      caseCfg,
				ProviderGraph: func(snapshot, treeOID string) graph.GraphProvider {
					if snapshot == dir || treeOID == "" {
						t.Fatalf("the graph received the live worktree or an empty tree OID: snapshot=%q tree=%q", snapshot, treeOID)
					}
					if tc.graphError {
						return errorGraphProvider{}
					}
					return graph.NewNativeProvider(snapshot, treeOID)
				},
				Run: func(command string) (int, string, error) {
					executed = command
					return 0, "", nil
				},
			})
			if err != nil {
				t.Fatalf("RunProfileOnCandidate failed: %v", err)
			}
			if executed != tc.command || runs[0].Command != tc.command {
				t.Fatalf("command = %q, run = %q; expected exact %q", executed, runs[0].Command, tc.command)
			}
			if runs[0].Scope != tc.scope || runs[0].ScopeReason == "" {
				t.Fatalf("scope decision not explained: %+v", runs[0])
			}
		})
	}
}

// TestRunProfileOnCandidate_WithoutGraphProviderUsesFullCommand is F4's exit
// criterion "without a GraphProvider (nil), the system works the same and
// validates full": even if the capability declares supports_scope and there
// is a real changed file, with no ProviderGraph configured (nil) the graph is
// never analyzed, so the scope authorization stays at its zero value (not
// authorized) and the executed command is the exact full one, never the
// scoped one.
func TestRunProfileOnCandidate_WithoutGraphProviderUsesFullCommand(t *testing.T) {
	dir := candidateTestRepo(t)
	cfg := candidateTestProfileConfig(config.ModeWorktree)
	cfg.Validation.Capabilities["cap1"] = config.CapabilityConfig{
		Command:       "go test ./...",
		SupportsScope: true,
		ScopedCommand: "go test {packages}",
	}

	var executed string
	runs, err := RunProfileOnCandidate("profile", []string{"internal/a/a.go"}, RunOptions{
		Worktree:      dir,
		Cfg:           cfg,
		ProviderGraph: nil,
		Run: func(command string) (int, string, error) {
			executed = command
			return 0, "", nil
		},
	})
	if err != nil {
		t.Fatalf("RunProfileOnCandidate failed: %v", err)
	}
	if executed != "go test ./..." || runs[0].Command != "go test ./..." {
		t.Fatalf("command = %q, run = %q; expected the exact full one without GraphProvider", executed, runs[0].Command)
	}
	if runs[0].Scope != ScopeFull || runs[0].ScopeReason == "" {
		t.Fatalf("scope decision not explained: %+v", runs[0])
	}
}

// TestRunProfileOnCandidate_SweepsOldSnapshots closes the cleanup loop: every
// flow that creates a snapshot sweeps the ones past retention, without
// touching the reusable fresh ones.
func TestRunProfileOnCandidate_SweepsOldSnapshots(t *testing.T) {
	dir := candidateTestRepo(t)

	headTree, err := git.TreeOf("HEAD")
	if err != nil {
		t.Fatalf("could not resolve HEAD's tree: %v", err)
	}
	emptyTree := exec.Command("git", "mktree")
	emptyTree.Dir = dir
	emptyTree.Stdin = strings.NewReader("")
	output, err := emptyTree.Output()
	if err != nil {
		t.Fatalf("could not materialize the empty tree: %v", err)
	}
	agedSnapshot, err := git.CreateSnapshot(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("could not create the aged snapshot: %v", err)
	}
	past := time.Now().Add(-(git.SnapshotRetention + time.Hour))
	if err := os.Chtimes(agedSnapshot, past, past); err != nil {
		t.Fatalf("could not age the snapshot: %v", err)
	}
	activeSnapshot, err := git.CreateSnapshot(headTree)
	if err != nil {
		t.Fatalf("could not create the active snapshot: %v", err)
	}
	if err := os.Chtimes(activeSnapshot, past, past); err != nil {
		t.Fatalf("could not age the active snapshot: %v", err)
	}

	opts := RunOptions{Worktree: dir, Cfg: candidateTestProfileConfig(config.ModeWorktree)}
	opts.Run = func(string) (int, string, error) { return 0, "ok", nil }
	if _, err := RunProfileOnCandidate("profile", nil, opts); err != nil {
		t.Fatalf("did not expect an error, got: %v", err)
	}

	commonDir, err := git.GetGitCommonDir(dir)
	if err != nil {
		t.Fatalf("could not get the common-dir: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(commonDir, "vas-sentinel", "snapshots"))
	if err != nil {
		t.Fatalf("could not read the snapshot directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != headTree {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("after the sweep %v remained, expected only the fresh snapshot %s", names, headTree)
	}
	info, err := os.Stat(activeSnapshot)
	if err != nil {
		t.Fatalf("the reused snapshot did not survive: %v", err)
	}
	if !info.ModTime().After(past) {
		t.Fatalf("the reused snapshot was not refreshed: %s", info.ModTime())
	}
}
