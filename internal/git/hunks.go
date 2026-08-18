package git

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LineRange is a line span in the "after" content's own numbering, touched
// by one diff hunk.
type LineRange struct {
	Start int
	End   int
}

// TouchedRanges diffs before and after as arbitrary in-memory text (neither
// has to be committed or tracked) and returns, in the after content's line
// numbering, the ranges touched by each hunk. It writes both to temp files
// and reuses ejecutarGitDiffNoIndex the same way the micro-diff path in
// slice.go does, so two arbitrary strings can be compared without touching
// the git index.
func TouchedRanges(before, after string) ([]LineRange, error) {
	beforeFile, err := writeTempDiffFile(before)
	if err != nil {
		return nil, err
	}
	defer os.Remove(beforeFile)

	afterFile, err := writeTempDiffFile(after)
	if err != nil {
		return nil, err
	}
	defer os.Remove(afterFile)

	diff, err := ejecutarGitDiffNoIndex("--unified=0", beforeFile, afterFile)
	if err != nil {
		return nil, fmt.Errorf("git: could not diff the two contents: %w", err)
	}
	return parseHunkRanges(diff)
}

// writeTempDiffFile writes content to a fresh temp file and returns its path.
func writeTempDiffFile(content string) (string, error) {
	f, err := os.CreateTemp("", "sentinel-diffguard-*")
	if err != nil {
		return "", fmt.Errorf("git: could not create a temp file for the diff: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("git: could not write the temp file for the diff: %w", err)
	}
	return f.Name(), nil
}

// parseHunkRanges extracts, from a --unified=0 diff, the "+" side of every
// "@@ ... @@" hunk header as a LineRange in the after content's numbering.
func parseHunkRanges(diff string) ([]LineRange, error) {
	var ranges []LineRange
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		fields := strings.Fields(line)
		var plusField string
		for _, field := range fields {
			if strings.HasPrefix(field, "+") {
				plusField = field
				break
			}
		}
		if plusField == "" {
			return nil, fmt.Errorf("git: could not find the '+' field in hunk header %q", line)
		}
		start, count, err := parseHunkField(plusField[1:])
		if err != nil {
			return nil, fmt.Errorf("git: could not parse hunk header %q: %w", line, err)
		}
		if count == 0 {
			ranges = append(ranges, LineRange{Start: start, End: start})
			continue
		}
		ranges = append(ranges, LineRange{Start: start, End: start + count - 1})
	}
	return ranges, nil
}

// parseHunkField parses one "start[,count]" hunk field. count defaults to 1
// when git omits the comma (a single-line hunk).
func parseHunkField(field string) (start, count int, err error) {
	parts := strings.SplitN(field, ",", 2)
	start, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start %q: %w", parts[0], err)
	}
	if len(parts) == 1 {
		return start, 1, nil
	}
	count, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid count %q: %w", parts[1], err)
	}
	return start, count, nil
}
