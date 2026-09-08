//go:build linux

package reviewsnapshot

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// storeRoot returns the per-UID review snapshot store under os.TempDir(),
// which tests retarget through TMPDIR at every call. Suffixing the directory
// with the effective UID keeps different local users from sharing — and
// stomping or snooping — one evidence store, and gives the ownership check
// below a stable meaning.
func storeRoot() string {
	return filepath.Join(os.TempDir(), storeDirName+"-"+strconv.Itoa(os.Getuid()))
}

// checkStoreRoot fails closed unless the store root is a real directory: not
// a symlink at its final component, private (exactly 0700), and owned by the
// current user. A store that fails any of these checks is never leased from
// nor published into — the snapshot evidence must not be readable, writable,
// or replaceable by another local user.
func checkStoreRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect review snapshot store: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("review snapshot store %q is a symlink", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("review snapshot store %q is not a directory", root)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("review snapshot store %q is not private: mode %04o, want 0700", root, info.Mode().Perm())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("review snapshot store %q is owned by uid %d, want %d", root, stat.Uid, os.Getuid())
	}
	return nil
}
