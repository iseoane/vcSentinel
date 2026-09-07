package git

// StagedFiles returns only paths present in the Git index diff.
// Unlike GetModifiedFiles, it never includes unstaged or untracked
// worktree content.
func StagedFiles() ([]ModifiedFile, error) {
	output, err := runGitOutput("diff", "--cached", "--numstat", "--")
	if err != nil {
		return nil, err
	}
	return parseNumstat(output), nil
}

// MeasureStagedVolume measures the authored additions in the pending commit
// candidate represented by the index. An empty index is a successful zero
// measurement.
func MeasureStagedVolume() (PendingVolume, error) {
	files, err := StagedFiles()
	if err != nil {
		return PendingVolume{State: "ERROR"}, err
	}

	var volume PendingVolume
	volume.Paths = make([]string, 0, len(files))
	for _, file := range files {
		volume.Paths = append(volume.Paths, file.Path)
		if CountsTowardVolume(FileClass(file.Path)) {
			volume.Blocking += file.Lines
			continue
		}
		volume.Informational += file.Lines
	}
	volume.State = classifyState(volume.Blocking)
	return volume, nil
}

// CheckStagedDiffLimits returns the blocking volume and state for the index
// diff, preserving the short-form contract used by worktree measurements.
func CheckStagedDiffLimits() (int, string, error) {
	volume, err := MeasureStagedVolume()
	if err != nil {
		return 0, "ERROR", err
	}
	return volume.Blocking, volume.State, nil
}
