package git

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type selectionPatch struct {
	header string
	hunks  []string
}

type selectionIndexEntry struct {
	mode   string
	object string
}

func executeSelectionPlan(plan *FragmentationPlan) ([]CommitResult, error) {
	return executeSelectionPlanWithCommit(plan, commitWithSelectionIndex)
}

func executeSelectionPlanWithCommit(plan *FragmentationPlan, commit func(string, string) (string, error)) (results []CommitResult, applyErr error) {
	originalHead := currentHeadFingerprint()
	changes := selectionChangeIndex(plan.Changes)
	patches, err := prepareSelectionPatches(plan, changes)
	if err != nil {
		return nil, err
	}
	if err := preflightSelectionPlan(plan, originalHead, changes, patches); err != nil {
		return nil, err
	}

	defer func() {
		if applyErr == nil {
			return
		}
		if cleanupErr := resetRealIndexForPlan(plan); cleanupErr != nil {
			applyErr = errors.Join(applyErr, fmt.Errorf("could not restore the real index after selection apply: %w", cleanupErr))
		}
	}()

	cumulativeHunks := make(map[string][]ChangeSelector)
	for _, batch := range plan.Batches {
		index, err := newSelectionIndex()
		if err != nil {
			return results, err
		}
		currentHead := currentHeadFingerprint()
		if err := readSelectionTree(index, currentHead); err != nil {
			cleanupSelectionIndex(index)
			return results, err
		}

		var whole []ChangeSelector
		for _, selector := range batch.Selectors {
			if selector.Mode == SelectorHunk {
				key := changeKey(selector.Path, selector.OldPath)
				cumulativeHunks[key] = append(cumulativeHunks[key], selector)
				continue
			}
			whole = append(whole, selector)
		}
		if err := stageSelectionSet(index, originalHead, changes, patches, whole, cumulativeHunks); err != nil {
			cleanupSelectionIndex(index)
			return results, err
		}

		message := batch.Message
		if strings.TrimSpace(message) == "" {
			message = batch.AutoMessage
		}
		hash, err := commit(index, message)
		cleanupSelectionIndex(index)
		if err != nil {
			return results, err
		}
		results = append(results, CommitResult{Hash: hash, Message: message, Layer: batch.Layer, Files: len(batch.Paths)})
	}
	if err := resetRealIndexForPlan(plan); err != nil {
		return results, err
	}
	return results, nil
}

func selectionChangeIndex(changes []PlannedChange) map[string]PlannedChange {
	result := make(map[string]PlannedChange, len(changes))
	for _, change := range changes {
		result[changeKey(change.Path, change.OldPath)] = change
	}
	return result
}

func prepareSelectionPatches(plan *FragmentationPlan, changes map[string]PlannedChange) (map[string]selectionPatch, error) {
	needed := make(map[string]struct{})
	for _, batch := range plan.Batches {
		for _, selector := range batch.Selectors {
			if selector.Mode == SelectorHunk {
				needed[changeKey(selector.Path, selector.OldPath)] = struct{}{}
			}
		}
	}
	patches := make(map[string]selectionPatch, len(needed))
	for key := range needed {
		change := changes[key]
		diff, err := diffForChange(gitChangeRecord{Status: change.Status, Path: change.Path, OldPath: change.OldPath})
		if err != nil {
			return nil, fmt.Errorf("could not capture selected hunk for %s: %w", change.Path, err)
		}
		patch, err := parseSelectionPatch(diff)
		if err != nil {
			return nil, fmt.Errorf("%w: %s hunk representation is unsafe: %v", ErrTreeChanged, change.Path, err)
		}
		actual, err := parseDiffHunks(diff)
		if err != nil || len(actual) != len(change.Atoms) {
			return nil, fmt.Errorf("%w: %s hunks no longer match the approved draft", ErrTreeChanged, change.Path)
		}
		for index, atom := range change.Atoms {
			if atom.Hunk == nil || actual[index] != *atom.Hunk || atom.Digest != actualPatchHash(patch.hunks[index]) {
				return nil, fmt.Errorf("%w: %s hunk %d no longer matches the approved draft", ErrTreeChanged, change.Path, index)
			}
		}
		patches[key] = patch
	}
	return patches, nil
}

