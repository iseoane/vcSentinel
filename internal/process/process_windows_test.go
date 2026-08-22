//go:build windows

// Compile-time assertions for the Windows ownership seam. Windows cannot be
// integration-tested from Debian CI, so this file only proves the platform
// implementation satisfies the portable contract at build time; behavior is
// exercised by the Linux harnesses and by manual Windows verification.
package process

import (
	"os"
	"testing"
)

var (
	_ Owner        = NewOwner(OwnerConfig{})
	_ platformTree = (*windowsTree)(nil)
)

// TestWindowsOwnerSatisfiesInterface asserts the concrete owner returned by
// the platform constructor implements the tiny ownership contract, and that
// the tree payload methods keep their signatures.
func TestWindowsOwnerSatisfiesInterface(t *testing.T) {
	var owner Owner = NewOwner(OwnerConfig{})
	var tree platformTree = &windowsTree{}
	var _ func(*os.Process) error = tree.attach
	var _ func(pid int) error = tree.terminate
	var _ func(pid int) bool = tree.alive
	if owner == nil {
		t.Fatal("NewOwner() = nil on windows")
	}
}
