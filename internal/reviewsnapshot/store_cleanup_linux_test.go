//go:build linux

package reviewsnapshot

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveWithOwnerWriteRemovesNestedReadOnlyTree(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "staging")
	nested := filepath.Join(tree, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(tree, "plain.go"), filepath.Join(nested, "run.sh")} {
		if err := os.WriteFile(path, []byte("evidence\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		path string
		mode fs.FileMode
	}{
		{path: filepath.Join(tree, "plain.go"), mode: 0o400},
		{path: filepath.Join(nested, "run.sh"), mode: 0o500},
		{path: nested, mode: 0o500},
		{path: tree, mode: 0o500},
	} {
		if err := os.Chmod(tc.path, tc.mode); err != nil {
			t.Fatal(err)
		}
	}

	err := removeWithOwnerWrite(tree, func(path string) error {
		if err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o200 == 0 {
				t.Fatalf("%s was not made owner-writable before removal: mode %04o", current, info.Mode().Perm())
			}
			return nil
		}); err != nil {
			return err
		}
		return os.RemoveAll(path)
	})
	if err != nil {
		t.Fatalf("removeWithOwnerWrite: %v", err)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("read-only tree survived cleanup: %v", err)
	}
}
