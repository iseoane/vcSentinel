package git

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type gitChangeRecord struct {
	Status  string
	Path    string
	OldPath string
}

// CaptureDraftChanges reads the complete HEAD-to-worktree draft without
// staging, writing the index, or creating history. It includes untracked files
// and preserves both sides of a detected rename.
func CaptureDraftChanges() ([]PlannedChange, error) {
	return CaptureDraftChangesExcluding(nil)
}

// CaptureDraftChangesExcluding captures the draft after removing records whose
// current or previous path is excluded. Filtering records before inspection is
// important: an excluded transcript must not be hashed, diffed, or parsed as a
// candidate change, including when Git reports it as a rename or copy.
func CaptureDraftChangesExcluding(excludedPaths []string) ([]PlannedChange, error) {
	records, err := captureGitChangeRecords()
	if err != nil {
		return nil, err
	}
	excluded := normalizedExcludedPaths(excludedPaths)
	changes := make([]PlannedChange, 0, len(records))
	for _, record := range records {
		if excluded[normalizeGitPath(record.Path)] || (record.OldPath != "" && excluded[normalizeGitPath(record.OldPath)]) {
			continue
		}
		change, err := inspectGitChange(record)
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	sort.Slice(changes, func(i, j int) bool {
		return changeKey(changes[i].Path, changes[i].OldPath) < changeKey(changes[j].Path, changes[j].OldPath)
	})
	return changes, nil
}

func normalizedExcludedPaths(paths []string) map[string]bool {
	excluded := make(map[string]bool, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		excluded[normalizeGitPath(path)] = true
	}
	return excluded
}

// HashDraftState returns a deterministic fingerprint of every pending change,
// including the HEAD revision, index content, and worktree content. A new or
// changed path anywhere in the draft therefore makes an old plan stale.
func HashDraftState() (string, error) {
	changes, err := CaptureDraftChanges()
	if err != nil {
		return "", err
	}
	return hashPlannedChangesState(changes), nil
}

func hashDraftStateForChanges(expected []PlannedChange) (string, error) {
	current, err := CaptureDraftChanges()
	if err != nil {
		return "", err
	}
	byKey := make(map[string]PlannedChange, len(current))
	for _, change := range current {
		byKey[changeKey(change.Path, change.OldPath)] = change
	}
	selected := make([]PlannedChange, 0, len(expected))
	for _, planned := range expected {
		if change, ok := byKey[changeKey(planned.Path, planned.OldPath)]; ok {
			selected = append(selected, change)
			continue
		}
		selected = append(selected, PlannedChange{
			Path:         planned.Path,
			OldPath:      planned.OldPath,
			Status:       "missing",
			Kind:         ChangeFile,
			HeadHash:     absentFingerprint,
			IndexHash:    absentFingerprint,
			WorktreeHash: absentFingerprint,
		})
	}
	return hashPlannedChangesState(selected), nil
}

func hashPlannedChangesState(changes []PlannedChange) string {
	identity := struct {
		Head    string          `json:"head"`
		Changes []PlannedChange `json:"changes"`
	}{
		Head:    currentHeadFingerprint(),
		Changes: canonicalChanges(changes),
	}
	encoded := mustMarshal(identity)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func currentHeadFingerprint() string {
	value, err := runGitOutput("rev-parse", "--verify", "HEAD")
	if err != nil {
		return "unborn-head"
	}
	return strings.TrimSpace(value)
}

func captureGitChangeRecords() ([]gitChangeRecord, error) {
	var records []gitChangeRecord
	if currentHeadFingerprint() == "unborn-head" {
		staged, err := runGitOutput("ls-files", "--cached", "-z", "--")
		if err != nil {
			return nil, fmt.Errorf("could not list staged changes on unborn HEAD: %w", err)
		}
		for _, path := range strings.Split(strings.TrimSuffix(staged, "\x00"), "\x00") {
			if path == "" {
				continue
			}
			records = append(records, gitChangeRecord{Status: "A", Path: normalizeGitPath(path)})
		}
	} else {
		diff, err := runGitOutput("diff", "--name-status", "-z", "-M", "-C", "HEAD", "--")
		if err != nil {
			return nil, fmt.Errorf("could not list draft changes: %w", err)
		}
		records, err = parseGitNameStatus(diff)
		if err != nil {
			return nil, err
		}
	}
	status, err := runGitOutput("status", "--porcelain", "-z", "-uall", "--")
	if err != nil {
		return nil, fmt.Errorf("could not list untracked draft files: %w", err)
	}
	known := make(map[string]struct{}, len(records))
	for _, record := range records {
		known[changeKey(record.Path, record.OldPath)] = struct{}{}
	}
	for _, path := range untrackedPaths(status) {
		path = normalizeGitPath(path)
		key := changeKey(path, "")
		if _, exists := known[key]; exists {
			continue
		}
		records = append(records, gitChangeRecord{Status: "A", Path: path})
		known[key] = struct{}{}
	}
	return records, nil
}

func parseGitNameStatus(output string) ([]gitChangeRecord, error) {
	parts := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return nil, nil
	}
	var records []gitChangeRecord
	for index := 0; index < len(parts); {
		status := parts[index]
		index++
		if status == "" || index >= len(parts) {
			return nil, fmt.Errorf("invalid Git change status record %q", status)
		}
		code := status[0]
		switch code {
		case 'R', 'C':
			if index+1 >= len(parts) {
				return nil, fmt.Errorf("invalid rename record %q", status)
			}
			records = append(records, gitChangeRecord{
				Status:  string(code),
				OldPath: normalizeGitPath(parts[index]),
				Path:    normalizeGitPath(parts[index+1]),
			})
			index += 2
		case 'A', 'D', 'M', 'T':
			path := normalizeGitPath(parts[index])
			record := gitChangeRecord{Status: string(code), Path: path}
			records = append(records, record)
			index++
		default:
			return nil, fmt.Errorf("unsupported Git change status %q", status)
		}
	}
	return records, nil
}

func inspectGitChange(record gitChangeRecord) (PlannedChange, error) {
	path := normalizeGitPath(record.Path)
	oldPath := normalizeGitPath(record.OldPath)
	if oldPath == "." {
		oldPath = ""
	}
	change := PlannedChange{
		Path:         path,
		OldPath:      oldPath,
		Status:       record.Status,
		Kind:         ChangeText,
		HeadHash:     absentFingerprint,
		IndexHash:    absentFingerprint,
		WorktreeHash: absentFingerprint,
	}

	var err error
	headPath := change.Path
	if record.Status == "R" || record.Status == "C" {
		headPath = oldPath
	}
	change.HeadHash, err = hashHeadPath(headPath)
	if err != nil {
		return PlannedChange{}, err
	}
	change.IndexHash, err = hashIndexPath(change.Path)
	if err != nil {
		return PlannedChange{}, err
	}
	change.WorktreeHash, err = hashWorktreePath(change.Path)
	if err != nil {
		return PlannedChange{}, err
	}

	diff, err := diffForChange(record)
	if err != nil {
		return PlannedChange{}, err
	}
	if record.Status == "R" || record.Status == "C" {
		change.Kind = ChangeRename
		change.Atoms = []ChangeAtom{syntheticChangeAtom(change, changeAtomRename, diff, 0)}
		return change, nil
	}

	addedLines, binary, err := numstatForChange(record)
	if err != nil {
		return PlannedChange{}, err
	}
	if binary {
		change.Kind = ChangeBinary
		change.AddedLines = addedLines
		change.Atoms = []ChangeAtom{syntheticChangeAtom(change, changeAtomBinary, diff, addedLines)}
		return change, nil
	}

	hunks, err := parseDiffHunks(diff)
	if err != nil {
		return PlannedChange{}, fmt.Errorf("could not parse diff for %s: %w", change.Path, err)
	}
	if len(hunks) == 0 {
		change.Kind = ChangeFile
		change.AddedLines = addedLines
		change.Atoms = []ChangeAtom{syntheticChangeAtom(change, changeAtomFile, diff, addedLines)}
		return change, nil
	}
	change.AddedLines = addedLines
	change.Atoms = make([]ChangeAtom, 0, len(hunks))
	for index, hunk := range hunks {
		atom := ChangeAtom{
			Index:      index,
			Kind:       changeAtomHunk,
			Hunk:       &hunk,
			Digest:     hunk.PatchHash,
			AddedLines: hunk.NewLines,
		}
		atom.ID = atomID(change, atom)
		change.Atoms = append(change.Atoms, atom)
	}
	if actual := sumAtomLines(change.Atoms); actual != change.AddedLines {
		return PlannedChange{}, fmt.Errorf("diff line count for %s is inconsistent: numstat=%d hunks=%d", change.Path, change.AddedLines, actual)
	}
	return change, nil
}

const absentFingerprint = "absent"

func syntheticChangeAtom(change PlannedChange, kind, diff string, addedLines int) ChangeAtom {
	digest := sha256.Sum256([]byte(diff))
	atom := ChangeAtom{
		Index:      0,
		Kind:       kind,
		Digest:     hex.EncodeToString(digest[:]),
		AddedLines: addedLines,
	}
	atom.ID = atomID(change, atom)
	change.AddedLines = addedLines
	return atom
}

func sumAtomLines(atoms []ChangeAtom) int {
	result := 0
	for _, atom := range atoms {
		result += atom.AddedLines
	}
	return result
}

func diffForChange(record gitChangeRecord) (string, error) {
	if record.Status == "A" && record.OldPath == "" {
		return runGitDiffNoIndex("--unified=0", filepath.ToSlash(os.DevNull), filepath.ToSlash(record.Path))
	}
	paths := []string{record.Path}
	if record.OldPath != "" {
		paths = []string{record.OldPath, record.Path}
	}
	args := []string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--unified=0", "HEAD", "--"}
	for _, path := range paths {
		args = append(args, literalPathspec(path))
	}
	return runGitOutput(args...)
}

func numstatForChange(record gitChangeRecord) (added int, binary bool, err error) {
	var output string
	if record.Status == "A" && record.OldPath == "" {
		output, err = runGitDiffNoIndex("--numstat", filepath.ToSlash(os.DevNull), filepath.ToSlash(record.Path))
	} else {
		paths := []string{record.Path}
		if record.OldPath != "" {
			paths = []string{record.OldPath, record.Path}
		}
		args := []string{"diff", "--numstat", "--no-renames", "HEAD", "--"}
		for _, path := range paths {
			args = append(args, literalPathspec(path))
		}
		output, err = runGitOutput(args...)
	}
	if err != nil {
		return 0, false, err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		if len(fields) < 3 {
			continue
		}
		if fields[0] == "-" || fields[1] == "-" {
			return 0, true, nil
		}
		value, parseErr := strconv.Atoi(fields[0])
		if parseErr == nil {
			added += value
		}
	}
	return added, false, nil
}

func parseDiffHunks(diff string) ([]DiffHunk, error) {
	var hunks []DiffHunk
	var raw strings.Builder
	active := false
	flush := func() {
		if !active {
			return
		}
		digest := sha256.Sum256([]byte(raw.String()))
		hunks[len(hunks)-1].PatchHash = hex.EncodeToString(digest[:])
		raw.Reset()
		active = false
	}

	for _, segment := range strings.SplitAfter(diff, "\n") {
		line := strings.TrimSuffix(strings.TrimSuffix(segment, "\n"), "\r")
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			flush()
			hunk, err := parseUnifiedHunkHeader(line)
			if err != nil {
				return nil, err
			}
			hunks = append(hunks, hunk)
			raw.WriteString(segment)
			active = true
			continue
		}
		if active {
			raw.WriteString(segment)
		}
	}
	flush()
	return hunks, nil
}

