package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/registry"
	"github.com/ISeoane-Quental/vcSentinel/internal/setup"
)

func isolateRepositoryRegistry(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repositories.json")
	previous := resolveRepositoryRegistryPath
	resolveRepositoryRegistryPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { resolveRepositoryRegistryPath = previous })
	return path
}

func repositoryCount(t *testing.T, path string) int {
	t.Helper()
	store, err := registry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(store.List())
}

// prepareInitRepository creates a throwaway repository with the DEFAULT hooks
// layout (no core.hooksPath override), so init's hook lands where plain Git
// reads it. Unlike prepareStagedCheckRepository, hooks stay enabled.
func prepareInitRepository(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	runGit(t, "init", "-q", "-b", "main")
	runGit(t, "config", "user.email", "test@vcsentinel")
	runGit(t, "config", "user.name", "vcSentinel Test")
	runGit(t, "config", "commit.gpgsign", "false")
	if err := os.WriteFile("base.txt", []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "base.txt")
	runGit(t, "commit", "-q", "-m", "chore: base")
	return runGit(t, "rev-parse", "--show-toplevel")
}

// TestInitInstallsHookInCommonDirWithMarkedRule drives the real init flow over
// a temporary repository and proves the full artifact set: the pre-commit hook
// is written into <git-common-dir>/hooks with the exact content the generator
// produces (shebang, absolute quoted binary path, check --staged), it is
// executable on Linux, the marked guardian rule block lands in the agent
// instruction files, and the per-project configuration is created.
func TestInitInstallsHookInCommonDirWithMarkedRule(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)

	runInit(root)

	commonDir, err := git.GetGitCommonDir(root)
	if err != nil {
		t.Fatalf("could not resolve the common-dir: %v", err)
	}
	hookPath := filepath.Join(commonDir, "hooks", "pre-commit")
	hookBytes, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("init did not install the hook at %s: %v", hookPath, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("could not locate the test executable: %v", err)
	}
	expected := generateHookScriptFor(exe)
	if string(hookBytes) != expected {
		t.Errorf("unexpected hook content:\n%q\nexpected:\n%q", hookBytes, expected)
	}
	if !strings.HasPrefix(string(hookBytes), "#!/bin/sh\n") {
		t.Errorf("the hook does not start with the #!/bin/sh shebang: %q", hookBytes)
	}
	if !strings.Contains(string(hookBytes), "check --staged") {
		t.Errorf("the hook does not invoke check --staged: %q", hookBytes)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0755 {
			t.Errorf("hook permissions = %o, expected 0755 (executable)", perm)
		}
	}

	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("init did not create %s: %v", name, err)
		}
		content := string(data)
		if !strings.Contains(content, markerBegin) || !strings.Contains(content, markerEnd) {
			t.Errorf("%s does not contain the marked volume rules block", name)
		}
		if !strings.Contains(content, "CRITICAL VOLUME RULE") {
			t.Errorf("%s does not contain the guardian rule", name)
		}
	}

	if !setup.IsInitialized(root) {
		t.Error("init did not leave the per-project configuration (.vas_sentinel/vassentinel.yml)")
	}
	if count := repositoryCount(t, registryPath); count != 1 {
		t.Fatalf("init registered %d repositories, want 1", count)
	}

	t.Run("is idempotent", func(t *testing.T) {
		hookBefore, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		runInit(root)
		hookAfter, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(hookBefore) != string(hookAfter) {
			t.Error("a second init rewrote the hook")
		}
		agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(agents), markerBegin); n != 1 {
			t.Errorf("after repeating init there are %d rule blocks, expected 1", n)
		}
		if count := repositoryCount(t, registryPath); count != 1 {
			t.Errorf("repeated init registered %d repositories, want 1", count)
		}
	})
}

// TestUninitRevertsHookConfigAndRules proves uninit reverses exactly what init
// installed: hook removed from the common dir, per-project configuration
// removed, rule blocks retired from the agent files while their own content
// survives.
func TestUninitRevertsHookConfigAndRules(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	ownContent := "# My project\n\nOwn documentation.\n"
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(ownContent), 0644); err != nil {
		t.Fatal(err)
	}

	runInit(root)

	commonDir, err := git.GetGitCommonDir(root)
	if err != nil {
		t.Fatalf("could not resolve the common-dir: %v", err)
	}
	hookPath := filepath.Join(commonDir, "hooks", "pre-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("the hook was not installed after init: %v", err)
	}

	runUninit(root)

	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("uninit did not remove the installed hook: %v", err)
	}
	if setup.IsInitialized(root) {
		t.Error("uninit did not remove the per-project configuration")
	}
	if count := repositoryCount(t, registryPath); count != 0 {
		t.Errorf("uninit left %d repositories registered", count)
	}
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, markerBegin) || strings.Contains(content, "CRITICAL VOLUME RULE") {
		t.Errorf("uninit left leftovers of the rules block: %q", content)
	}
	if !strings.Contains(content, "Own documentation.") {
		t.Error("uninit lost the file's own content")
	}
}

// TestUninitPreservesForeignHook: if another tool replaced the hook after init,
// uninit must leave it alone instead of deleting what it did not install.
func TestUninitPreservesForeignHook(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	runInit(root)

	commonDir, err := git.GetGitCommonDir(root)
	if err != nil {
		t.Fatalf("could not resolve the common-dir: %v", err)
	}
	hookPath := filepath.Join(commonDir, "hooks", "pre-commit")
	foreign := "#!/bin/sh\necho other-tool\n"
	if err := os.WriteFile(hookPath, []byte(foreign), 0755); err != nil {
		t.Fatal(err)
	}

	runUninit(root)

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("uninit removed a hook that no longer belonged to it: %v", err)
	}
	if string(data) != foreign {
		t.Errorf("uninit modified the foreign hook: %q", data)
	}
	if count := repositoryCount(t, registryPath); count != 0 {
		t.Errorf("foreign hook prevented registry removal: %d repositories remain", count)
	}
}