func parseSelectionPatch(diff string) (selectionPatch, error) {
	parts := strings.SplitAfter(diff, "\n")
	var patch selectionPatch
	var current strings.Builder
	seenHunk := false
	flush := func() {
		if current.Len() > 0 {
			patch.hunks = append(patch.hunks, current.String())
			current.Reset()
		}
	}
	for _, part := range parts {
		line := strings.TrimSuffix(strings.TrimSuffix(part, "\n"), "\r")
		if strings.HasPrefix(line, "@@ ") {
			flush()
			seenHunk = true
			current.WriteString(part)
			continue
		}
		if seenHunk {
			current.WriteString(part)
		} else {
			patch.header += part
		}
	}
	flush()
	if patch.header == "" || len(patch.hunks) == 0 {
		return selectionPatch{}, fmt.Errorf("diff contains no applicable text hunks")
	}
	return patch, nil
}

func actualPatchHash(hunk string) string {
	digest := sha256.Sum256([]byte(hunk))
	return hex.EncodeToString(digest[:])
}

func (patch selectionPatch) forSelectors(selectors []ChangeSelector) (string, error) {
	indices := make([]int, 0, len(selectors))
	seen := make(map[int]struct{}, len(selectors))
	for _, selector := range selectors {
		if selector.HunkIndex < 0 || selector.HunkIndex >= len(patch.hunks) {
			return "", fmt.Errorf("%w: hunk index %d is outside the live diff", ErrTreeChanged, selector.HunkIndex)
		}
		if _, ok := seen[selector.HunkIndex]; ok {
			return "", fmt.Errorf("%w: hunk %d is selected more than once", ErrInvalidPlan, selector.HunkIndex)
		}
		seen[selector.HunkIndex] = struct{}{}
		indices = append(indices, selector.HunkIndex)
	}
	sort.Ints(indices)
	var result strings.Builder
	result.WriteString(patch.header)
	for _, index := range indices {
		result.WriteString(patch.hunks[index])
	}
	return result.String(), nil
}

func preflightSelectionPlan(plan *FragmentationPlan, originalHead string, changes map[string]PlannedChange, patches map[string]selectionPatch) error {
	hunks := make(map[string][]ChangeSelector)
	var whole []ChangeSelector
	for _, batch := range plan.Batches {
		for _, selector := range batch.Selectors {
			if selector.Mode == SelectorHunk {
				key := changeKey(selector.Path, selector.OldPath)
				hunks[key] = append(hunks[key], selector)
			} else {
				whole = append(whole, selector)
			}
		}
		if err := withSelectionIndex(originalHead, func(index string) error {
			return stageSelectionSet(index, originalHead, changes, patches, whole, hunks)
		}); err != nil {
			return fmt.Errorf("could not preflight batch %d: %w", batch.Number, err)
		}
	}
	return nil
}

func stageSelectionSet(index, originalHead string, changes map[string]PlannedChange, patches map[string]selectionPatch, whole []ChangeSelector, hunks map[string][]ChangeSelector) error {
	if err := stageWholeSelectors(index, whole); err != nil {
		return err
	}
	keys := make([]string, 0, len(hunks))
	for key := range hunks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		change, ok := changes[key]
		if !ok {
			return fmt.Errorf("%w: no change for selected hunk %s", ErrInvalidPlan, key)
		}
		patch, ok := patches[key]
		if !ok {
			return fmt.Errorf("%w: no patch for selected hunk %s", ErrInvalidPlan, key)
		}
		entry, exists, err := materializeHunkEntry(originalHead, change, hunks[key], patch)
		if err != nil {
			return err
		}
		if err := setSelectionIndexEntry(index, change.Path, entry, exists); err != nil {
			return err
		}
	}
	return nil
}

