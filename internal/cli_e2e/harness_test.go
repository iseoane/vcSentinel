package cli_e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var (
	sharedCLIBinaryOnce       sync.Once
	sharedCLIBinaryDir        string
	sharedCLIBinaryPath       string
	sharedCLIBinaryErr        error
	sharedCLIBinaryDiagnostic string
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedCLIBinaryDir != "" {
		if err := os.RemoveAll(sharedCLIBinaryDir); err != nil {
			fmt.Fprintf(os.Stderr, "failed to remove shared CLI binary directory %q: %v\n", sharedCLIBinaryDir, err)
		}
	}
	os.Exit(code)
}

func TestCLIRunnerExecutesIsolatedBinary(t *testing.T) {
	runner := newCLIRunner(t)
	rootStatusBefore := runner.gitStatus(repositoryRoot(t))
	realHomeStateBefore := pathState(t, filepath.Join(realHome(t), ".vcsentinel", "repositories.json"))

	runner.writeFile("fixture.txt", "isolated fixture\n")
	if commit := runner.commit("test: seed isolated repository"); commit == "" {
		t.Fatal("commit returned an empty revision")
	}

	version := runner.run("version")
	if version.ExitCode != 0 {
		t.Fatalf("version failed:\n%s", version.Diagnostic())
	}
	if !strings.Contains(version.Stdout, "vcSentinel version") {
		t.Fatalf("version output does not identify vcSentinel: %q", version.Stdout)
	}

	help := runner.run("help")
	if help.ExitCode != 0 {
		t.Fatalf("help failed:\n%s", help.Diagnostic())
	}
	if !strings.Contains(help.Stdout, "Usage: vcsentinel") {
		t.Fatalf("help output does not contain the public usage: %q", help.Stdout)
	}

	notInitialized := runner.run("check")
	if notInitialized.ExitCode != 1 {
		t.Fatalf("check exit code = %d, want 1:\n%s", notInitialized.ExitCode, notInitialized.Diagnostic())
	}
	if !strings.Contains(notInitialized.Stdout, "not been initialized") {
		t.Fatalf("check did not report the disposable repository state: %q", notInitialized.Stdout)
	}

	gotRepository := filepath.Clean(filepath.FromSlash(strings.TrimSpace(runner.git("rev-parse", "--show-toplevel").Stdout)))
	if gotRepository != filepath.Clean(runner.repository) {
		t.Fatalf("runner Git repository = %q, want %q", gotRepository, runner.repository)
	}
	if _, err := os.Stat(filepath.Join(runner.repository, ".vcsentinel")); !os.IsNotExist(err) {
		t.Fatalf("public commands created repository state: stat error = %v", err)
	}

	if got := runner.gitStatus(repositoryRoot(t)); got != rootStatusBefore {
		t.Fatalf("real repository status changed:\nbefore: %q\nafter:  %q", rootStatusBefore, got)
	}
	if got := pathState(t, filepath.Join(realHome(t), ".vcsentinel", "repositories.json")); got != realHomeStateBefore {
		t.Fatalf("real home repository registry changed:\nbefore: %q\nafter:  %q", realHomeStateBefore, got)
	}
}

type cliRunner struct {
	t          *testing.T
	repository string
	binary     string
	gitPath    string
	env        []string
}

