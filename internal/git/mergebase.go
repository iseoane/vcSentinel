package git

import (
	"fmt"
	"strconv"
	"strings"
)

// MergeBase returns the nearest common ancestor of two revisions. Without a
// common ancestor it is an explicit error: there is no base to analyze.
func MergeBase(a, b string) (string, error) {
	out, err := runGitOutput("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("no common base between %q and %q: %w", a, b, err)
	}
	return strings.TrimSpace(out), nil
}

// RangeNumstat sums the added and deleted lines of the diff between two
// revisions (from..to) to measure a branch's real volume. Binary files
// (no count in the numstat) are ignored.
func RangeNumstat(from, to string) (int, error) {
	out, err := runGitOutput("diff", "--numstat", from+".."+to)
	if err != nil {
		return 0, fmt.Errorf("could not measure the volume of %s..%s: %v", from, to, err)
	}

	total := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// Binaries arrive as "-" in both columns: they add no lines.
		if fields[0] == "-" || fields[1] == "-" {
			continue
		}
		a, errA := strconv.Atoi(fields[0])
		b, errB := strconv.Atoi(fields[1])
		if errA != nil || errB != nil {
			continue
		}
		total += a + b
	}
	return total, nil
}
