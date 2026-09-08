//go:build linux

package reviewsnapshot

import (
	"io/fs"
	"testing"
)

func TestPublishedPermissionsUseStrictPOSIXModes(t *testing.T) {
	cases := []struct {
		name    string
		gitMode string
		want    fs.FileMode
	}{
		{name: "regular file", gitMode: "100644", want: 0o400},
		{name: "executable file", gitMode: "100755", want: 0o500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := publishedPerm(tc.gitMode); got != tc.want {
				t.Fatalf("publishedPerm(%q) = %04o, want %04o", tc.gitMode, got, tc.want)
			}
			if !publishedPermMatches(tc.want, publishedPerm(tc.gitMode)) {
				t.Fatalf("publishedPermMatches(%04o, %04o) = false, want true", tc.want, publishedPerm(tc.gitMode))
			}
			if publishedPermMatches(0o444, publishedPerm(tc.gitMode)) {
				t.Fatalf("publishedPermMatches(0444, %04o) = true, want false", publishedPerm(tc.gitMode))
			}
		})
	}
}