func stageWholeSelectors(index string, selectors []ChangeSelector) error {
	paths := make([]string, 0, len(selectors)*2)
	for _, selector := range selectors {
		paths = append(paths, selector.Path)
		if selector.OldPath != "" {
			paths = append(paths, selector.OldPath)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	args := []string{"add", "-A", "-f", "--"}
	for _, path := range paths {
		if len(args) == 4 || args[len(args)-1] != literalPathspec(path) {
			args = append(args, literalPathspec(path))
		}
	}
	return runGitWithIndex(index, nil, args...)
}

func materializeHunkEntry(originalHead string, change PlannedChange, selectors []ChangeSelector, patch selectionPatch) (selectionIndexEntry, bool, error) {
	index, err := newSelectionIndex()
	if err != nil {
		return selectionIndexEntry{}, false, err
	}
	defer cleanupSelectionIndex(index)
	if err := readSelectionTree(index, originalHead); err != nil {
		return selectionIndexEntry{}, false, err
	}
	selectedPatch, err := patch.forSelectors(selectors)
	if err != nil {
		return selectionIndexEntry{}, false, err
	}
	if err := runGitWithIndex(index, strings.NewReader(selectedPatch), "apply", "--cached", "--unidiff-zero", "--whitespace=nowarn"); err != nil {
		return selectionIndexEntry{}, false, fmt.Errorf("could not apply selected hunk from %s: %w", change.Path, err)
	}
	return readSelectionIndexEntry(index, change.Path)
}

func setSelectionIndexEntry(index, path string, entry selectionIndexEntry, exists bool) error {
	if !exists {
		current, currentExists, err := readSelectionIndexEntry(index, path)
		_ = current
		if err != nil {
			return err
		}
		if !currentExists {
			return nil
		}
		return runGitWithIndex(index, nil, "update-index", "--force-remove", "--", literalPathspec(path))
	}
	cacheInfo := entry.mode + "," + entry.object + "," + filepath.ToSlash(path)
	return runGitWithIndex(index, nil, "update-index", "--add", "--cacheinfo", cacheInfo)
}

func commitWithSelectionIndex(index, message string) (string, error) {
	if err := runGitWithIndex(index, nil, "commit", "-m", message, "--no-verify"); err != nil {
		return "", err
	}
	hash, err := runGitOutput("rev-parse", "--short", "HEAD")
	return strings.TrimSpace(hash), err
}

func resetRealIndexForPlan(plan *FragmentationPlan) error {
	paths := plannedSelectionPaths(plan)
	if len(paths) == 0 {
		return nil
	}
	args := []string{"reset", "HEAD", "--"}
	for _, path := range paths {
		args = append(args, literalPathspec(path))
	}
	if _, err := runGitOutput(args...); err != nil {
		return fmt.Errorf("could not clean the real index after selection apply: %w", err)
	}
	return nil
}

func plannedSelectionPaths(plan *FragmentationPlan) []string {
	paths := make([]string, 0, len(plan.Changes)*2)
	if len(plan.Changes) > 0 {
		for _, change := range plan.Changes {
			paths = append(paths, change.Path)
			if change.OldPath != "" {
				paths = append(paths, change.OldPath)
			}
		}
		return sortedUnique(paths)
	}

	for _, batch := range plan.Batches {
		if len(batch.Selectors) == 0 {
			paths = append(paths, batch.Paths...)
			continue
		}
		for _, selector := range batch.Selectors {
			paths = append(paths, selector.Path)
			if selector.OldPath != "" {
				paths = append(paths, selector.OldPath)
			}
		}
	}
	return sortedUnique(paths)
}

func withSelectionIndex(head string, action func(string) error) error {
	index, err := newSelectionIndex()
	if err != nil {
		return err
	}
	defer cleanupSelectionIndex(index)
	if err := readSelectionTree(index, head); err != nil {
		return err
	}
	return action(index)
}

func newSelectionIndex() (string, error) {
	file, err := os.CreateTemp("", "vas-sentinel-selection-index-")
	if err != nil {
		return "", fmt.Errorf("could not create temporary Git index: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("could not close temporary Git index: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("could not prepare temporary Git index: %w", err)
	}
	return path, nil
}

func cleanupSelectionIndex(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + ".lock")
}

func readSelectionTree(index, head string) error {
	if head == "unborn-head" {
		return runGitWithIndex(index, nil, "read-tree", "--empty")
	}
	return runGitWithIndex(index, nil, "read-tree", head)
}

func runGitWithIndex(index string, stdin io.Reader, args ...string) error {
	_, err := runGitWithIndexOutput(index, stdin, args...)
	return err
}

func runGitWithIndexOutput(index string, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = selectionIndexEnvironment(index)
	cmd.Stdin = stdin
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func selectionIndexEnvironment(index string) []string {
	values := os.Environ()
	env := make([]string, 0, len(values)+1)
	for _, value := range values {
		if !strings.HasPrefix(value, "GIT_INDEX_FILE=") {
			env = append(env, value)
		}
	}
	return append(env, "GIT_INDEX_FILE="+filepath.ToSlash(index))
}

func readSelectionIndexEntry(index, path string) (selectionIndexEntry, bool, error) {
	output, err := runGitWithIndexOutput(index, nil, "ls-files", "--stage", "-z", "--", literalPathspec(path))
	if err != nil {
		return selectionIndexEntry{}, false, err
	}
	entries := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(entries) == 1 && entries[0] == "" {
		return selectionIndexEntry{}, false, nil
	}
	if len(entries) != 1 {
		return selectionIndexEntry{}, false, fmt.Errorf("unsupported conflicted temporary index state for %s", path)
	}
	fields := strings.Fields(strings.SplitN(entries[0], "\t", 2)[0])
	if len(fields) < 3 || fields[2] != "0" {
		return selectionIndexEntry{}, false, fmt.Errorf("unsupported temporary index entry for %s", path)
	}
	return selectionIndexEntry{mode: fields[0], object: fields[1]}, true, nil
}
