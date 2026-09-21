package main

import (
	"regexp"
	"strings"
)

// markedVolumeRulesPattern recognizes the block delimited by
// markerBegin/markerEnd regardless of the inner text (wording differs between
// versions) and regardless of the line-ending mix. It is the B12 fix: before
// this, init/uninit compared against the exact literal text of volumeRules,
// so a wording change between versions left orphan blocks that uninit could
// not remove and made init inject a second block instead of recognizing the
// existing one.
var markedVolumeRulesPattern = regexp.MustCompile(markedBlockPattern())

// legacyVolumeRulesPattern recognizes the EXACT block (without markers) that
// versions before the markers injected. It is kept only to detect and
// remove/migrate those existing blocks; no version from this one on ever
// writes this format again.
var legacyVolumeRulesPattern = regexp.MustCompile(blockPattern(legacyVolumeRules))

func markedBlockPattern() string {
	header := blockPattern("\n" + markerBegin + "\n")
	tail := blockPattern("\n" + markerEnd + "\n")
	return header + `(?s:.*?)` + tail
}

func blockPattern(block string) string {
	lines := strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n")
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		parts = append(parts, regexp.QuoteMeta(line))
	}
	if len(parts) > 0 && parts[0] == "" {
		// The block starts with a newline (every one this file handles does).
		// That newline separates the block from the previous content, but if
		// the block is the first thing in the file there is nothing before it
		// to precede it: old init versions even wrote it that way in new
		// files (e.g. this repo's own .claudecode.md, with two copies of the
		// legacy block glued from byte 0). Always requiring "\r?\n" before it
		// left that first copy unrecognized and, therefore, impossible to
		// remove or migrate.
		return `(?:\A|\r?\n)` + strings.Join(parts[1:], "\r?\n")
	}
	return strings.Join(parts, "\r?\n")
}

// containsVolumeRules reports whether the file already carries the block,
// marked (from this version or a future one sharing the markers) or legacy
// (from a version before the markers), whatever the mix of line endings it
// was written with.
func containsVolumeRules(content string) bool {
	return markedVolumeRulesPattern.MatchString(content) || legacyVolumeRulesPattern.MatchString(content)
}

// removeVolumeRules removes ALL occurrences of the block, marked or legacy,
// and leaves the rest of the file byte for byte as it was, including its line
// endings.
func removeVolumeRules(content string) string {
	content = removeAllMatches(markedVolumeRulesPattern, content)
	return removeAllMatches(legacyVolumeRulesPattern, content)
}

// removeAllMatches applies the pattern until the result stops changing (fixed
// point). A single ReplaceAllString pass is not enough when two copies of the
// block are glued together with a single separating newline (the real case of
// .claudecode.md, written by a binary so old that it did not even prepend its
// own newline): the first copy consumes that single "\r?\n" as its own ending,
// and the second copy no longer has a newline in front of it for its own
// pattern to recognize in the same pass. Repeating until the fixed point
// solves it: after removing the first copy, the second sits at the start of
// the resulting text and the pattern's "\A" branch recognizes it on the next
// round.
func removeAllMatches(pattern *regexp.Regexp, content string) string {
	for {
		next := pattern.ReplaceAllString(content, "")
		if next == content {
			return content
		}
		content = next
	}
}

// volumeRulesFor adapts the block to the line ending that already dominates
// in the target file. Always writing it in LF is what used to seed the mixed
// files that broke the comparison.
func volumeRulesFor(content string) string {
	if dominantLineEnding(content) == "\r\n" {
		return strings.ReplaceAll(volumeRules, "\n", "\r\n")
	}
	return volumeRules
}

// dominantLineEnding returns "\r\n" only when the file uses CRLF more often
// than bare LF. An empty file or one without line breaks is treated as LF,
// which is the constant's format and Debian's.
func dominantLineEnding(content string) string {
	crlf := strings.Count(content, "\r\n")
	lf := strings.Count(content, "\n") - crlf
	if crlf > lf {
		return "\r\n"
	}
	return "\n"
}
