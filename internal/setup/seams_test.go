package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseAPIURLRejectsNonLoopbackOverride(t *testing.T) {
	t.Setenv(testReleaseAPIURLEnv, "https://example.invalid/releases/latest")

	_, err := releaseAPIURL()
	if err == nil {
		t.Fatal("releaseAPIURL accepted a non-loopback override")
	}
	if !strings.Contains(err.Error(), testReleaseAPIURLEnv) {
		t.Fatalf("error = %q, want it to name %s", err, testReleaseAPIURLEnv)
	}
}

func TestReleaseAPIURLAcceptsLoopbackHTTP(t *testing.T) {
	for _, endpoint := range []string{
		"http://localhost:1234/releases/latest",
		"http://127.0.0.1:1234/releases/latest",
		"http://[::1]:1234/releases/latest",
	} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv(testReleaseAPIURLEnv, endpoint)
			got, err := releaseAPIURL()
			if err != nil {
				t.Fatalf("releaseAPIURL returned error: %v", err)
			}
			if got != endpoint {
				t.Errorf("releaseAPIURL = %q, want %q", got, endpoint)
			}
		})
	}
}

func TestInstallRootOverrideMustStayUnderTemp(t *testing.T) {
	inside := filepath.Join(os.TempDir(), "vcsentinel-seams", "install")
	t.Setenv(testInstallRootEnv, inside)
	got, err := installRoot()
	if err != nil {
		t.Fatalf("installRoot rejected a path under temp: %v", err)
	}
	if got != filepath.Clean(inside) {
		t.Errorf("installRoot = %q, want %q", got, filepath.Clean(inside))
	}

	for _, invalid := range []string{"relative/install-root", filepath.Join(filepath.Dir(os.TempDir()), "outside-install-root")} {
		t.Run(invalid, func(t *testing.T) {
			t.Setenv(testInstallRootEnv, invalid)
			if _, err := installRoot(); err == nil || !strings.Contains(err.Error(), testInstallRootEnv) {
				t.Fatalf("installRoot error = %v, want an error naming %s", err, testInstallRootEnv)
			}
		})
	}
}

func TestWindowsPathFileOverrideMustStayUnderTemp(t *testing.T) {
	path := filepath.Join(os.TempDir(), "vcsentinel-seams", "windows-path.txt")
	t.Setenv(testWindowsPathFileEnv, path)
	got, err := windowsPathFileOverride()
	if err != nil {
		t.Fatalf("windowsPathFileOverride rejected a path under temp: %v", err)
	}
	if got != filepath.Clean(path) {
		t.Errorf("windowsPathFileOverride = %q, want %q", got, filepath.Clean(path))
	}

	t.Setenv(testWindowsPathFileEnv, filepath.Join(filepath.Dir(os.TempDir()), "outside-path.txt"))
	if _, err := windowsPathFileOverride(); err == nil || !strings.Contains(err.Error(), testWindowsPathFileEnv) {
		t.Fatalf("windowsPathFileOverride error = %v, want an error naming %s", err, testWindowsPathFileEnv)
	}
}

func TestWindowsPathFileSeamPreservesUnrelatedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user-path.txt")
	t.Setenv(testWindowsPathFileEnv, path)
	original := "C:\\Windows;C:\\Tools\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := addWindowsPath("C:\\VCSentinel"); err != nil {
		t.Fatalf("addWindowsPath returned error: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "C:\\Windows;C:\\Tools;C:\\VCSentinel\n" {
		t.Errorf("PATH after add = %q", content)
	}

	if err := addWindowsPath("C:\\VCSentinel"); err != nil {
		t.Fatalf("repeated addWindowsPath returned error: %v", err)
	}
	if err := removeWindowsPath("C:\\VCSentinel"); err != nil {
		t.Fatalf("removeWindowsPath returned error: %v", err)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Errorf("PATH after remove = %q, want %q", content, original)
	}
}

func TestFallbackOverrideFailsClosed(t *testing.T) {
	previous := fallbackGoInstall
	fallbackGoInstall = true
	t.Cleanup(func() { fallbackGoInstall = previous })

	t.Setenv(testDisableGoInstallEnv, "0")
	if _, err := fallbackSetting(); err == nil || !strings.Contains(err.Error(), testDisableGoInstallEnv) {
		t.Fatalf("fallbackSetting error = %v, want an error naming %s", err, testDisableGoInstallEnv)
	}

	t.Setenv(testDisableGoInstallEnv, "1")
	got, err := fallbackSetting()
	if err != nil {
		t.Fatalf("fallbackSetting returned error for 1: %v", err)
	}
	if got {
		t.Error("fallbackSetting left go install fallback enabled")
	}
}

func TestInstallRootDefaultsRemainPlatformSpecific(t *testing.T) {
	t.Setenv(testInstallRootEnv, "")
	got, err := installRoot()
	if err != nil {
		t.Fatalf("installRoot returned error: %v", err)
	}
	if runtime.GOOS == "windows" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(home, ".vcsentinel", "bin")
		if got != want {
			t.Errorf("installRoot = %q, want %q", got, want)
		}
		return
	}
	if got != "/usr/local/bin" {
		t.Errorf("installRoot = %q, want /usr/local/bin", got)
	}
}