func TestUninitKeepsRegistryWhenCleanupFails(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	runInit(root)
	configPath := filepath.Join(root, ".vas_sentinel", "vassentinel.yml")
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configPath, "blocked"), 0755); err != nil {
		t.Fatal(err)
	}

	runUninit(root)
	if count := repositoryCount(t, registryPath); count != 1 {
		t.Fatalf("failed uninit left %d registry entries, want 1", count)
	}
}

func TestInitContinuesWhenRepositoryRegistryUpdateFails(t *testing.T) {
	root := prepareInitRepository(t)
	parentFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := resolveRepositoryRegistryPath
	resolveRepositoryRegistryPath = func() (string, error) {
		return filepath.Join(parentFile, "repositories.json"), nil
	}
	t.Cleanup(func() { resolveRepositoryRegistryPath = previous })
	runInit(root)
	if !setup.IsInitialized(root) {
		t.Fatal("init failed because the additive registry update failed")
	}
}

// writeFakeVCSentinel writes a sh-script vcsentinel executable that mimics the
// staged check contract: exit 1 (with a recognizable message) when overBudget,
// exit 0 otherwise. Only used from Linux-gated tests; on Windows the harness
// coverage for the same contract is compile-time plus the existing .cmd
// fixtures in internal/setup.
func writeFakeVCSentinel(t *testing.T, path string, overBudget bool) {
	t.Helper()
	script := "#!/bin/sh\necho 'STAGED VOLUME REJECTED BY FAKE VCSENTINEL'\nexit 1\n"
	if !overBudget {
		script = "#!/bin/sh\nexit 0\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

// installHookFor installs the production-generated hook script into the
// repository common dir, pointing at the given vcsentinel executable — exactly
// what init does, with the fake binary standing in for the real one.
func installHookFor(t *testing.T, root string, exeVCSentinel string) string {
	t.Helper()
	commonDir, err := git.GetGitCommonDir(root)
	if err != nil {
		t.Fatalf("could not resolve the common-dir: %v", err)
	}
	hooksDir := filepath.Join(commonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(generateHookScriptFor(exeVCSentinel)), 0755); err != nil {
		t.Fatal(err)
	}
	// core.hooksPath must point at the DIRECTORY containing the hooks,
	// not at the hook file itself.
	return filepath.ToSlash(hooksDir)
}

// commitWithHooks runs a real `git commit` with core.hooksPath resolving to
// the repository's own installed hooks directory (overriding any ambient Git
// configuration), so the hook under test is precisely the one init installed.
func commitWithHooks(t *testing.T, root string, hooksPathArg string, message string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("git", "-c", "core.hooksPath="+hooksPathArg, "commit", "-m", message)
	cmd.Dir = root
	return cmd.CombinedOutput()
}

// TestPreCommitHookEnforcesStagedVolumeThroughRealGit is the end-to-end
// enforcement proof: the hook that init installs must BLOCK a real git commit
// when the vcsentinel binary rejects the staged candidate (exit 1) and ALLOW it
// once the binary accepts (exit 0). Linux-only: it drives sh + git directly.
func TestPreCommitHookEnforcesStagedVolumeThroughRealGit(t *testing.T) {
	if testing.Short() {
		t.Skip("skips real git commit integration in short mode")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake vcsentinel fixture is a sh script; Windows coverage is compile-time here")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available in PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	root := prepareInitRepository(t)
	fakeVCSentinel := filepath.Join(root, "fakebin", "vcsentinel")
	hooksPath := installHookFor(t, root, fakeVCSentinel)

	// Over budget: fake vcsentinel exits 1, git commit must be blocked.
	writeFakeVCSentinel(t, fakeVCSentinel, true)
	writeTestLines(t, filepath.Join(root, "large.go"), 401)
	runGitIn(t, root, "add", "large.go")
	base := runGitIn(t, root, "rev-parse", "HEAD")

	output, err := commitWithHooks(t, root, hooksPath, "feat: too large")
	if err == nil {
		t.Fatalf("git commit should have been blocked by the hook, but succeeded:\n%s", output)
	}
	if !strings.Contains(string(output), "STAGED VOLUME REJECTED BY FAKE VCSENTINEL") {
		t.Fatalf("commit was blocked for the wrong reason (hook did not run?):\n%s", output)
	}
	if head := runGitIn(t, root, "rev-parse", "HEAD"); head != base {
		t.Fatalf("a blocked commit moved HEAD: %s -> %s", base, head)
	}

	// Within budget: fake vcsentinel exits 0, the same pipeline commits.
	writeFakeVCSentinel(t, fakeVCSentinel, false)
	runGitIn(t, root, "reset", "-q", "HEAD")
	writeTestLines(t, filepath.Join(root, "small.go"), 10)
	runGitIn(t, root, "add", "small.go")

	output, err = commitWithHooks(t, root, hooksPath, "feat: small")
	if err != nil {
		t.Fatalf("git commit within budget should succeed through the hook: %v\n%s", err, output)
	}
	if head := runGitIn(t, root, "rev-parse", "HEAD"); head == base {
		t.Fatal("an allowed commit did not move HEAD")
	}
}

// runGitIn runs git with an explicit working directory (used by the hook
// enforcement test, which must not depend on the process-wide cwd).
func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
