package cli_e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type setupReleaseFixture struct {
	mu            sync.RWMutex
	releaseStatus int
	releaseBody   []byte
	assetStatus   int
	assetBody     []byte
}

func (f *setupReleaseFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	switch r.URL.Path {
	case "/release":
		if f.releaseStatus >= http.StatusBadRequest {
			http.Error(w, "release fixture failure", f.releaseStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(f.releaseBody)
	case "/assets/release":
		if f.assetStatus >= http.StatusBadRequest {
			http.Error(w, "asset fixture failure", f.assetStatus)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(f.assetBody)
	default:
		http.NotFound(w, r)
	}
}

func (f *setupReleaseFixture) setRelease(body []byte, status int, asset []byte, assetStatus int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseBody = append([]byte(nil), body...)
	f.releaseStatus = status
	f.assetBody = append([]byte(nil), asset...)
	f.assetStatus = assetStatus
}

func setupReleaseBody(t *testing.T, tag, assetName, assetURL string, includeAsset bool) []byte {
	t.Helper()
	assets := []map[string]string{}
	if includeAsset {
		assets = append(assets, map[string]string{
			"name":                 assetName,
			"browser_download_url": "https://unused.example.invalid/" + assetName,
			"url":                  assetURL,
		})
	}
	body, err := json.Marshal(map[string]any{
		"tag_name": tag,
		"assets":   assets,
	})
	if err != nil {
		t.Fatalf("could not encode the release fixture: %v", err)
	}
	return body
}

func setupAssetName() string {
	name := "vcsentinel-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func withCLIEnvironment(runner *cliRunner, values map[string]string) *cliRunner {
	copy := *runner
	copy.env = append([]string(nil), runner.env...)
	for name, value := range values {
		prefix := name + "="
		replaced := false
		for index, entry := range copy.env {
			if strings.HasPrefix(entry, prefix) {
				copy.env[index] = prefix + value
				replaced = true
				break
			}
		}
		if !replaced {
			copy.env = append(copy.env, prefix+value)
		}
	}
	return &copy
}

func buildSetupReleaseBinary(t *testing.T, runner *cliRunner, version string) []byte {
	t.Helper()
	root := repositoryRoot(t)
	goPath := lookupTool(t, "go")
	output := filepath.Join(t.TempDir(), binaryName())
	command := exec.Command(goPath, "build", "-ldflags=-X main.version="+version, "-o", output, "./cmd/vcsentinel")
	command.Dir = root
	command.Env = append([]string(nil), runner.env...)
	command.Env = append(command.Env,
		"GOCACHE="+t.TempDir(),
		"GOMODCACHE="+goEnvironmentValue(t, goPath, "GOMODCACHE"),
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build release binary %s failed: %v\n%s", version, err, output)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read release binary %s: %v", version, err)
	}
	return data
}

func setupRunnerEnvironment(t *testing.T, runner *cliRunner, serverURL string, installRoot, pathFile string) *cliRunner {
	t.Helper()
	home := cliRunnerEnvironmentValue(t, runner, "HOME")
	if err := os.WriteFile(pathFile, []byte("C:\\Windows;C:\\Unrelated\n"), 0644); err != nil {
		t.Fatalf("seed the disposable Windows PATH file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export KEEP=1\n"), 0644); err != nil {
			t.Fatalf("seed the disposable shell file: %v", err)
		}
	}
	return withCLIEnvironment(runner, map[string]string{
		"GITHUB_TOKEN":                                "e2e-test-token",
		"VCSENTINEL_TEST_RELEASE_API_URL":             serverURL + "/release",
		"VCSENTINEL_TEST_INSTALL_ROOT":                installRoot,
		"VCSENTINEL_TEST_WINDOWS_PATH_FILE":           pathFile,
		"VCSENTINEL_TEST_DISABLE_GO_INSTALL_FALLBACK": "1",
	})
}

func TestCLIPublicSetupLifecycleE2E(t *testing.T) {
	runner := newCLIRunner(t)
	tempDir := cliRunnerEnvironmentValue(t, runner, "TMPDIR")
	home := cliRunnerEnvironmentValue(t, runner, "HOME")
	installRoot := filepath.Join(tempDir, "install-root")
	pathFile := filepath.Join(tempDir, "windows-path.txt")
	assetName := setupAssetName()

	fixture := &setupReleaseFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()

	installedAsset := buildSetupReleaseBinary(t, runner, "e2e-installed")
	upgradedAsset := buildSetupReleaseBinary(t, runner, "e2e-upgraded")
	fixture.setRelease(
		setupReleaseBody(t, "v1-e2e", assetName, server.URL+"/assets/release", true),
		http.StatusOK,
		installedAsset,
		http.StatusOK,
	)
	setupRunner := setupRunnerEnvironment(t, runner, server.URL, installRoot, pathFile)

	configPath := filepath.Join(home, ".vcsentinel", "vcsentinel.yml")
	shellPath := filepath.Join(home, ".bashrc")
	var err error
	var initialShell []byte
	if runtime.GOOS != "windows" {
		initialShell, err = os.ReadFile(shellPath)
		if err != nil {
			t.Fatalf("read seeded shell file: %v", err)
		}
	}
	initialPath, err := os.ReadFile(pathFile)
	if err != nil {
		t.Fatalf("read seeded Windows PATH file: %v", err)
	}

	install := setupRunner.run("install")
	if install.ExitCode != 0 {
		t.Fatalf("public install failed:\n%s", install.Diagnostic())
	}
	installedBinary := filepath.Join(installRoot, binaryName())
	assertExecutableVersion(t, setupRunner, installedBinary, "e2e-installed")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("install did not create the global configuration: %v", err)
	}
	shellAfterInstall, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatalf("read shell file after install: %v", err)
	}
	assertSetupPathEffect(t, installRoot, shellAfterInstall, pathFile, initialPath)

	installAgain := setupRunner.run("install")
	if installAgain.ExitCode != 0 {
		t.Fatalf("repeated public install failed:\n%s", installAgain.Diagnostic())
	}
	assertExecutableVersion(t, setupRunner, installedBinary, "e2e-installed")
	configAfterInstall, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read configuration after repeated install: %v", err)
	}
	if string(configAfterInstall) != string(configBefore) {
		t.Fatal("repeated install overwrote the existing configuration")
	}
	shellAfterRepeat, err := os.ReadFile(shellPath)
	if err != nil {
		t.Fatalf("read shell file after repeated install: %v", err)
	}
	if string(shellAfterRepeat) != string(shellAfterInstall) {
		t.Fatal("repeated install duplicated or changed the PATH block")
	}

	fixture.setRelease(
		setupReleaseBody(t, "v2-e2e", assetName, server.URL+"/assets/release", true),
		http.StatusOK,
		upgradedAsset,
		http.StatusOK,
	)
	if runtime.GOOS == "windows" {
		t.Log("Windows cannot safely replace the executable that is currently running; upgrade remains covered by setup package tests and the Linux public process flow")
	} else {
		upgrade := setupRunner.runInDir(installedBinary, runner.repository, "upgrade")
		if upgrade.ExitCode != 0 {
			t.Fatalf("public upgrade failed:\n%s", upgrade.Diagnostic())
		}
		assertExecutableVersion(t, setupRunner, installedBinary, "e2e-upgraded")
		configAfterUpgrade, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read configuration after upgrade: %v", err)
		}
		if string(configAfterUpgrade) != string(configBefore) {
			t.Fatal("upgrade changed the global configuration")
		}
		shellAfterUpgrade, err := os.ReadFile(shellPath)
		if err != nil {
			t.Fatalf("read shell file after upgrade: %v", err)
		}
		if string(shellAfterUpgrade) != string(shellAfterInstall) {
			t.Fatal("upgrade changed the shell PATH block")
		}
	}

	uninstall := setupRunner.run("uninstall")
	if uninstall.ExitCode != 0 {
		t.Fatalf("public uninstall failed:\n%s", uninstall.Diagnostic())
	}
	assertSetupCleanup(t, installRoot, configPath, shellPath, pathFile, initialShell, initialPath)

	uninstallAgain := setupRunner.run("uninstall")
	if uninstallAgain.ExitCode != 0 {
		t.Fatalf("repeated public uninstall failed:\n%s", uninstallAgain.Diagnostic())
	}
	assertSetupCleanup(t, installRoot, configPath, shellPath, pathFile, initialShell, initialPath)
	if runtime.GOOS == "windows" {
		assertOnlySetupTempEntries(t, tempDir, filepath.Base(pathFile))
	} else {
		assertOnlySetupTempEntries(t, tempDir, filepath.Base(pathFile), filepath.Base(installRoot))
	}
}

