package consent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func tempRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Fatal(err)
	}
	return repo
}

func useHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func createSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("the platform does not allow creating symlinks: %v", err)
		}
		t.Fatal(err)
	}
}

func TestExternalDiffConsentFS(t *testing.T) {
	t.Run("grant is idempotent and revocable", func(t *testing.T) {
		repo := tempRepo(t)
		first, err := GrantExternalDiff(repo)
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(first.Path)
		if err != nil {
			t.Fatal(err)
		}
		second, err := GrantExternalDiff(repo)
		if err != nil || second.GrantedAt != first.GrantedAt {
			t.Fatalf("second grant = %+v, err = %v", second, err)
		}
		after, _ := os.ReadFile(first.Path)
		if string(after) != string(content) {
			t.Fatal("the idempotent grant replaced the existing file")
		}
		for path, mode := range map[string]os.FileMode{filepath.Dir(first.Path): 0700, first.Path: 0600} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != mode {
				t.Fatalf("mode of %s = %v", path, info.Mode())
			}
		}
		if err := RevokeExternalDiff(repo); err != nil {
			t.Fatal(err)
		}
		if state, err := ExternalDiffStatus(repo); err != nil || state.Granted {
			t.Fatalf("revoked state = %+v, err = %v", state, err)
		}
	})

	t.Run("corrupt JSON fails closed and is not replaced", func(t *testing.T) {
		repo := tempRepo(t)
		state, _ := stateFor(repo)
		if err := os.MkdirAll(filepath.Dir(state.Path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(state.Path, []byte("{corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ExternalDiffStatus(repo); err == nil {
			t.Fatal("corrupt JSON counted as consent")
		}
		if _, err := GrantExternalDiff(repo); err == nil {
			t.Fatal("the grant replaced tampered JSON")
		}
		content, _ := os.ReadFile(state.Path)
		if string(content) != "{corrupt" {
			t.Fatal("the tampered JSON was overwritten")
		}
	})

	t.Run("valid but tampered JSON does not count as consent", func(t *testing.T) {
		repo := tempRepo(t)
		state, err := GrantExternalDiff(repo)
		if err != nil {
			t.Fatal(err)
		}
		content, _ := os.ReadFile(state.Path)
		content = append(content, '\n')
		if err := os.WriteFile(state.Path, content, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ExternalDiffStatus(repo); err == nil {
			t.Fatal("non-canonical JSON counted as consent")
		}
		if _, err := GrantExternalDiff(repo); err == nil {
			t.Fatal("the grant replaced non-canonical JSON")
		}
	})

	t.Run("isolates user and common-dir", func(t *testing.T) {
		repoA, repoB := tempRepo(t), tempRepo(t)
		useHome(t, filepath.Join(t.TempDir(), "user-a"))
		a, err := GrantExternalDiff(repoA)
		if err != nil {
			t.Fatal(err)
		}
		b, err := GrantExternalDiff(repoB)
		if err != nil {
			t.Fatal(err)
		}
		useHome(t, filepath.Join(t.TempDir(), "user-b"))
		other, err := GrantExternalDiff(repoA)
		if err != nil {
			t.Fatal(err)
		}
		if a.Path == b.Path || a.Repository == b.Repository || a.Path == other.Path || a.User == other.User {
			t.Fatalf("scopes are not isolated: a=%+v b=%+v other=%+v", a, b, other)
		}
	})
}

func TestExternalDiffConsentRejectsSymlinks(t *testing.T) {
	t.Run("pre-created target", func(t *testing.T) {
		repo := tempRepo(t)
		state, _ := stateFor(repo)
		if err := os.MkdirAll(filepath.Dir(state.Path), 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("intact"), 0600); err != nil {
			t.Fatal(err)
		}
		createSymlink(t, target, state.Path)
		if _, err := GrantExternalDiff(repo); err == nil {
			t.Fatal("the grant followed the destination symlink")
		}
		content, _ := os.ReadFile(target)
		if string(content) != "intact" {
			t.Fatal("the symlink target was overwritten")
		}
	})

	t.Run("app-owned component", func(t *testing.T) {
		repo := tempRepo(t)
		state, _ := stateFor(repo)
		external := t.TempDir()
		createSymlink(t, external, filepath.Dir(filepath.Dir(state.Path)))
		if _, err := GrantExternalDiff(repo); err == nil {
			t.Fatal("the grant followed a linked app-owned component")
		}
		entries, _ := os.ReadDir(external)
		if len(entries) != 0 {
			t.Fatal("wrote outside the consent subtree")
		}
	})
}
