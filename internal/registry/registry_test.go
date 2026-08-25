package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegisterRefreshesNameAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	registryDir := filepath.Join(root, "registry")
	mustDo(t, os.Mkdir(repository, 0755))
	mustDo(t, os.Mkdir(registryDir, 0700))
	path := filepath.Join(registryDir, "repositories.json")
	data := fmt.Sprintf(`{"version":1,"repositories":[{"path":%q,"name":"stale","enabled":false}]}`,
		filepath.ToSlash(repository))
	mustDo(t, os.WriteFile(path, []byte(data), 0600))

	store := must(Open(path))
	added, err := store.Register(repository + string(filepath.Separator))
	if err != nil || added {
		t.Fatalf("Register() = (%v, %v), want (false, nil)", added, err)
	}
	added, err = store.Register(repository)
	if err != nil || added || store.Len() != 1 {
		t.Fatalf("second Register() = (%v, %v), len = %d", added, err, store.Len())
	}
	entry := store.List()[0]
	if entry.Name != "repository" || !entry.Enabled || entry.Path != repository {
		t.Fatalf("entry = %+v, want refreshed normalized entry", entry)
	}
	persisted := must(os.ReadFile(path))
	if !strings.Contains(string(persisted), filepath.ToSlash(repository)) {
		t.Fatalf("persisted path = %q, want slash-form absolute path", persisted)
	}
	files := must(os.ReadDir(registryDir))
	if len(files) != 1 || files[0].Name() != "repositories.json" {
		t.Fatalf("registry directory contains temporary residue: %v", files)
	}
	if runtime.GOOS != "windows" {
		directoryInfo := must(os.Stat(registryDir))
		fileInfo := must(os.Stat(path))
		if directoryInfo.Mode().Perm() != 0700 || fileInfo.Mode().Perm() != 0600 {
			t.Fatalf("modes = directory %o, file %o; want 0700 and 0600", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
		}
	}
}

func TestRegistryLifecycleSortedAndMissing(t *testing.T) {
	root := t.TempDir()
	mustDo(t, os.Mkdir(filepath.Join(root, "a"), 0755))
	mustDo(t, os.Mkdir(filepath.Join(root, "b"), 0755))
	store := must(Open(filepath.Join(root, "repositories.json")))
	t.Chdir(root)
	must(store.Register("b" + string(filepath.Separator)))
	must(store.Register(filepath.Join(root, "a")))
	entries := store.List()
	if len(entries) != 2 || entries[0].Path != filepath.Join(root, "a") || entries[1].Path != filepath.Join(root, "b") {
		t.Fatalf("List() = %+v, want sorted normalized paths", entries)
	}
	if changed, err := store.SetEnabled(filepath.Join(root, "a"), false); err != nil || !changed {
		t.Fatalf("SetEnabled(false) = (%v, %v)", changed, err)
	}
	mustDo(t, os.RemoveAll(filepath.Join(root, "a")))
	if !store.List()[0].Missing() {
		t.Fatal("deleted repository should be reported as missing")
	}
	if changed, err := store.SetEnabled(filepath.Join(root, "a"), true); err != nil || !changed {
		t.Fatalf("SetEnabled() on missing repository = (%v, %v)", changed, err)
	}
	if removed, err := store.Remove(filepath.Join(root, "a")); err != nil || !removed {
		t.Fatalf("Remove() on missing repository = (%v, %v)", removed, err)
	}
}

func TestOpenRejectsCorruptAndUnknownJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"malformed", "{", "invalid JSON"},
		{"unknown root field", `{"version":1,"repositories":[],"extra":true}`, "unknown field"},
		{"unknown entry field", `{"version":1,"repositories":[{"path":"repo","extra":true}]}`, "unknown field"},
		{"unsupported version", `{"version":2,"repositories":[]}`, "invalid version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repositories.json")
			mustDo(t, os.WriteFile(path, []byte(tt.data), 0600))
			if _, err := Open(path); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Open() error = %v, want text %q", err, tt.want)
			}
		})
	}
}
