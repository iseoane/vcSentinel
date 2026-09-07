package main

import "testing"

func TestVersionLessThanOrEqual(t *testing.T) {
	tests := []struct {
		name      string
		newVer    string
		published string
		expected  bool
	}{
		{"lower greater blocks", "0.2.0", "0.3.0", true},
		{"equal blocks", "0.1.0", "0.1.0", true},
		{"higher lower allows", "0.3.0", "0.2.0", false},
		{"lower minor blocks", "0.1.5", "0.2.0", true},
		{"equal minor blocks", "0.1.0", "0.1.0", true},
		{"higher patch allows", "0.1.1", "0.1.0", false},
		{"v prefix normalizes", "v0.2.0", "0.1.0", false},
		{"published with v normalizes", "0.1.0", "v0.2.0", true},
		{"short components zero-pad", "1.2", "1.2.0", true},
		{"higher short components allow", "1.3", "1.2.9", false},
		{"non-numeric suffix counts as zero", "0.1.0-beta", "0.1.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := versionLessOrEqual(tt.newVer, tt.published)
			if got != tt.expected {
				t.Errorf("versionLessOrEqual(%q, %q) = %v, want %v", tt.newVer, tt.published, got, tt.expected)
			}
		})
	}
}
