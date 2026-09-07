package git

import (
	"strings"
)

// PendingVolume separates the lines that stop the guardian from the ones
// that are only reported. The distinction is the core of the rule: the
// guardian exists so code stays reviewable, and documentation and generated
// files are not reviewed line by line.
type PendingVolume struct {
	// Blocking holds the lines of code, tests and hand-written configuration.
	// It is the number that decides SMALL / OPTIMAL_POINT / CRITICAL.
	Blocking int
	// Informational holds the lines of documentation and generated files.
	// They are shown so the user knows how much is pending, but they never
	// trigger the brake.
	Informational int
	State         string
	// Paths contains the measured candidate paths for scoped follow-up analysis.
	// It is intentionally omitted from serialized reports.
	Paths []string `json:"-"`
}

// MeasureVolume measures the pending volume of the worktree reusing
// GetModifiedFiles, which already includes both tracked files (git diff
// HEAD) and untracked ones (git status --uall). This way check and slice
// share a single source of truth about "pending lines", split by file
// class.
func MeasureVolume() (PendingVolume, error) {
	files, err := GetModifiedFiles()
	if err != nil {
		return PendingVolume{State: "ERROR"}, err
	}

	var volume PendingVolume
	for _, file := range files {
		if CountsTowardVolume(FileClass(file.Path)) {
			volume.Blocking += file.Lines
			continue
		}
		volume.Informational += file.Lines
	}
	volume.State = classifyState(volume.Blocking)
	return volume, nil
}

// CheckDiffLimits returns the BLOCKING volume and its state. It is kept as
// the shortcut for callers that only need the verdict; whoever wants to show
// the informational lines too uses MeasureVolume.
func CheckDiffLimits() (int, string, error) {
	volume, err := MeasureVolume()
	if err != nil {
		return 0, "ERROR", err
	}
	return volume.Blocking, volume.State, nil
}

func countAddedLines(diff string) int {
	count := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			count++
		}
	}
	return count
}

// classifyState translates ADDED code lines into the guardian's verdict. It
// compares against the thresholds in thresholds.go, not against literals:
// before T0.4 the 400 was hand-written here and could diverge from the one
// fragmentation and the PR-chain decision use (B4).
func classifyState(lines int) string {
	switch {
	case lines >= OptimalPointThreshold && lines <= ReviewableLinesLimit:
		return "OPTIMAL_POINT"
	case lines > ReviewableLinesLimit:
		return "CRITICAL"
	default:
		return "SMALL"
	}
}
