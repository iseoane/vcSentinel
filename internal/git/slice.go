package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentadapter"
)

const (
	// Both derive from ReviewableLinesLimit (thresholds.go): a batch and an
	// isolated configuration file are measured with the same yardstick as the
	// guardian, and recalibrating the guardian must drag them along.
	batchLinesLimit  = ReviewableLinesLimit
	giantConfigLimit = ReviewableLinesLimit
	// The micro-diff is sent to an external process: it bounds both each new
	// file and the total result before materializing its full content.
	microDiffByteLimit = 1 << 20

	isolatedDepsMessage = "chore(deps): track lock and auto-generated files"
	isolatedDocsMessage = "docs(slice): isolate extensive document %s"
	giantBypassMessage  = "chore(slice): bypass AI for massive file %s"
)

var layerOrder = []string{"config", "backend", "frontend", "test"}

// ModifiedFile represents a file with pending changes to slice.
type ModifiedFile struct {
	Path  string
	Lines int
	Layer string
}

// GetModifiedFiles returns the files with changes relative to HEAD,
// including the untracked ones, with their added lines and their layer.
func GetModifiedFiles() ([]ModifiedFile, error) {
	tracked, err := trackedFiles()
	if err != nil {
		return nil, err
	}
	untracked, err := untrackedFiles()
	if err != nil {
		return nil, err
	}
	return append(tracked, untracked...), nil
}

// trackedFiles reads the tracked changes (modified and staged) with numstat.
func trackedFiles() ([]ModifiedFile, error) {
	output, err := runGitOutput("diff", "HEAD", "--numstat")
	if err != nil {
		return nil, err
	}

	return parseNumstat(output), nil
}

// parseNumstat interprets the output of "git diff --numstat", which separates
// added/deleted/path with tabs. It splits on the first two tabs (SplitN)
// instead of on spaces, so a path with spaces stays intact in the third
// field. For renames, that field is not a usable path as-is:
// destinationPath reduces it to the destination path, the only one that
// exists in the worktree and that "git add" accepts.
func parseNumstat(output string) []ModifiedFile {
	var result []ModifiedFile
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		addCount, err := strconv.Atoi(fields[0])
		if err != nil {
			continue // Ignores binaries marked with "-"
		}
		path := destinationPath(fields[2])
		result = append(result, ModifiedFile{Path: path, Lines: addCount, Layer: ClassifyLayer(path)})
	}
	return result
}

// destinationPath reduces the third numstat field to the destination path
// when it describes a rename. Git emits renames in two forms:
//
//   - flat: "old file.go => new file.go" — the whole string is not a path,
//     only the right half is.
//   - abbreviated with braces: "dir/{old => sub1/new}.go" — only the stretch
//     between braces changes; it must be replaced by its right half while
//     keeping the common prefix and suffix.
//
// If the field does not describe a rename, it is returned as-is.
func destinationPath(field string) string {
	if opening := strings.Index(field, "{"); opening != -1 {
		if closingRelative := strings.Index(field[opening:], "}"); closingRelative != -1 {
			closing := opening + closingRelative
			content := field[opening+1 : closing]
			if parts := strings.SplitN(content, " => ", 2); len(parts) == 2 {
				return field[:opening] + parts[1] + field[closing+1:]
			}
		}
	}
	if parts := strings.SplitN(field, " => ", 2); len(parts) == 2 {
		return parts[1]
	}
	return field
}

// untrackedFiles detects the new files (??) and counts their physical lines.
// It uses "--porcelain -z" instead of "--short": with "--short", git quotes
// any path with spaces or special characters and escapes the non-ASCII ones
// (e.g. an accented letter) with octal sequences, which broke both the
// counting (nonexistent file) and slice's later "git add".
// "-z" separates the records with NUL and emits the paths raw, without
// quotes or escapes: it removes the whole class of errors instead of
// patching one case.
func untrackedFiles() ([]ModifiedFile, error) {
	output, err := runGitOutput("status", "--porcelain", "-z", "-uall")
	if err != nil {
		return nil, err
	}

	var result []ModifiedFile
	for _, path := range untrackedPaths(output) {
		lines, err := countPhysicalLines(path)
		if err != nil {
			return nil, fmt.Errorf("could not count the lines of %s: %w", path, err)
		}
		result = append(result, ModifiedFile{Path: path, Lines: lines, Layer: ClassifyLayer(path)})
	}
	return result, nil
}

