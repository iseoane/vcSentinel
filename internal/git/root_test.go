package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsSamePath(t *testing.T) {
	base := filepath.Join("repos", "demo")
	withTrailingSlash := base + string(filepath.Separator)
	oppositeSeparator := filepath.ToSlash(base)

	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "identical", a: base, b: base, want: true},
		{name: "with trailing slash", a: base, b: withTrailingSlash, want: true},
		{name: "with opposite separator", a: base, b: oppositeSeparator, want: true},
		{name: "different paths", a: base, b: filepath.Join("repos", "other"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSamePath(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("IsSamePath(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsSamePathCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive comparison only applies on Windows")
	}
	if !IsSamePath(`C:\Repos\Demo`, `c:\repos\demo`) {
		t.Error("IsSamePath should ignore case on Windows")
	}
}

func TestGetWorktreeRootAtRepoRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	root, err := GetWorktreeRoot()
	if err != nil {
		t.Fatalf("GetWorktreeRoot returned error: %v", err)
	}
	if !IsSamePath(root, dir) {
		t.Errorf("GetWorktreeRoot() = %q, want %q", root, dir)
	}
}

func TestGetWorktreeRootFromSubdirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	sub := filepath.Join(dir, "src", "package")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("could not create the subdirectory: %v", err)
	}
	t.Chdir(sub)

	root, err := GetWorktreeRoot()
	if err != nil {
		t.Fatalf("GetWorktreeRoot returned error: %v", err)
	}
	if !IsSamePath(root, dir) {
		t.Errorf("GetWorktreeRoot() = %q, want %q", root, dir)
	}
}

func TestGetWorktreeRootOutsideRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	t.Chdir(t.TempDir())

	if _, err := GetWorktreeRoot(); err == nil {
		t.Error("expected an error when asking for the root outside a Git repository")
	}
}