func TestCLIPublicSetupReleaseFailuresHaveNoSideEffects(t *testing.T) {
	cases := []struct {
		name          string
		releaseStatus int
		releaseBody   func(t *testing.T, serverURL, assetName string) []byte
		assetStatus   int
	}{
		{
			name:          "HTTP 500",
			releaseStatus: http.StatusInternalServerError,
			releaseBody:   func(t *testing.T, serverURL, assetName string) []byte { return []byte(`{}`) },
		},
		{
			name:        "malformed release",
			releaseBody: func(t *testing.T, serverURL, assetName string) []byte { return []byte("not json") },
		},
		{
			name: "missing asset",
			releaseBody: func(t *testing.T, serverURL, assetName string) []byte {
				return setupReleaseBody(t, "v1-e2e", assetName, serverURL+"/assets/release", false)
			},
		},
		{
			name: "asset download failure",
			releaseBody: func(t *testing.T, serverURL, assetName string) []byte {
				return setupReleaseBody(t, "v1-e2e", assetName, serverURL+"/assets/release", true)
			},
			assetStatus: http.StatusBadGateway,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := newCLIRunner(t)
			tempDir := cliRunnerEnvironmentValue(t, runner, "TMPDIR")
			home := cliRunnerEnvironmentValue(t, runner, "HOME")
			installRoot := filepath.Join(tempDir, "install-root")
			pathFile := filepath.Join(tempDir, "windows-path.txt")
			shellPath := filepath.Join(home, ".bashrc")
			var err error
			var initialShell []byte
			if runtime.GOOS != "windows" {
				if err := os.WriteFile(shellPath, []byte("export KEEP=1\n"), 0644); err != nil {
					t.Fatal(err)
				}
				initialShell, err = os.ReadFile(shellPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			initialPath := []byte("C:\\Windows;C:\\Unrelated\n")
			if err := os.WriteFile(pathFile, initialPath, 0644); err != nil {
				t.Fatal(err)
			}

			fixture := &setupReleaseFixture{}
			server := httptest.NewServer(fixture)
			defer server.Close()
			assetName := setupAssetName()
			fixture.setRelease(testCase.releaseBody(t, server.URL, assetName), testCase.releaseStatus, []byte("not-an-executable"), testCase.assetStatus)
			setupRunner := setupRunnerEnvironment(t, runner, server.URL, installRoot, pathFile)

			result := setupRunner.run("install")
			if result.ExitCode == 0 {
				t.Fatalf("public install unexpectedly succeeded:\n%s", result.Diagnostic())
			}
			if strings.Contains(result.Stdout, "go install") || strings.Contains(result.Stderr, "go install") {
				t.Fatalf("release failure attempted the go install fallback:\n%s", result.Diagnostic())
			}

			configPath := filepath.Join(home, ".vcsentinel", "vcsentinel.yml")
			if _, err := os.Stat(configPath); !os.IsNotExist(err) {
				t.Fatalf("release failure changed the configuration: %v", err)
			}
			assertSetupCleanup(t, installRoot, configPath, shellPath, pathFile, initialShell, initialPath)
			assertOnlySetupTempEntries(t, tempDir, filepath.Base(pathFile))
		})
	}
}

func TestCLISetupRejectsInvalidTestOverrides(t *testing.T) {
	cases := []struct {
		name string
		env  func(t *testing.T, runner *cliRunner) map[string]string
		want string
	}{
		{
			name: "release endpoint",
			env: func(t *testing.T, runner *cliRunner) map[string]string {
				return map[string]string{"VCSENTINEL_TEST_RELEASE_API_URL": "https://example.invalid/releases/latest"}
			},
			want: "VCSENTINEL_TEST_RELEASE_API_URL",
		},
		{
			name: "install root",
			env: func(t *testing.T, runner *cliRunner) map[string]string {
				tempDir := cliRunnerEnvironmentValue(t, runner, "TMPDIR")
				return map[string]string{"VCSENTINEL_TEST_INSTALL_ROOT": filepath.Join(filepath.Dir(tempDir), "outside-install-root")}
			},
			want: "VCSENTINEL_TEST_INSTALL_ROOT",
		},
		{
			name: "Windows PATH file",
			env: func(t *testing.T, runner *cliRunner) map[string]string {
				tempDir := cliRunnerEnvironmentValue(t, runner, "TMPDIR")
				return map[string]string{"VCSENTINEL_TEST_WINDOWS_PATH_FILE": filepath.Join(filepath.Dir(tempDir), "outside-windows-path.txt")}
			},
			want: "VCSENTINEL_TEST_WINDOWS_PATH_FILE",
		},
		{
			name: "fallback override",
			env: func(t *testing.T, runner *cliRunner) map[string]string {
				return map[string]string{"VCSENTINEL_TEST_DISABLE_GO_INSTALL_FALLBACK": "0"}
			},
			want: "VCSENTINEL_TEST_DISABLE_GO_INSTALL_FALLBACK",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := newCLIRunner(t)
			environment := testCase.env(t, runner)
			environment["GITHUB_TOKEN"] = "e2e-test-token"
			environment["VCSENTINEL_TEST_DISABLE_GO_INSTALL_FALLBACK"] = "1"
			if testCase.name == "fallback override" {
				environment["VCSENTINEL_TEST_DISABLE_GO_INSTALL_FALLBACK"] = "0"
			}
			result := withCLIEnvironment(runner, environment).run("install")
			if result.ExitCode == 0 {
				t.Fatalf("install unexpectedly accepted an invalid override:\n%s", result.Diagnostic())
			}
			if !strings.Contains(result.Stdout, testCase.want) {
				t.Fatalf("diagnostic = %q, want it to name %s", result.Stdout, testCase.want)
			}
			if entries, err := os.ReadDir(cliRunnerEnvironmentValue(t, runner, "TMPDIR")); err != nil {
				t.Fatalf("inspect isolated temp directory: %v", err)
			} else if len(entries) != 0 {
				t.Fatalf("invalid override left temporary entries: %v", entries)
			}
		})
	}
}

