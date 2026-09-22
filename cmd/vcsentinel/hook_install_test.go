package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
func writeForeignHook(t *testing.T, root, content string, mode os.FileMode) string {
	t.Helper()
	commonDir, err := git.GetGitCommonDir(root)
	if err != nil {
		t.Fatalf("could not resolve the common-dir: %v", err)
	}
	hookPath := filepath.Join(commonDir, "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return hookPath
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

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

	skillPath := filepath.Join(root, ".agents", "skills", "vcsentinel", "SKILL.md")
	skillBefore, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("init did not create the vcSentinel skill: %v", err)
	}
	if string(skillBefore) != vcsentinelSkillContent {
		t.Errorf("init installed unexpected skill content:\n%s\nexpected:\n%s", skillBefore, vcsentinelSkillContent)
	}

	if !setup.IsInitialized(root) {
		t.Error("init did not leave the per-project configuration (.vcsentinel/vcsentinel.yml)")
	}
	if count := repositoryCount(t, registryPath); count != 1 {
		t.Fatalf("init registered %d repositories, want 1", count)
	}

	t.Run("is idempotent", func(t *testing.T) {
		hookBefore, err := os.ReadFile(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		skillBefore, err := os.ReadFile(skillPath)
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
		skillAfter, err := os.ReadFile(skillPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(skillBefore) != string(skillAfter) {
			t.Error("a second init rewrote the agent skill")
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

func TestInitPreservesForeignPreCommitHook(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	foreign := []byte("#!/bin/sh\necho foreign hook\n")
	hookPath := writeForeignHook(t, root, string(foreign), 0755)

	runInit(root)

	backupPath := originalHookPath(hookPath)
	backup := mustReadFile(t, backupPath)
	if string(backup) != string(foreign) {
		t.Errorf("preserved hook changed: %q, want %q", backup, foreign)
	}
	wrapper := mustReadFile(t, hookPath)
	if !strings.Contains(string(wrapper), "vcsentinel:pre-commit-wrapper:v1") {
		t.Errorf("installed hook is not a vcSentinel wrapper: %q", wrapper)
	}
	if count := repositoryCount(t, registryPath); count != 1 {
		t.Fatalf("init registered %d repositories, want 1", count)
	}
}

func TestLegacyHookAtRelocatedVCSentinelPathRemainsOwned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink ownership coverage is platform-specific")
	}
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	relocated := filepath.Join(t.TempDir(), "relocated vcsentinel")
	if err := os.Symlink(current, relocated); err != nil {
		t.Skipf("cannot create executable relocation symlink: %v", err)
	}
	hookPath := writeForeignHook(t, root, generateLegacyHookScriptFor(relocated), 0755)
	legacy := string(mustReadFile(t, hookPath))

	runInit(root)
	if _, err := os.Stat(originalHookPath(hookPath)); !os.IsNotExist(err) {
		t.Fatalf("relocated legacy hook was wrapped: %v", err)
	}
	if got := string(mustReadFile(t, hookPath)); got != legacy {
		t.Fatalf("relocated legacy hook changed: %q", got)
	}
	runUninit(root)
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("uninit did not remove the owned relocated legacy hook: %v", err)
	}
}

func TestSpoofedDirectMarkerRemainsForeign(t *testing.T) {
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	foreign := "#!/bin/sh\n" + hookDirectMarker + "\n" + hookDirectExecutablePrefix + "/tmp/foreign-tool\n\"/tmp/foreign-tool\" check --staged\n"
	hookPath := writeForeignHook(t, root, foreign, 0755)

	runInit(root)
	if got := string(mustReadFile(t, originalHookPath(hookPath))); got != foreign {
		t.Fatalf("spoofed hook was not preserved: %q", got)
	}
	runUninit(root)
	if got := string(mustReadFile(t, hookPath)); got != foreign {
		t.Fatalf("spoofed hook was removed or changed: %q", got)
	}
}

func TestForeignOrphanedHookSidecarFailsClosed(t *testing.T) {
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	commonDir, err := git.GetGitCommonDir(root)
	if err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(commonDir, "hooks", "pre-commit")
	backupPath := originalHookPath(hookPath)
	foreign := "foreign tool sidecar\n"
	if err := os.MkdirAll(filepath.Dir(backupPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	checkerPath := filepath.Join(root, "fakebin", "vcsentinel")
	writeExecutable(t, checkerPath, "#!/bin/sh\nexit 0\n")

	if err := installPreCommitHookFor(hookPath, checkerPath); err == nil {
		t.Fatal("an orphaned foreign sidecar was recovered without transaction state")
	}
	if got := string(mustReadFile(t, backupPath)); got != foreign {
		t.Fatalf("foreign sidecar changed: %q", got)
	}
	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Fatalf("orphan recovery created an active hook: %v", err)
	}
}

func TestDirectHookQuotesExecutablePath(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "space \" apostrophe'", "vcsentinel")
	argsPath := filepath.Join(t.TempDir(), "hook args.txt")
	expected := shellQuote(filepath.ToSlash(executable)) + " check --staged"
	if !strings.Contains(generateHookScriptFor(executable), expected) {
		t.Fatalf("direct hook did not use safe executable quoting: %q", generateHookScriptFor(executable))
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell execution coverage uses sh; generation is checked above")
	}
	writeExecutable(t, executable, "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+shellQuote(argsPath)+"\n")
	hookPath := filepath.Join(t.TempDir(), "pre-commit")
	if err := os.WriteFile(hookPath, []byte(generateHookScriptFor(executable)), 0755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", hookPath).CombinedOutput(); err != nil {
		t.Fatalf("quoted direct hook failed: %v\n%s", err, output)
	}
	if got := string(mustReadFile(t, argsPath)); got != "check\n--staged\n" {
		t.Fatalf("direct hook arguments = %q", got)
	}
}

func TestForeignDirectLookingHookIsNotOwned(t *testing.T) {
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	foreign := "#!/bin/sh\n\"/tmp/foreign-tool\" check --staged\n"
	hookPath := writeForeignHook(t, root, foreign, 0755)

	runInit(root)
	if got := string(mustReadFile(t, originalHookPath(hookPath))); got != foreign {
		t.Fatalf("preserved hook = %q, want %q", got, foreign)
	}
	if !hasHookWrapperMarker(string(mustReadFile(t, hookPath))) {
		t.Fatal("foreign direct-looking hook was not wrapped")
	}

	runUninit(root)
	if got := string(mustReadFile(t, hookPath)); got != foreign {
		t.Fatalf("restored hook = %q, want %q", got, foreign)
	}
}

func TestInstallRecoveryFailsClosedWithoutOriginalSidecar(t *testing.T) {
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	active := "#!/bin/sh\necho active foreign hook\n"
	hookPath := writeForeignHook(t, root, active, 0755)
	info, err := os.Lstat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := beginHookTransaction(hookPath, "install", "", hookContentHash([]byte("expected original\n")), info.Mode().Perm(), "regular"); err != nil {
		t.Fatal(err)
	}
	transactionPath := hookTransactionPath(hookPath)

	if err := installPreCommitHookFor(hookPath, filepath.Join(root, "missing-vcsentinel")); err == nil {
		t.Fatal("install recovery accepted an unverified active hook")
	}
	if got := string(mustReadFile(t, hookPath)); got != active {
		t.Fatalf("active hook changed during failed recovery: %q", got)
	}
	if _, err := os.Stat(transactionPath); err != nil {
		t.Fatalf("failed recovery cleared transaction state: %v", err)
	}
}

func TestInitRecoversInterruptedForeignHookInstall(t *testing.T) {
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	foreign := "#!/bin/sh\necho interrupted original\n"
	hookPath := writeForeignHook(t, root, foreign, 0755)
	backupPath := originalHookPath(hookPath)
	info, err := os.Lstat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := beginHookTransaction(hookPath, "install", "", hookContentHash([]byte(foreign)), info.Mode().Perm(), "regular"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(hookPath, backupPath); err != nil {
		t.Fatal(err)
	}
	checkerPath := filepath.Join(root, "fakebin", "vcsentinel")
	writeExecutable(t, checkerPath, "#!/bin/sh\nexit 0\n")

	if err := installPreCommitHookFor(hookPath, checkerPath); err != nil {
		t.Fatalf("interrupted install recovery failed: %v", err)
	}
	if !hasHookWrapperMarker(string(mustReadFile(t, hookPath))) {
		t.Fatal("recovery installed a direct hook instead of a wrapper")
	}
	if got := string(mustReadFile(t, backupPath)); got != foreign {
		t.Fatalf("recovered backup = %q, want %q", got, foreign)
	}
}

func TestRestoreChainedHookValidatesOriginalAfterRename(t *testing.T) {
	hookDir := t.TempDir()
	hookPath := filepath.Join(hookDir, "pre-commit")
	backupPath := originalHookPath(hookPath)
	wrapper := []byte("#!/bin/sh\necho wrapper\n")
	original := []byte("#!/bin/sh\necho original\n")
	if err := os.WriteFile(hookPath, wrapper, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, original, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := hookWrapperMetadata{
		originalPath: backupPath,
		originalKind: "regular",
		originalMode: 0755,
		originalHash: hookContentHash([]byte("unexpected original\n")),
	}

	if err := restoreChainedHook(hookPath, metadata, hookContentHash(wrapper)); err == nil {
		t.Fatal("restore accepted an original that failed post-rename validation")
	}
	if got := string(mustReadFile(t, hookPath)); got != string(original) {
		t.Fatalf("active hook = %q, want %q", got, original)
	}
	if _, err := os.Stat(hookPath + ".vcsentinel-wrapper"); err != nil {
		t.Fatalf("post-validation failure lost recoverable wrapper state: %v", err)
	}
	if _, err := os.Stat(hookTransactionPath(hookPath)); err != nil {
		t.Fatalf("post-validation failure cleared transaction state: %v", err)
	}
}

func TestUninitRecoversInterruptedHookRestoration(t *testing.T) {
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	foreign := "#!/bin/sh\necho restore me\n"
	hookPath := writeForeignHook(t, root, foreign, 0755)
	checkerPath := filepath.Join(root, "fakebin", "vcsentinel")
	writeExecutable(t, checkerPath, "#!/bin/sh\nexit 0\n")
	if err := installPreCommitHookFor(hookPath, checkerPath); err != nil {
		t.Fatalf("could not install chained hook: %v", err)
	}
	wrapper := mustReadFile(t, hookPath)
	temporaryPath := hookPath + ".vcsentinel-wrapper"
	if err := os.Rename(hookPath, temporaryPath); err != nil {
		t.Fatal(err)
	}
	metadata, err := parseHookWrapper(string(wrapper))
	if err != nil {
		t.Fatal(err)
	}
	if err := beginHookTransaction(hookPath, "restore", hookContentHash(wrapper), metadata.originalHash, metadata.originalMode, metadata.originalKind); err != nil {
		t.Fatal(err)
	}

	runUninit(root)
	if got := string(mustReadFile(t, hookPath)); got != foreign {
		t.Fatalf("restored hook = %q, want %q", got, foreign)
	}
	if _, err := os.Stat(temporaryPath); !os.IsNotExist(err) {
		t.Fatalf("recovery left the temporary wrapper: %v", err)
	}
	if _, err := os.Stat(originalHookPath(hookPath)); !os.IsNotExist(err) {
		t.Fatalf("recovery left the original sidecar: %v", err)
	}
	if _, err := os.Stat(hookTransactionPath(hookPath)); !os.IsNotExist(err) {
		t.Fatalf("recovery left transaction state: %v", err)
	}
}

func TestChainedPreCommitHookRunsBothCommandsWithDeterministicFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the executable hook fixture uses sh; Windows coverage is compile-time here")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available in PATH")
	}

	tests := []struct {
		name     string
		original int
		check    int
		wantExit int
	}{
		{name: "both succeed", original: 0, check: 0, wantExit: 0},
		{name: "original failure is retained", original: 7, check: 0, wantExit: 7},
		{name: "check failure is retained", original: 0, check: 9, wantExit: 9},
		{name: "original failure wins when both fail", original: 7, check: 9, wantExit: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := prepareInitRepository(t)
			logPath := filepath.Join(root, "hook-order.log")
			original := "#!/bin/sh\nprintf '%s\\n' original >> " + shellQuote(logPath) + "\nexit " + strconv.Itoa(test.original) + "\n"
			hookPath := writeForeignHook(t, root, original, 0755)
			checkerPath := filepath.Join(root, "fakebin", "vcsentinel")
			checker := "#!/bin/sh\nprintf '%s\\n' check >> " + shellQuote(logPath) + "\nexit " + strconv.Itoa(test.check) + "\n"
			writeExecutable(t, checkerPath, checker)

			if err := installPreCommitHookFor(hookPath, checkerPath); err != nil {
				t.Fatalf("could not install chained hook: %v", err)
			}
			command := exec.Command("sh", hookPath)
			output, err := command.CombinedOutput()
			if got := exitCode(err); got != test.wantExit {
				t.Fatalf("hook exit code = %d, want %d; output: %s", got, test.wantExit, output)
			}
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("hook did not write execution log: %v", err)
			}
			if got := string(log); got != "original\ncheck\n" {
				t.Fatalf("execution order = %q, want %q", got, "original\\ncheck\\n")
			}
		})
	}
}

func TestForeignHookInitIsIdempotentAndUninitRestoresIt(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	foreign := []byte("#!/bin/sh\necho preserve me\n")
	hookPath := writeForeignHook(t, root, string(foreign), 0700)

	runInit(root)
	wrapperBefore := mustReadFile(t, hookPath)
	backupPath := originalHookPath(hookPath)
	backupBefore := mustReadFile(t, backupPath)
	runInit(root)
	wrapperAfter := mustReadFile(t, hookPath)
	backupAfter := mustReadFile(t, backupPath)
	if string(wrapperAfter) != string(wrapperBefore) {
		t.Fatal("repeated init changed the chained wrapper")
	}
	if string(backupAfter) != string(backupBefore) {
		t.Fatal("repeated init changed the preserved foreign hook")
	}
	if strings.Count(string(wrapperAfter), "check --staged") != 1 {
		t.Fatalf("repeated init duplicated the staged check: %q", wrapperAfter)
	}

	runUninit(root)
	restored, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("uninit did not restore the foreign hook: %v", err)
	}
	if string(restored) != string(foreign) {
		t.Fatalf("restored hook = %q, want %q", restored, foreign)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("uninit left the vcSentinel backup: %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(hookPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0700 {
			t.Errorf("restored hook mode = %o, want 0700", got)
		}
	}
	if count := repositoryCount(t, registryPath); count != 0 {
		t.Fatalf("uninit left %d repositories registered", count)
	}
}

