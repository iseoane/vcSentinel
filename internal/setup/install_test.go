package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setHome(t *testing.T, home string) {
	t.Helper()
	// os.UserHomeDir() uses HOME on Unix and USERPROFILE on Windows.
	keys := []string{"HOME"}
	if runtime.GOOS == "windows" {
		keys = append(keys, "USERPROFILE")
	}
	originals := make(map[string]string, len(keys))
	for _, key := range keys {
		originals[key] = os.Getenv(key)
	}
	for _, key := range keys {
		if err := os.Setenv(key, home); err != nil {
			t.Fatalf("could not set %s: %v", key, err)
		}
	}
	t.Cleanup(func() {
		for key, value := range originals {
			os.Setenv(key, value)
		}
	})
}

func TestCreateGlobalConfig(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	if err := createGlobalConfig(); err != nil {
		t.Fatalf("createGlobalConfig returned error: %v", err)
	}
	path := filepath.Join(home, ".vas_sentinel", "vassentinel.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the global config was not created at %s: %v", path, err)
	}
	if len(content) == 0 {
		t.Error("the global config was left empty")
	}

	t.Run("does not overwrite an existing config", func(t *testing.T) {
		before := "previous-content\n"
		if err := os.WriteFile(path, []byte(before), 0644); err != nil {
			t.Fatal(err)
		}
		if err := createGlobalConfig(); err != nil {
			t.Fatalf("second call returned error: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != before {
			t.Errorf("the global config was overwritten: %q, want %q", after, before)
		}
	})
}

func TestCreatePerProjectConfig(t *testing.T) {
	worktree := t.TempDir()

	if err := CreatePerProjectConfig(worktree); err != nil {
		t.Fatalf("CreatePerProjectConfig returned error: %v", err)
	}
	path := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the per-project config was not created at %s: %v", path, err)
	}
	if len(content) == 0 {
		t.Error("the per-project config was left empty")
	}

	t.Run("does not overwrite an existing config", func(t *testing.T) {
		before := "previous-content\n"
		if err := os.WriteFile(path, []byte(before), 0644); err != nil {
			t.Fatal(err)
		}
		if err := CreatePerProjectConfig(worktree); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != before {
			t.Errorf("the per-project config was overwritten: %q, want %q", after, before)
		}
	})
}

func TestTemplateRequestsExternalDiffDisabledByDefault(t *testing.T) {
	if !strings.Contains(perProjectConfigTemplate, "request_external_agent_diff: false") {
		t.Fatal("the template must declare the external request as false")
	}
	if strings.Contains(perProjectConfigTemplate, "allow_external_agent_diff") {
		t.Fatal("the template still carries the old ambiguous consent name")
	}
}

func TestTemplateCodeGraphReviewerDisabledByDefault(t *testing.T) {
	if !strings.Contains(perProjectConfigTemplate, "codegraph_context: false") {
		t.Fatal("the template must disable the CodeGraph reviewer context")
	}
}

// TestTemplateDocumentsDeterministicVerification: the template init writes
// must show the user (commented) the keys that activate deterministic
// verification without an agent — lint_commands, test_commands and
// build_commands — with their effect and an executable example.
func TestTemplateDocumentsDeterministicVerification(t *testing.T) {
	content := perProjectConfigTemplate
	for _, key := range []string{"lint_commands", "test_commands", "build_commands"} {
		if !strings.Contains(content, key) {
			t.Errorf("the per-project template does not document %q", key)
		}
	}
	if !strings.Contains(content, "Deterministic verification WITHOUT agent") {
		t.Error("the template must explain that these keys activate deterministic verification")
	}
	if !strings.Contains(content, `go test ./...`) {
		t.Error("the template must show a test_commands example")
	}
	if !strings.Contains(content, `go build ./...`) {
		t.Error("the template must show a build_commands example")
	}
	if !strings.Contains(content, `supports_scope: true`) || !strings.Contains(content, `scoped_command: "go test {packages}"`) {
		t.Error("the template must document the real unit_test scope")
	}
}

func TestMoveAndReplace(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	destination := filepath.Join(dir, "destination")

	if err := os.WriteFile(source, []byte("new-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old-content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := moveAndReplace(source, destination); err != nil {
		t.Fatalf("moveAndReplace returned error: %v", err)
	}

	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new-content" {
		t.Errorf("the destination was not replaced: %q", content)
	}
}