func parseUnifiedHunkHeader(header string) (DiffHunk, error) {
	fields := strings.Fields(header)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" && !strings.HasPrefix(fields[3], "@@") {
		return DiffHunk{}, fmt.Errorf("invalid hunk header %q", header)
	}
	if !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return DiffHunk{}, fmt.Errorf("invalid hunk ranges in %q", header)
	}
	oldStart, oldLines, err := parseHunkField(fields[1][1:])
	if err != nil {
		return DiffHunk{}, err
	}
	newStart, newLines, err := parseHunkField(fields[2][1:])
	if err != nil {
		return DiffHunk{}, err
	}
	return DiffHunk{OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}, nil
}

func hashHeadPath(path string) (string, error) {
	if path == "" {
		return absentFingerprint, nil
	}
	if currentHeadFingerprint() == "unborn-head" {
		return absentFingerprint, nil
	}
	output, err := runGitOutput("ls-tree", "-z", "HEAD", "--", literalPathspec(path))
	if err != nil {
		return "", fmt.Errorf("could not read HEAD content for %s: %w", path, err)
	}
	if output == "" {
		return absentFingerprint, nil
	}
	entry := strings.SplitN(strings.TrimSuffix(output, "\x00"), "\t", 2)[0]
	fields := strings.Fields(entry)
	if len(fields) < 3 {
		return "", fmt.Errorf("invalid HEAD tree entry for %s", path)
	}
	return fields[1], nil
}