func TestUninitPreservesModifiedChainedHook(t *testing.T) {
	_ = isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	foreign := []byte("#!/bin/sh\necho preserve modified state\n")
	hookPath := writeForeignHook(t, root, string(foreign), 0755)

	runInit(root)
	if err := os.WriteFile(hookPath, append(mustReadFile(t, hookPath), []byte("# local modification\n")...), 0755); err != nil {
		t.Fatal(err)
	}
	modified := mustReadFile(t, hookPath)

	runUninit(root)
	current, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("uninit removed the modified wrapper: %v", err)
	}
	if string(current) != string(modified) {
		t.Fatal("uninit changed a modified vcSentinel wrapper")
	}
	backup := mustReadFile(t, originalHookPath(hookPath))
	if string(backup) != string(foreign) {
		t.Fatal("uninit changed the preserved foreign hook")
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return -1
	}
	return exitErr.ExitCode()
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func TestInitPreservesForeignAgentSkill(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	skillPath := filepath.Join(root, ".agents", "skills", "vcsentinel", "SKILL.md")
	foreign := "---\nname: another-skill\n---\n\n# Keep this skill\n"
	if err := os.MkdirAll(filepath.Dir(skillPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte(foreign), 0644); err != nil {
		t.Fatal(err)
	}

	runInit(root)

	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("init removed the foreign skill: %v", err)
	}
	if string(data) != foreign {
		t.Errorf("init overwrote the foreign skill: %q", data)
	}

	runUninit(root)
	data, err = os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("uninit removed the foreign skill: %v", err)
	}
	if string(data) != foreign {
		t.Errorf("uninit modified the foreign skill: %q", data)
	}
	if count := repositoryCount(t, registryPath); count != 0 {
		t.Errorf("uninit left %d repositories registered", count)
	}
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
	skillPath := filepath.Join(root, ".agents", "skills", "vcsentinel", "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		t.Fatalf("the vcSentinel skill was not installed after init: %v", err)
	}

	runUninit(root)

	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("uninit did not remove the installed hook: %v", err)
	}
	if setup.IsInitialized(root) {
		t.Error("uninit did not remove the per-project configuration")
	}
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Errorf("uninit did not remove the vcSentinel skill: %v", err)
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
	skillPath := filepath.Join(root, ".agents", "skills", "vcsentinel", "SKILL.md")
	foreign := "#!/bin/sh\necho other-tool\n"
	if err := os.WriteFile(hookPath, []byte(foreign), 0755); err != nil {
		t.Fatal(err)
	}
	modifiedSkill := vcsentinelSkillContent + "\nLocal repository instructions.\n"
	if err := os.WriteFile(skillPath, []byte(modifiedSkill), 0644); err != nil {
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
	skillData, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("uninit removed a modified skill: %v", err)
	}
	if string(skillData) != modifiedSkill {
		t.Errorf("uninit modified the local skill: %q", skillData)
	}
	if count := repositoryCount(t, registryPath); count != 0 {
		t.Errorf("foreign hook prevented registry removal: %d repositories remain", count)
	}
}

func TestUninitKeepsRegistryWhenCleanupFails(t *testing.T) {
	registryPath := isolateRepositoryRegistry(t)
	root := prepareInitRepository(t)
	runInit(root)
	configPath := filepath.Join(root, ".vcsentinel", "vcsentinel.yml")
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
