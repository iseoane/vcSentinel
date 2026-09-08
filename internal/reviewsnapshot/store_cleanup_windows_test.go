//go:build windows

package reviewsnapshot

import (
	"io/fs"
	"testing"
)

func TestRemovalModeUsesWindowsWritableRepresentation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      fs.FileMode
		directory bool
	}{
		{name: "read-only file", mode: 0o400},
		{name: "executable file", mode: 0o500},
		{name: "read-only directory", mode: 0o500, directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := removalMode(tc.mode, tc.directory); got != 0o600 {
				t.Fatalf("removalMode(%04o, %t) = %04o, want 0600", tc.mode, tc.directory, got)
			}
		})
	}
}
