package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/validation"
)

// outputLocationPattern recognizes the "path:line[:column]:" prefix already
// used by go vet, go build, and the Go compiler itself in their error output.
var outputLocationPattern = regexp.MustCompile(`^(\S+\.\w+):(\d+)(?::\d+)?:`)

// bareFilePathPattern recognizes a line that is only a file path, with no
// other text: the format of `gofmt -l`, which lists badly formatted files
// without indicating a line.
var bareFilePathPattern = regexp.MustCompile(`^(\S+\.\w+)$`)

// dimensionByCapability maps the known validation capabilities to the
// equivalent semantic dimension they may supersede (T6.2). Deliberately
// conservative: a capability missing from this map (any unknown one, or
// "unit_test"/"build", whose evidence comes from running code of the branch
// itself and is therefore more manipulable than the static output of
// gofmt/go vet) never produces a finding with a Dimension assigned, and
// review.SupersedeDeterministicFindings never supersedes with a deterministic
// finding without Dimension: better not to supersede than to supersede
// unrelated findings.
var dimensionByCapability = map[string]string{
	"format": review.DimStyle,
	"lint":   review.DimStyle,
}

// projectValidationFindings converts deterministic validation findings
// (lint/build/failed test) into review.Finding{Source: SourceValidation}, so
// AuditarCommit can apply the T6.2 supersede over the equivalent semantic
// finding. Each Evidence line with a recognizable "file:line" prefix produces
// a finding with that exact location; a line that is only a file path (gofmt
// -l) produces a finding without a line, which covers the whole file. If no
// line is recognizable, a single finding without location is kept instead of
// silently discarding the evidence.
func projectValidationFindings(findings []validation.Finding) []review.Finding {
	var projected []review.Finding
	for _, f := range findings {
		locations := evidenceLocations(f.Evidence)
		if len(locations) == 0 {
			projected = append(projected, newDeterministicFinding(f, "", 0, strings.TrimSpace(f.Evidence)))
			continue
		}
		for _, l := range locations {
			projected = append(projected, newDeterministicFinding(f, l.file, l.line, l.evidence))
		}
	}
	return projected
}

type evidenceLocation struct {
	file     string
	line     int
	evidence string
}

func evidenceLocations(evidence string) []evidenceLocation {
	var locations []evidenceLocation
	seenFiles := make(map[string]bool)
	for _, line := range strings.Split(evidence, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := outputLocationPattern.FindStringSubmatch(line); m != nil {
			if n, err := strconv.Atoi(m[2]); err == nil {
				locations = append(locations, evidenceLocation{file: m[1], line: n, evidence: line})
				continue
			}
		}
		if m := bareFilePathPattern.FindStringSubmatch(line); m != nil && !seenFiles[m[1]] {
			seenFiles[m[1]] = true
			locations = append(locations, evidenceLocation{file: m[1], evidence: line})
		}
	}
	return locations
}

func newDeterministicFinding(h validation.Finding, file string, line int, evidence string) review.Finding {
	finding := review.Finding{
		Source:      review.SourceValidation,
		Dimension:   dimensionByCapability[h.Capability],
		Severity:    h.Severity,
		Title:       h.Capability,
		Description: fmt.Sprintf("%s: %s", h.Capability, h.Command),
		Evidence:    evidence,
		Confidence:  1.0,
		Status:      review.StatusConfirmed,
		Fixable:     review.FixableManual,
		Location:    review.Location{File: file, LineStart: line},
	}
	finding.Fingerprint = review.Fingerprint(finding)
	return finding
}
