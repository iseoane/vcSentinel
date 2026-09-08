//go:build windows

package reviewsnapshot

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// storeRoot returns the shared review snapshot store under os.TempDir(),
// which tests retarget through TMPDIR at every call. On Windows os.TempDir
// resolves to the current user's private temp location, which carries the
// per-user isolation the per-UID suffix provides on Linux.
func storeRoot() string {
	return filepath.Join(os.TempDir(), storeDirName)
}

// checkStoreRoot fails closed unless the store root is a real directory and
// not a symlink or reparse point — Go surfaces both as ModeSymlink. Mode and
// ownership bits are not meaningfully portable here; the per-user temp
// location carries the isolation and the non-writable evidence files are
// enforced at publish time through the POSIX emulation Go provides.
func checkStoreRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect review snapshot store: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("review snapshot store %q is a symlink or reparse point", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("review snapshot store %q is not a directory", root)
	}
	return nil
}
