//go:build windows

package reviewsnapshot

import (
	"io/fs"
	"testing"
)

func TestPublishedPermissionsAcceptWindowsReadOnlyModes(t *testing.T) {
	for _, gitMode := range []string{"100644", "100755"} {
		want := publishedPerm(gitMode)
		if !publishedPermMatches(0o444, want) {
			t.Fatalf("publishedPermMatches(0444, %04o) = false, want true", want)
		}
		if publishedPermMatches(0o666, want) {
			t.Fatalf("publishedPermMatches(0666, %04o) = true, want false", want)
		}
	}
}
