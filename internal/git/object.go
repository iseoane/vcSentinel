package git

import "strings"

// IsValidGitObjectID reports whether value is a canonical lower-case SHA-1 or
// SHA-256 object ID. Surrounding whitespace is rejected so callers cannot
// validate one identity while persisting or comparing another.
func IsValidGitObjectID(value string) bool {
	if value != strings.TrimSpace(value) {
		return false
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