func assertExecutableVersion(t *testing.T, runner *cliRunner, binary, version string) {
	t.Helper()
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("installed binary %s is missing: %v", binary, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		t.Fatalf("installed binary %s is not executable: %o", binary, info.Mode().Perm())
	}
	result := runner.runInDir(binary, runner.repository, "--version")
	if result.ExitCode != 0 {
		t.Fatalf("installed binary --version failed:\n%s", result.Diagnostic())
	}
	want := "vcSentinel version: " + version
	if !strings.Contains(result.Stdout, want) {
		t.Fatalf("installed --version = %q, want it to contain %q", result.Stdout, want)
	}
}

func assertSetupPathEffect(t *testing.T, installRoot string, shellContent []byte, pathFile string, initialPath []byte) {
	t.Helper()
	line := `export PATH="` + filepath.ToSlash(installRoot) + `:$PATH"`
	if runtime.GOOS == "windows" {
		content, err := os.ReadFile(pathFile)
		if err != nil {
			t.Fatalf("read disposable Windows PATH file: %v", err)
		}
		if !strings.Contains(string(content), installRoot) || !strings.Contains(string(content), "C:\\Unrelated") {
			t.Fatalf("Windows PATH effect lost expected entries: %q", content)
		}
		return
	}
	if !strings.Contains(string(shellContent), line) || !strings.Contains(string(shellContent), "export KEEP=1") {
		t.Fatalf("shell PATH effect did not preserve unrelated content: %q", shellContent)
	}
	if pathFile != "" {
		content, err := os.ReadFile(pathFile)
		if err == nil && string(content) != string(initialPath) {
			t.Fatalf("unused Windows PATH file changed on Unix: %q", content)
		}
	}
}