type commandResult struct {
	Command  []string
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

func sharedCLIBinary(t *testing.T, root, goPath string, buildEnv []string) string {
	t.Helper()
	sharedCLIBinaryOnce.Do(func() {
		var err error
		sharedCLIBinaryDir, err = os.MkdirTemp("", "vcsentinel-cli-e2e-binary-")
		if err != nil {
			sharedCLIBinaryErr = err
			sharedCLIBinaryDiagnostic = fmt.Sprintf("create shared CLI binary directory: %v", err)
			return
		}
		sharedCLIBinaryPath = filepath.Join(sharedCLIBinaryDir, binaryName())

		build := exec.Command(goPath, "build", "-o", sharedCLIBinaryPath, "./cmd/vcsentinel")
		build.Dir = root
		build.Env = buildEnv
		var stdout, stderr bytes.Buffer
		build.Stdout = &stdout
		build.Stderr = &stderr
		if err := build.Run(); err != nil {
			result := commandResult{
				Command:  append([]string{goPath}, "build", "-o", sharedCLIBinaryPath, "./cmd/vcsentinel"),
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
				ExitCode: exitCode(err),
				Err:      err,
			}
			sharedCLIBinaryErr = err
			sharedCLIBinaryDiagnostic = result.Diagnostic()
		}
	})
	if sharedCLIBinaryErr != nil {
		if sharedCLIBinaryPath == "" {
			t.Fatalf("shared CLI binary setup failed:\n%s", sharedCLIBinaryDiagnostic)
		}
		t.Fatalf("go build failed:\n%s", sharedCLIBinaryDiagnostic)
	}
	return sharedCLIBinaryPath
}

func newCLIRunner(t *testing.T) *cliRunner {
	t.Helper()

	root := repositoryRoot(t)
	goPath := lookupTool(t, "go")
	gitPath := lookupTool(t, "git")
	pathValue := isolatedPath(t, goPath, gitPath)
	home := t.TempDir()
	configHome := t.TempDir()
	tempDir := t.TempDir()
	runner := &cliRunner{
		t:          t,
		repository: t.TempDir(),
		gitPath:    gitPath,
		env: isolatedEnvironment(
			home,
			configHome,
			filepath.Join(configHome, "gitconfig"),
			tempDir,
			pathValue,
		),
	}

	moduleCache := goEnvironmentValue(t, goPath, "GOMODCACHE")
	buildEnv := append([]string(nil), runner.env...)
	buildEnv = append(buildEnv,
		"GOCACHE="+t.TempDir(),
		"GOMODCACHE="+moduleCache,
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
	)
	runner.binary = sharedCLIBinary(t, root, goPath, buildEnv)

	runner.mustGit("init", "-q")
	runner.mustGit("config", "--local", "user.name", "vcSentinel E2E")
	runner.mustGit("config", "--local", "user.email", "vcsentinel-e2e@example.invalid")
	runner.mustGit("config", "--local", "commit.gpgsign", "false")
	return runner
}

func (r *cliRunner) run(args ...string) commandResult {
	r.t.Helper()
	return r.runInDir(r.binary, r.repository, args...)
}

func (r *cliRunner) git(args ...string) commandResult {
	r.t.Helper()
	return r.runInDir(r.gitPath, r.repository, args...)
}

func (r *cliRunner) gitStatus(directory string) string {
	r.t.Helper()
	result := r.runInDir(r.gitPath, directory, "status", "--porcelain", "--untracked-files=all")
	if result.ExitCode != 0 {
		r.t.Fatalf("git status failed:\n%s", result.Diagnostic())
	}
	return result.Stdout
}

func (r *cliRunner) runInDir(executable, directory string, args ...string) commandResult {
	r.t.Helper()
	commandArgs := append([]string{executable}, args...)
	command := exec.Command(executable, args...)
	command.Dir = directory
	command.Env = append([]string(nil), r.env...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return commandResult{
		Command:  commandArgs,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(err),
		Err:      err,
	}
}

func (r *cliRunner) mustGit(args ...string) commandResult {
	r.t.Helper()
	result := r.git(args...)
	if result.ExitCode != 0 {
		r.t.Fatalf("git command failed:\n%s", result.Diagnostic())
	}
	return result
}

func (r *cliRunner) writeFile(name, content string) {
	r.t.Helper()
	if filepath.IsAbs(name) {
		r.t.Fatalf("repository file must be relative: %q", name)
	}
	path := filepath.Join(r.repository, name)
	relative, err := filepath.Rel(r.repository, path)
	if err != nil {
		r.t.Fatalf("resolve repository file %q: %v", name, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		r.t.Fatalf("repository file escapes the fixture: %q", name)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatalf("create directory for %q: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatalf("write repository file %q: %v", name, err)
	}
}

func (r *cliRunner) commit(message string) string {
	r.t.Helper()
	r.mustGit("add", "--all")
	r.mustGit("commit", "--quiet", "--message", message)
	return strings.TrimSpace(r.mustGit("rev-parse", "HEAD").Stdout)
}

func (r commandResult) Diagnostic() string {
	var diagnostic strings.Builder
	fmt.Fprintf(&diagnostic, "command %q exited with code %d", strings.Join(r.Command, " "), r.ExitCode)
	if r.Err != nil {
		fmt.Fprintf(&diagnostic, " (%v)", r.Err)
	}
	fmt.Fprintf(&diagnostic, "\nstdout:\n%s", r.Stdout)
	if r.Stdout == "" {
		diagnostic.WriteString("<empty>\n")
	}
	fmt.Fprintf(&diagnostic, "stderr:\n%s", r.Stderr)
	if r.Stderr == "" {
		diagnostic.WriteString("<empty>\n")
	}
	return diagnostic.String()
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate the CLI E2E harness source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %q does not contain go.mod: %v", root, err)
	}
	return root
}

func lookupTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("required tool %q is not available: %v", name, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve path for tool %q: %v", name, err)
	}
	return absolute
}

func isolatedPath(t *testing.T, required ...string) string {
	t.Helper()
	seen := make(map[string]bool)
	directories := make([]string, 0, len(required)+2)
	add := func(path string) {
		directory := filepath.Dir(path)
		if !seen[directory] {
			seen[directory] = true
			directories = append(directories, directory)
		}
	}
	for _, path := range required {
		add(path)
	}
	for _, name := range []string{"sh", "bash", "cmd.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			add(path)
		}
	}
	if len(directories) == 0 {
		t.Fatal("no tool directories were resolved for the isolated PATH")
	}
	return strings.Join(directories, string(os.PathListSeparator))
}

func isolatedEnvironment(home, configHome, gitConfig, tempDir, pathValue string) []string {
	environment := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + configHome,
		"GIT_CONFIG_GLOBAL=" + gitConfig,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"PATH=" + pathValue,
		"TMPDIR=" + tempDir,
		"TMP=" + tempDir,
		"TEMP=" + tempDir,
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"ComSpec", "COMSPEC", "PATHEXT", "SystemRoot", "WINDIR"} {
			if value := os.Getenv(name); value != "" {
				environment = append(environment, name+"="+value)
			}
		}
	}
	return environment
}

func goEnvironmentValue(t *testing.T, goPath, name string) string {
	t.Helper()
	command := exec.Command(goPath, "env", name)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go env %s failed: %v", name, err)
	}
	value := strings.TrimSpace(string(output))
	if value == "" {
		t.Fatalf("go env %s returned an empty value", name)
	}
	return value
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "vcsentinel.exe"
	}
	return "vcsentinel"
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func realHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve the operator home: %v", err)
	}
	return home
}

func pathState(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "absent"
		}
		t.Fatalf("inspect %s: %v", path, err)
	}
	return fmt.Sprintf("%s:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
}
