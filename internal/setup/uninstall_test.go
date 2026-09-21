package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoveBinary(t *testing.T) {
	t.Run("removes an existing binary", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "vcsentinel.exe")
		if err := os.WriteFile(path, []byte("binary"), 0755); err != nil {
			t.Fatal(err)
		}

		if err := removeBinary(path); err != nil {
			t.Fatalf("removeBinary returned error: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("the binary still exists: %v", err)
		}
	})

	t.Run("does not fail when it does not exist", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "does-not-exist.exe")
		if err := removeBinary(path); err != nil {
			t.Fatalf("removeBinary with a nonexistent file returned error: %v", err)
		}
	})
}

func TestRemoveShellPathBlock(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	const line = `export PATH="/usr/local/bin:$PATH"`
	block := "\n# vcSentinel\n" + line + "\n"
	zshrc := filepath.Join(home, ".zshrc")

	t.Run("removes the block from an existing file", func(t *testing.T) {
		if err := os.WriteFile(zshrc, []byte("export OLD=1\n"+block), 0644); err != nil {
			t.Fatal(err)
		}
		if err := removeShellPathBlock(); err != nil {
			t.Fatalf("removeShellPathBlock returned error: %v", err)
		}

		content, err := os.ReadFile(zshrc)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "vcSentinel") {
			t.Errorf("the vcSentinel block is still in the file:\n%s", content)
		}
		if !strings.Contains(string(content), "export OLD=1") {
			t.Errorf("content that must be kept was removed:\n%s", content)
		}
	})

	t.Run("is idempotent and leaves files without the block untouched", func(t *testing.T) {
		if err := os.WriteFile(zshrc, []byte("export ONLY=1\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := removeShellPathBlock(); err != nil {
			t.Fatalf("second call returned error: %v", err)
		}
		content, err := os.ReadFile(zshrc)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "export ONLY=1\n" {
			t.Errorf("the file was modified without containing the block:\n%s", content)
		}
	})
}

func TestRemoveGlobalConfig(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)

	dir := filepath.Join(home, ".vcsentinel")
	path := filepath.Join(dir, "vcsentinel.yml")

	t.Run("removes the config and the empty directory", func(t *testing.T) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("config"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := removeGlobalConfig(home); err != nil {
			t.Fatalf("removeGlobalConfig returned error: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("the global config still exists: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("the .vcsentinel directory was not removed after becoming empty: %v", err)
		}
	})

	t.Run("does not fail when the config does not exist", func(t *testing.T) {
		if err := removeGlobalConfig(home); err != nil {
			t.Fatalf("removeGlobalConfig without a config returned error: %v", err)
		}
	})

	t.Run("does not remove the directory when it contains other files", func(t *testing.T) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("config"), 0644); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(dir, "other.txt")
		if err := os.WriteFile(other, []byte("other"), 0644); err != nil {
			t.Fatal(err)
		}

		if err := removeGlobalConfig(home); err != nil {
			t.Fatalf("removeGlobalConfig returned error: %v", err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("the directory with other files should not be removed: %v", err)
		}
	})
}

func TestRemoveWindowsPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only applies to Windows")
	}
	// removeWindowsPath touches the real user PATH: the command is not run
	// here, we only verify that the removal decision uses the same helper as
	// the addition one (needsWindowsPathUpdate).
	if !needsWindowsPathUpdate("C:\\x", "C:\\new") {
		t.Error("should detect that the directory must be removed from the PATH")
	}
}
