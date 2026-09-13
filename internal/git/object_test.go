package git

import (
	"strings"
	"testing"
)

func TestIsValidGitObjectIDRequiresCanonicalSHA(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "SHA-1", value: strings.Repeat("a", 40), want: true},
		{name: "SHA-256", value: strings.Repeat("b", 64), want: true},
		{name: "leading whitespace", value: " " + strings.Repeat("a", 40), want: false},
		{name: "trailing whitespace", value: strings.Repeat("a", 40) + "\n", want: false},
		{name: "uppercase", value: strings.Repeat("A", 40), want: false},
		{name: "invalid character", value: strings.Repeat("a", 39) + "g", want: false},
		{name: "wrong length", value: strings.Repeat("a", 39), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidGitObjectID(tc.value); got != tc.want {
				t.Fatalf("IsValidGitObjectID(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
