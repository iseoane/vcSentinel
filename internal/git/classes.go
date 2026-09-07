package git

import (
	"path/filepath"
	"strings"
)

// File classes. The class describes WHAT a file is; the layer (ClassifyLayer)
// only decides the order in which slice batches come out. They are distinct
// axes and must not be confused: an .md is class docs, and the layer calls it
// "backend" because it falls into ClassifyLayer's default case.
const (
	ClassSource    = "source"
	ClassTest      = "test"
	ClassConfig    = "config"
	ClassGenerated = "generated"
	ClassDocs      = "docs"
)

// FileClass determines the class of a file by fixed precedence:
//
//	generated > test > documentation > configuration > code
//
// The precedence matters: a .pb.go is generated even though it is .go, and a
// testdata/config.yml is test even though it is .yml. Unlike ClassifyLayer,
// it does not classify by loose substring: "latest/" and "contest.go" are
// code, not tests.
func FileClass(path string) string {
	normalized := filepath.ToSlash(path)
	base := filepath.Base(normalized)
	ext := strings.ToLower(filepath.Ext(base))

	switch {
	case isGenerated(base, ext):
		return ClassGenerated
	case isTest(base, normalized):
		return ClassTest
	case isDocumentation(normalized, ext):
		return ClassDocs
	case isConfiguration(base, ext):
		return ClassConfig
	default:
		return ClassSource
	}
}

// CountsTowardVolume tells whether a class stops the guardian. The guardian
// measures code reviewability, not bytes: documentation and generated files
// are reported separately but do not block. Nobody reviews a 2000-line lock
// file line by line, and blocking someone for writing documentation is
// friction with no safety in return.
func CountsTowardVolume(class string) bool {
	return class != ClassDocs && class != ClassGenerated
}

// isGenerated recognizes what a tool produces and nobody edits by hand.
func isGenerated(base, ext string) bool {
	if ext == ".lock" {
		return true
	}
	if base == "go.sum" {
		return true
	}
	if strings.HasSuffix(base, "-lock.json") || strings.HasSuffix(base, "-lock.yaml") {
		return true
	}
	if strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_gen.go") ||
		strings.HasSuffix(base, ".gen.go") {
		return true
	}
	return strings.Contains(base, ".generated.")
}

// isTest recognizes tests by file suffix or by dedicated directory, never by
// the loose "test" substring: that is exactly ClassifyLayer's defect that
// makes "latest/version.go" pass for a test.
func isTest(base, normalized string) bool {
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.Contains(base, ".spec.") || strings.Contains(base, ".test.") {
		return true
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "testdata" || segment == "__tests__" || segment == "__mocks__" {
			return true
		}
	}
	return false
}

// isDocumentation recognizes prose: by extension or by living under docs/.
func isDocumentation(normalized, ext string) bool {
	switch ext {
	case ".md", ".markdown", ".rst", ".adoc":
		return true
	}
	return strings.HasPrefix(normalized, "docs/") || strings.Contains(normalized, "/docs/")
}

// isConfiguration recognizes hand-written configuration. It does not include
// generated content, which was already filtered earlier: a docker-compose.yml
// is code that runs and does count toward the volume; a package-lock.json
// does not.
func isConfiguration(base, ext string) bool {
	switch ext {
	case ".json", ".yaml", ".yml", ".toml", ".ini", ".mod", ".cfg", ".conf", ".properties":
		return true
	}
	switch base {
	case "requirements.txt", "Dockerfile", "Makefile", ".gitattributes", ".gitignore":
		return true
	}
	return false
}