func assertSetupCleanup(t *testing.T, installRoot, configPath, shellPath, pathFile string, initialShell, initialPath []byte) {
	t.Helper()
	binary := filepath.Join(installRoot, binaryName())
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the binary at %s: %v", binary, err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall left the global configuration: %v", err)
	}
	if runtime.GOOS != "windows" {
		shellContent, err := os.ReadFile(shellPath)
		if err != nil {
			t.Fatalf("read shell file after uninstall: %v", err)
		}
		if string(shellContent) != string(initialShell) {
			t.Fatalf("uninstall did not restore unrelated shell content: %q", shellContent)
		}
	}
	if runtime.GOOS == "windows" {
		pathContent, err := os.ReadFile(pathFile)
		if err != nil {
			t.Fatalf("read Windows PATH file after uninstall: %v", err)
		}
		if string(pathContent) != string(initialPath) {
			t.Fatalf("uninstall did not restore unrelated Windows PATH content: %q", pathContent)
		}
	}
}

func assertOnlySetupTempEntries(t *testing.T, tempDir string, expected ...string) {
	t.Helper()
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("inspect setup temporary directory %s: %v", tempDir, err)
	}
	for _, entry := range entries {
		found := false
		for _, name := range expected {
			if entry.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected setup temporary artifact %s", entry.Name())
		}
	}
	if len(entries) != len(expected) {
		t.Fatalf("temporary directory %s contains %d entries, want %d: %v", tempDir, len(entries), len(expected), entries)
	}
}
