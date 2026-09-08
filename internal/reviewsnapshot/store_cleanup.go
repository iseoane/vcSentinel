package reviewsnapshot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// removeReadOnlyStoreEntry restores owner write access before deleting a store
// entry. Published files are deliberately read-only, and Windows will not
// remove them until that attribute is cleared.
func removeReadOnlyStoreEntry(path string) error {
	return removeWithOwnerWrite(path, os.RemoveAll)
}

func removeWithOwnerWrite(path string, remove func(string) error) error {
	if err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil
		}
		if err := os.Chmod(current, removalMode(info.Mode(), info.IsDir())); err != nil {
			return err
		}
		return nil
		// An entry that vanished under us is already removed, which is what
		// os.RemoveAll reports too. filepath.WalkDir does not share that
		// tolerance: it surfaces the lstat failure for a missing root, so it
		// is absorbed here rather than reported as a cleanup failure. Two
		// callers count a nil result as one reaped entry.
	}); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("restore owner write access for review snapshot %q: %w", path, err)
	}
	return remove(path)
}
