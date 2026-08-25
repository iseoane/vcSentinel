package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustOrPanic[T any](value T, err error) T {
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

func require(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
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

	store := mustOrPanic(Open(path))
	added, err := store.Register(repository + string(filepath.Separator))
	require(t, err == nil && !added, "Register() = (%v, %v), want (false, nil)", added, err)
	added, err = store.Register(repository)
	require(t, err == nil && !added && len(store.List()) == 1, "second Register() = (%v, %v), len = %d", added, err, len(store.List()))
	entry := store.List()[0]
	require(t, entry.Name == "repository" && entry.Enabled && entry.Path == repository && (runtime.GOOS != "windows" || entry.Path == mustOrPanic(normalizePath(filepath.Join(root, "REPOSITORY")))), "entry = %+v, want refreshed normalized entry", entry)
	persisted := mustOrPanic(os.ReadFile(path))
	require(t, strings.Contains(string(persisted), filepath.ToSlash(repository)), "persisted path = %q, want slash-form absolute path", persisted)
	files := mustOrPanic(os.ReadDir(registryDir))
	require(t, len(files) == 1 && files[0].Name() == "repositories.json", "registry directory contains temporary residue: %v", files)
	if runtime.GOOS != "windows" {
		directoryInfo := mustOrPanic(os.Stat(registryDir))
		fileInfo := mustOrPanic(os.Stat(path))
		require(t, directoryInfo.Mode().Perm() == 0700 && fileInfo.Mode().Perm() == 0600, "modes = directory %o, file %o; want 0700 and 0600", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestRegistryLifecycleSortedAndMissing(t *testing.T) {
	root := t.TempDir()
	mustDo(t, os.Mkdir(filepath.Join(root, "a"), 0755))
	mustDo(t, os.Mkdir(filepath.Join(root, "b"), 0755))
	store := mustOrPanic(Open(filepath.Join(root, "repositories.json")))
	t.Chdir(root)
	mustOrPanic(store.Register("b" + string(filepath.Separator)))
	mustOrPanic(store.Register(filepath.Join(root, "a")))
	entries := store.List()
	require(t, len(entries) == 2 && entries[0].Path == filepath.Join(root, "a") && entries[1].Path == filepath.Join(root, "b"), "List() = %+v, want sorted normalized paths", entries)
	changed, err := store.SetEnabled(filepath.Join(root, "a"), false)
	require(t, err == nil && changed, "SetEnabled(false) = (%v, %v)", changed, err)
	mustDo(t, os.RemoveAll(filepath.Join(root, "a")))
	require(t, store.List()[0].Missing(), "deleted repository should be reported as missing")
	changed, err = store.SetEnabled(filepath.Join(root, "a"), true)
	require(t, err == nil && changed, "SetEnabled() on missing repository = (%v, %v)", changed, err)
	removed, err := store.Remove(filepath.Join(root, "a"))
	require(t, err == nil && removed, "Remove() on missing repository = (%v, %v)", removed, err)
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
			_, err := Open(path)
			require(t, err != nil && strings.Contains(err.Error(), tt.want), "Open() error = %v, want text %q", err, tt.want)
		})
	}
}