func TestAddShellPathBlock(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	if err := addShellPathBlock(); err != nil {
		t.Fatalf("addShellPathBlock returned error: %v", err)
	}

	zshrc := filepath.Join(home, ".zshrc")
	content, err := os.ReadFile(zshrc)
	if err != nil {
		t.Fatalf("~/.zshrc was not created: %v", err)
	}
	block := `export PATH="/usr/local/bin:$PATH"`
	if !strings.Contains(string(content), block) {
		t.Errorf("~/.zshrc does not contain the expected block:\n%s", content)
	}

	t.Run("is idempotent", func(t *testing.T) {
		if err := addShellPathBlock(); err != nil {
			t.Fatalf("second call returned error: %v", err)
		}
		after, err := os.ReadFile(zshrc)
		if err != nil {
			t.Fatal(err)
		}
		occurrences := strings.Count(string(after), block)
		if occurrences != 1 {
			t.Errorf("the block should appear exactly once, got %d", occurrences)
		}
	})

	t.Run("adds to bashrc when zshrc does not exist", func(t *testing.T) {
		bashrc := filepath.Join(home, ".bashrc")
		if err := os.WriteFile(bashrc, []byte("export OLD=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := addShellPathBlock(); err != nil {
			t.Fatalf("addShellPathBlock returned error: %v", err)
		}
		content, err := os.ReadFile(bashrc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), block) {
			t.Errorf("~/.bashrc does not contain the expected block:\n%s", content)
		}
	})

	t.Run("adds to both when both exist", func(t *testing.T) {
		zshrc := filepath.Join(home, ".zshrc")
		bashrc := filepath.Join(home, ".bashrc")
		if err := os.WriteFile(zshrc, []byte("export OLD=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bashrc, []byte("export OLD=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := addShellPathBlock(); err != nil {
			t.Fatalf("addShellPathBlock returned error: %v", err)
		}
		for _, path := range []string{zshrc, bashrc} {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(content), block) {
				t.Errorf("%s does not contain the expected block", path)
			}
		}
	})
}

func TestBinaryPaths(t *testing.T) {
	home := t.TempDir()
	if path := windowsBinaryPath(home); path != filepath.Join(home, ".vas_sentinel", "bin", "sentinel.exe") {
		t.Errorf("windowsBinaryPath = %q, want the path under .vas_sentinel/bin/sentinel.exe", path)
	}
	if path := linuxBinaryPath(); path != "/usr/local/bin/sentinel" {
		t.Errorf("linuxBinaryPath = %q, want /usr/local/bin/sentinel", path)
	}
}

func TestNeedsWindowsPathUpdate(t *testing.T) {
	if needsWindowsPathUpdate("C:\\x;C:\\y", "C:\\x") {
		t.Error("should not add a directory already present in the PATH")
	}
	if !needsWindowsPathUpdate("C:\\x", "C:\\new") {
		t.Error("should add a directory missing from the PATH")
	}
}

func TestWindowsUserPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only applies to Windows")
	}
	if _, err := windowsUserPath(); err != nil {
		t.Fatalf("windowsUserPath returned error: %v", err)
	}
}

// TestInstallPlacesExecutableBinaryAndReportsVersion verifies the placement
// half of the install flow on a temporary destination: moveAndReplace puts
// the downloaded artifact in place and the installer's chmod 0755 step makes
// it executable; verifyBinary then proves the installed binary answers
// --version with its real version string.
//
// installLinux itself targets /usr/local/bin and falls back to sudo, so it is
// deliberately NOT invoked from tests; this test exercises its exact placement
// primitives over an injected temporary destination. installWindows is
// HOME-based but shells out to PowerShell for the user PATH update, so its
// logic stays covered by path-building unit tests (TestBinaryPaths,
// TestNeedsWindowsPathUpdate) plus GOOS=windows build+vet as the
// compile-time half.
func TestInstallPlacesExecutableBinaryAndReportsVersion(t *testing.T) {
	dir := t.TempDir()
	download := filepath.Join(dir, "downloaded-sentinel")
	destination := filepath.Join(dir, "installed", goBinaryName())
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS == "windows" {
		if err := os.WriteFile(download, []byte("@echo off\r\necho sentinel v3.2.1\r\n"), 0644); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(download, []byte("#!/bin/sh\necho sentinel v3.2.1\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := moveAndReplace(download, destination); err != nil {
		t.Fatalf("placement failed: %v", err)
	}

	if err := os.Chmod(destination, 0755); err != nil {
		t.Fatalf("chmod of the installed binary failed: %v", err)
	}

	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("the installed binary is missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0100 == 0 {
		t.Errorf("the installed binary is not executable: %o", perm)
	}
	if _, err := os.Stat(download); !os.IsNotExist(err) {
		t.Errorf("the downloaded artifact was not consumed by placement: %v", err)
	}

	output := captureStdout(t, func() {
		if err := verifyBinary(destination); err != nil {
			t.Fatalf("verifyBinary failed on the installed binary: %v", err)
		}
	})
	if !strings.Contains(output, "v3.2.1") {
		t.Errorf("the installed version was not reported: %q", output)
	}
}