func hashIndexPath(path string) (string, error) {
	if path == "" {
		return absentFingerprint, nil
	}
	output, err := runGitOutput("ls-files", "--stage", "-z", "--", literalPathspec(path))
	if err != nil {
		return "", fmt.Errorf("could not read index content for %s: %w", path, err)
	}
	entries := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(entries) == 1 && entries[0] == "" {
		return absentFingerprint, nil
	}
	if len(entries) != 1 {
		return "", fmt.Errorf("unsupported conflicted index state for %s", path)
	}
	fields := strings.Fields(strings.SplitN(entries[0], "\t", 2)[0])
	if len(fields) < 3 {
		return "", fmt.Errorf("invalid index entry for %s", path)
	}
	return fields[1], nil
}

func hashWorktreePath(path string) (string, error) {
	if path == "" {
		return absentFingerprint, nil
	}
	if _, err := os.Lstat(filepath.FromSlash(path)); err != nil {
		if os.IsNotExist(err) {
			return absentFingerprint, nil
		}
		return "", fmt.Errorf("could not inspect worktree path %s: %w", path, err)
	}
	output, err := runGitOutput("hash-object", "--", filepath.ToSlash(path))
	if err != nil {
		return "", fmt.Errorf("could not hash worktree path %s: %w", path, err)
	}
	return strings.TrimSpace(output), nil
}

func literalPathspec(path string) string {
	return ":(literal)" + filepath.ToSlash(path)
}

func normalizeGitPath(path string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
}

func mustMarshal(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("cannot serialize deterministic Git state: %v", err))
	}
	return encoded
}
