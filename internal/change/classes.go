// Package change classifies files into deterministic classes (source, test,
// config, generated, docs, infra, ci) via globs with declaration-order
// precedence. It fixes the false positive of internal/git.ClassifyLayer, which
// classifies "test" by substring in the path (e.g. "latest/x.go").
package change

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Classes recognized by the file_classes contract (see
// docs/design/replanteamiento-objetivo.md, section 8).
const (
	ClassSource    = "source"
	ClassTest      = "test"
	ClassConfig    = "config"
	ClassGenerated = "generated"
	ClassDocs      = "docs"
	ClassInfra     = "infra"
	ClassCI        = "ci"
)

// Rule associates a class with the list of globs that activate it. The order
// of the Rules in the slice IS the precedence: the first one that matches wins.
type Rule struct {
	Class    string
	Patterns []string
}

// DefaultRules are the sensible default rules (Go as the main language of the
// repo). Anything that matches none of them falls into ClassSource.
func DefaultRules() []Rule {
	return []Rule{
		{Class: ClassTest, Patterns: []string{"**/*_test.go", "**/testdata/**", "**/*.spec.ts", "**/*.spec.tsx"}},
		{Class: ClassGenerated, Patterns: []string{"**/*.pb.go", "**/*_gen.go", "**/*.lock", "go.sum"}},
		{Class: ClassInfra, Patterns: []string{"Dockerfile", "**/Dockerfile", "**/*.tf", "infra/**"}},
		{Class: ClassCI, Patterns: []string{".github/workflows/**", ".gitlab-ci.yml"}},
		{Class: ClassDocs, Patterns: []string{"**/*.md", "docs/**"}},
		{Class: ClassConfig, Patterns: []string{"**/*.json", "**/*.yaml", "**/*.yml", "**/*.toml", "go.mod"}},
	}
}

// ClassifyByPath classifies path according to rules: the first one whose glob
// matches wins (deterministic by position, not by "most specific match"). With
// no matches, the file is ClassSource, the catch-all of the contract.
func ClassifyByPath(path string, rules []Rule) string {
	normalizedPath := filepath.ToSlash(path)
	for _, rule := range rules {
		for _, pattern := range rule.Patterns {
			if matchesGlob(pattern, normalizedPath) {
				return rule.Class
			}
		}
	}
	return ClassSource
}

// isGeneratedByGitAttributes reports whether content (in .gitattributes
// format) marks path with linguist-generated: an additional signal alongside
// the globs. Only Classify uses it; it is not public package API.
func isGeneratedByGitAttributes(content, path string) bool {
	normalizedPath := filepath.ToSlash(path)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		for _, attribute := range fields[1:] {
			if attribute == "linguist-generated" || attribute == "linguist-generated=true" {
				if matchesGlob(gitattributesPattern(fields[0]), normalizedPath) {
					return true
				}
				break
			}
		}
	}
	return false
}

// gitattributesPattern adapts pattern to the .gitattributes convention
// (inherited from .gitignore): without "/" it matches at any depth, not only
// at the root.
func gitattributesPattern(pattern string) string {
	if strings.Contains(pattern, "/") {
		return pattern
	}
	return "**/" + pattern
}

// Classify combines the .gitattributes signal with the glob rules:
// linguist-generated always wins, before evaluating any path rule.
func Classify(path string, rules []Rule, gitattributes string) string {
	if isGeneratedByGitAttributes(gitattributes, path) {
		return ClassGenerated
	}
	return ClassifyByPath(path, rules)
}

// matchesGlob translates pattern ("**" of arbitrary depth) to a regex and
// evaluates path: filepath.Match does not support "**" crossing directories.
func matchesGlob(pattern, path string) bool {
	re, err := regexp.Compile(translateGlobToRegex(pattern))
	if err != nil {
		return false
	}
	return re.MatchString(path)
}

// translateGlobToRegex: "**/" = zero or more directories; bare "**" = any
// remainder; "*"/"?" do not cross "/"; the rest is escaped literally.
func translateGlobToRegex(pattern string) string {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			out.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(pattern[i:], "**"):
			out.WriteString(".*")
			i += 2
		case pattern[i] == '*':
			out.WriteString("[^/]*")
			i++
		case pattern[i] == '?':
			out.WriteString("[^/]")
			i++
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	out.WriteString("$")
	return out.String()
}
