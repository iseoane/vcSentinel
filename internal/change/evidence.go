package change

import (
	"path/filepath"
	"strconv"
	"strings"
)

var defaultSensitivePathPatterns = []string{"**/auth/**", "**/*auth*.go", "**/security/**"}

// NewCharacteristicsInput assembles detector evidence from one change
// profile, its changed paths, a unified diff, and the repository attributes.
func NewCharacteristicsInput(profile ChangeProfile, paths []string, unifiedDiff, gitattributes string) EntradaCaracteristicas {
	normalizedPaths := make([]string, len(paths))
	for i, path := range paths {
		normalizedPaths[i] = filepath.ToSlash(path)
	}
	return EntradaCaracteristicas{
		Symbols:           profile.Symbols,
		Rutas:             normalizedPaths,
		LineasAnadidas:    addedLinesFromUnifiedDiff(unifiedDiff, normalizedPaths),
		Gitattributes:     gitattributes,
		PatronesSensibles: append([]string(nil), defaultSensitivePathPatterns...),
	}
}

func addedLinesFromUnifiedDiff(unifiedDiff string, paths []string) map[string][]string {
	allowedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		allowedPaths[filepath.ToSlash(path)] = struct{}{}
	}

	addedLines := make(map[string][]string)
	currentPath := ""
	inHunk := false
	for _, rawLine := range strings.Split(unifiedDiff, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.HasPrefix(line, "diff --git ") {
			currentPath = ""
			inHunk = false
			continue
		}
		if inHunk {
			if strings.HasPrefix(line, "+") && currentPath != "" {
				if _, allowed := allowedPaths[currentPath]; allowed {
					addedLines[currentPath] = append(addedLines[currentPath], strings.TrimPrefix(line, "+"))
				}
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			currentPath = unifiedDiffPath(strings.TrimPrefix(line, "+++ "))
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
		}
	}
	return addedLines
}

func unifiedDiffPath(rawPath string) string {
	if strings.HasPrefix(rawPath, `"`) {
		if decoded, err := strconv.Unquote(rawPath); err == nil {
			rawPath = decoded
		}
	}
	if rawPath == "/dev/null" {
		return ""
	}
	return filepath.ToSlash(strings.TrimPrefix(rawPath, "b/"))
}
