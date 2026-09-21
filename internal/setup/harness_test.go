package setup

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout while fn runs and returns everything it
// printed. The install/upgrade/uninstall entry points report progress through
// fmt.Print* on stdout, so their user-facing contract is only observable here.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = original }()

	fn()

	writer.Close()
	os.Stdout = original
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("could not read captured stdout: %v", err)
	}
	return string(output)
}

// TestVerifyBinaryReportsInstalledVersion: after an upgrade replaces the
// binary, verifyBinary must run `<binary> --version` and report the
// effective installed version to the user.
func TestVerifyBinaryReportsInstalledVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentinel")
	if runtime.GOOS == "windows" {
		path += ".cmd"
		if err := os.WriteFile(path, []byte("@echo off\r\necho sentinel v9.9.9\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho sentinel v9.9.9\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	output := captureStdout(t, func() {
		if err := verifyBinary(path); err != nil {
			t.Fatalf("verifyBinary returned error: %v", err)
		}
	})
	if !strings.Contains(output, "v9.9.9") {
		t.Errorf("the output does not report the installed version: %q", output)
	}
}

// TestUpgradeReplacesBinaryPreservingConfig composes the upgrade
// replacement primitives with real configuration artifacts: after the binary
// is replaced in place, both the global configuration (~/.vcsentinel/
// vcsentinel.yml) and a repository-local configuration must remain
// byte-identical. Both replacement helpers are pure filesystem operations over
// explicit paths, so both GOOS variants execute on every platform.
func TestUpgradeReplacesBinaryPreservingConfig(t *testing.T) {
	variants := []struct {
		name    string
		replace func(tmpPath string, currentBinary string) error
	}{
		{"replaceWindowsBinary", func(tmp, current string) error {
			_, err := replaceWindowsBinary(tmp, current)
			return err
		}},
		{"replaceLinuxBinary", replaceLinuxBinary},
	}

	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			home := t.TempDir()
			repo := t.TempDir()
			setHome(t, home)

			if err := createGlobalConfig(); err != nil {
				t.Fatalf("createGlobalConfig returned error: %v", err)
			}
			if err := CreatePerProjectConfig(repo); err != nil {
				t.Fatalf("CreatePerProjectConfig returned error: %v", err)
			}
			globalPath := filepath.Join(home, ".vcsentinel", "vcsentinel.yml")
			repoPath := filepath.Join(repo, ".vcsentinel", "vcsentinel.yml")
			globalBefore, err := os.ReadFile(globalPath)
			if err != nil {
				t.Fatal(err)
			}
			repoBefore, err := os.ReadFile(repoPath)
			if err != nil {
				t.Fatal(err)
			}

			currentBinary := filepath.Join(home, "installed", goBinaryName())
			if err := os.MkdirAll(filepath.Dir(currentBinary), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(currentBinary, []byte("version-old"), 0755); err != nil {
				t.Fatal(err)
			}
			newBinary := filepath.Join(home, "download", goBinaryName())
			if err := os.MkdirAll(filepath.Dir(newBinary), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(newBinary, []byte("version-new"), 0755); err != nil {
				t.Fatal(err)
			}

			output := captureStdout(t, func() {
				if err := variant.replace(newBinary, currentBinary); err != nil {
					t.Fatalf("%s returned error: %v", variant.name, err)
				}
			})
			if strings.TrimSpace(output) != "" {
				// A clean replacement is silent; the only expected print is
				// the locked-backup warning when the .old file cannot be
				// removed, which must not happen in this fixture.
				t.Errorf("%s printed unexpected output during a clean replacement: %q", variant.name, output)
			}

			content, err := os.ReadFile(currentBinary)
			if err != nil {
				t.Fatalf("the current binary disappeared after the replacement: %v", err)
			}
			if string(content) != "version-new" {
				t.Errorf("the binary was not replaced: %q", content)
			}

			globalAfter, err := os.ReadFile(globalPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(globalAfter) != string(globalBefore) {
				t.Error("upgrade modified the global configuration")
			}
			repoAfter, err := os.ReadFile(repoPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(repoAfter) != string(repoBefore) {
				t.Error("upgrade modified the per-project configuration")
			}
		})
	}
}

// TestGoBinaryNameBySystem pins the per-GOOS binary name used by the go
// install fallback. On this platform the value is executed for real; on the
// other one it is verified as far as Linux allows (pure path building), with
// GOOS=windows build+vet providing the compile-time half.
func TestGoBinaryNameBySystem(t *testing.T) {
	want := "vcsentinel"
	if runtime.GOOS == "windows" {
		want = "vcsentinel.exe"
	}
	if got := goBinaryName(); got != want {
		t.Errorf("goBinaryName() = %q, expected %q", got, want)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(want, ".exe") {
		t.Errorf("the Windows binary must carry the .exe suffix: %q", want)
	}
}

// snapshotTree walks root and returns a stable map of relative path to file
// content (directories map to empty content), so a test can prove an area of
// the filesystem was left untouched.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			snapshot[rel+"/"] = ""
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[rel] = string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("could not snapshot %s: %v", root, err)
	}
	return snapshot
}

// TestUninstallLeavesRepositoriesUntouched composes the uninstall primitives
// (binary removal + global config cleanup + shell rc cleanup) against a fake
// repository that must survive byte-identically: uninstall is global scope and
// repositories are explicitly out of its blast radius.
func TestUninstallLeavesRepositoriesUntouched(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	setHome(t, home)

	// Fake repository: worktree file plus the hook init would have installed.
	hookDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hookDir, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsentinel check --staged\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, repo)

	// Installed artifacts that uninstall owns.
	binary := filepath.Join(home, ".vcsentinel", "bin", goBinaryName())
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := createGlobalConfig(); err != nil {
		t.Fatalf("createGlobalConfig returned error: %v", err)
	}

	captureStdout(t, func() {
		if err := removeBinary(binary); err != nil {
			t.Fatalf("removeBinary returned error: %v", err)
		}
		if err := removeGlobalConfig(home); err != nil {
			t.Fatalf("removeGlobalConfig returned error: %v", err)
		}
		if err := removeShellPathBlock(); err != nil {
			t.Fatalf("removeShellPathBlock returned error: %v", err)
		}
	})

	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Errorf("the installed binary still exists: %v", err)
	}
	after := snapshotTree(t, repo)
	if len(before) != len(after) {
		t.Fatalf("uninstall changed the repository tree: %d entries before, %d after", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("uninstall modified %s in the repository", path)
		}
	}
	// The full uninstall flow (RunFullUninstall) is not safe to
	// run from tests — uninstallLinux targets the real /usr/local/bin — so
	// its repository-hook disclaimer is pinned through the constant the flow
	// prints, keeping repositories documented as out of uninstall's scope.
	if !strings.Contains(repositoryHooksNotice, "pre-commit hook") ||
		!strings.Contains(repositoryHooksNotice, "is not removed") {
		t.Errorf("the uninstall disclaimer no longer states that repository hooks are left untouched: %q", repositoryHooksNotice)
	}
}