// untrackedPaths extracts the paths of the untracked files ("??") from the
// output of "git status --porcelain -z -uall". Each record is separated by
// NUL; the untracked ones have a single record with the "??" prefix.
// Renames of tracked files generate an additional record without that
// prefix (the old path), which is ignored like any other state that is not
// "??".
func untrackedPaths(output string) []string {
	var paths []string
	for _, record := range strings.Split(output, "\x00") {
		path, isUntracked := strings.CutPrefix(record, "?? ")
		if !isUntracked {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// runGitOutput runs git and returns the full standard output.
func runGitOutput(args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// countPhysicalLines counts the lines of a file without depending on git,
// tolerant to very long lines and to files without a trailing newline.
func countPhysicalLines(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	lines := 0
	hasContent := false
	endsWithNewline := false
	buffer := make([]byte, 32*1024)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			hasContent = true
			endsWithNewline = buffer[n-1] == '\n'
			for i := 0; i < n; i++ {
				if buffer[i] == '\n' {
					lines++
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if hasContent && !endsWithNewline {
		lines++
	}
	return lines, nil
}

// ClassifyLayer determines the layer of a file according to its path and
// extension.
//
// It classifies by substring (e.g. "test" anywhere in the path), which
// produces the false positives documented in F3 (latest/x.go, contest.go).
// internal/change.ClassifyByPath (T3.1) fixes this with explicit globs, but
// it coexists with this function until T3.7: its three consumers (grouping
// slice batches, choosing review dimensions, computing the record bucket)
// migrate in separate tasks (T3.7, F5-T5.2, F2-T2.6 already closed for the
// third one), not in one jump. Do not remove this function or redirect it
// here without closing that migration: see docs/issues/decisions.md (FU-10,
// F3 outcome), T3.1.
func ClassifyLayer(path string) string {
	ext := filepath.Ext(path)
	base := filepath.Base(path)
	pathLower := strings.ToLower(path)

	if strings.Contains(pathLower, "test") || strings.Contains(base, "spec") {
		return "test"
	} else if strings.Contains(pathLower, "frontend") || ext == ".tsx" || ext == ".jsx" || ext == ".css" || ext == ".scss" {
		return "frontend"
	} else if ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".toml" || ext == ".lock" || ext == ".sum" || base == "requirements.txt" {
		return "config"
	}
	return "backend"
}

// isOversizedConfig tells whether a configuration file exceeds the isolation
// limit.
func isOversizedConfig(f ModifiedFile) bool {
	return f.Layer == "config" && f.Lines > giantConfigLimit
}

// isExtensiveDocumentation tells whether a document exceeds the isolation
// limit. It is isolated into its own batch like the configuration, but it
// NEVER goes through the massive-code branch: proposing an SRP split plan
// over prose makes no sense at all.
func isExtensiveDocumentation(f ModifiedFile) bool {
	return FileClass(f.Path) == ClassDocs && f.Lines > giantConfigLimit
}

// isGiantCode tells whether a source file exceeds the interactive bypass
// limit. It only applies to code: documentation is isolated with
// isExtensiveDocumentation and generated content is never mixed with code
// because the plan builder groups by class first, so neither of the two
// needs to offer a refactor.
func isGiantCode(f ModifiedFile) bool {
	switch FileClass(f.Path) {
	case ClassDocs, ClassGenerated, ClassConfig:
		return false
	}
	return f.Layer != "config" && f.Lines > GiantCodeLimit
}

type batchWithLayer struct {
	Layer string
	Paths []string
}

func buildBatchSequence(byLayers map[string][]ModifiedFile) []batchWithLayer {
	var sequence []batchWithLayer

	for _, layer := range layerOrder {
		for _, batch := range buildBatches(byLayers[layer]) {
			paths := make([]string, 0, len(batch))
			for _, f := range batch {
				paths = append(paths, f.Path)
			}
			sequence = append(sequence, batchWithLayer{Layer: layer, Paths: paths})
		}
	}
	return sequence
}

func buildBatches(files []ModifiedFile) [][]ModifiedFile {
	var batches [][]ModifiedFile
	var currentBatch []ModifiedFile
	accumulatedLines := 0

	for _, f := range files {
		if accumulatedLines+f.Lines > batchLinesLimit && len(currentBatch) > 0 {
			batches = append(batches, currentBatch)
			currentBatch = nil
			accumulatedLines = 0
		}
		currentBatch = append(currentBatch, f)
		accumulatedLines += f.Lines
	}

	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}
	return batches
}

// getMessageWithDiff prefers the adapter with diff layerbility
// (agentadapter.AdapterWithDiff); if the extraction of the pending diff
// fails or the adapter does not implement it, it uses the base
// agentadapter.AgentAdapter interface.
func getMessageWithDiff(paths []string, layer string, number int, adapter agentadapter.AgentAdapter) (string, error) {
	if adapterWithDiff, ok := adapter.(agentadapter.AdapterWithDiff); ok {
		diff, err := pendingDiffForPaths(paths)
		if err == nil {
			return adapterWithDiff.GetCommitMessageWithDiff(paths, layer, number, diff)
		}
	}
	return adapter.GetCommitMessage(paths, layer, number)
}

// pendingDiffForPaths returns the diff of the given files relative to HEAD
// and incorporates untracked files without staging or modifying the index.
func pendingDiffForPaths(paths []string) (string, error) {
	args := append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, paths...)
	tracked, err := runGitOutput(args...)
	if err != nil {
		return "", err
	}
	if len(tracked) > microDiffByteLimit {
		return "", fmt.Errorf("the tracked diff exceeds the limit of %d bytes for the micro-diff", microDiffByteLimit)
	}

	args = append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, paths...)
	output, err := runGitOutput(args...)
	if err != nil {
		return "", err
	}
	untracked := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(untracked) == 1 && untracked[0] == "" {
		return tracked, nil
	}
	sort.Strings(untracked)

	var diff strings.Builder
	diff.WriteString(tracked)
	for _, path := range untracked {
		info, err := os.Lstat(filepath.FromSlash(path))
		if err != nil {
			return "", fmt.Errorf("could not read the untracked file %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("the untracked file %s is not a regular file", path)
		}
		if info.Size() > microDiffByteLimit {
			return "", fmt.Errorf("the untracked file %s exceeds the limit of %d bytes for the micro-diff", path, microDiffByteLimit)
		}

		numstat, err := runGitDiffNoIndex("--numstat", os.DevNull, path)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(numstat, "-\t-\t") {
			return "", fmt.Errorf("the untracked file %s is binary; its content will not be sent", path)
		}
		patch, err := runGitDiffNoIndex("--patch", os.DevNull, path)
		if err != nil {
			return "", err
		}
		if diff.Len()+len(patch) > microDiffByteLimit {
			return "", fmt.Errorf("the micro-diff exceeds the limit of %d bytes", microDiffByteLimit)
		}
		diff.WriteString(patch)
	}
	return diff.String(), nil
}

func runGitDiffNoIndex(format, from, to string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", format, "--", from, to)
	output, err := cmd.Output()
	if err == nil {
		return string(output), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return string(output), nil
	}
	detail := ""
	if exitErr != nil {
		detail = strings.TrimSpace(string(exitErr.Stderr))
	}
	return "", fmt.Errorf("git diff --no-index failed for %s: %w: %s", to, err, detail)
}
