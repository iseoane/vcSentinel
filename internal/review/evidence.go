package review

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

// EvidenceLog is the complete deterministic log for one Pipeline step.
type EvidenceLog struct { Step, Content string }

// WriteEvidence writes stable, repository-relative evidence logs. It never
// adds timestamps or run-local metadata: a commit-then-re-review loop must
// converge when the reviewed evidence did not change.
func WriteEvidence(worktree, branch string, logs []EvidenceLog) ([]string, error) {
	root := filepath.Join(worktree, ".vas_sentinel", "evidence", evidenceBranchSlug(branch))
	paths := make([]string, 0, len(logs))
	seen := make(map[string]struct{}, len(logs))
	for _, log := range logs {
		step := evidenceBranchSlug(log.Step)
		if step == "" { return nil, errors.New("evidence log requires a step name") }
		if _, ok := seen[step]; ok { return nil, fmt.Errorf("duplicate evidence step %q", log.Step) }
		seen[step] = struct{}{}
		if err := os.MkdirAll(root, 0755); err != nil { return nil, err }
		path := filepath.Join(root, step+".log")
		if err := os.WriteFile(path, []byte(log.Content), 0644); err != nil { return nil, err }
		paths = append(paths, filepath.ToSlash(filepath.Join(".vas_sentinel", "evidence", evidenceBranchSlug(branch), step+".log")))
	}
	return paths, nil
}

// EvidenceAtHEAD reports whether path is present at HEAD and its working-tree
// bytes match the blob at HEAD. A tracked but staged or edited path is not a
// permalink target for this review body.
func EvidenceAtHEAD(worktree, path string) (bool, string, error) {
	gitPath := filepath.ToSlash(path)
	head := exec.Command("git", "-C", worktree, "rev-parse", "--verify", "HEAD:"+gitPath)
	headOID, err := head.Output()
	if err != nil {
		return false, "not in HEAD", nil
	}
	local := exec.Command("git", "-C", worktree, "hash-object", gitPath)
	localOID, err := local.Output()
	if err != nil { return false, "", err }
	if strings.TrimSpace(string(headOID)) != strings.TrimSpace(string(localOID)) {
		return false, "differs from HEAD", nil
	}
	return true, "", nil
}

func evidenceBranchSlug(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) { b.WriteRune(r) } else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") { b.WriteByte('-') }
	}
	return strings.Trim(b.String(), "-")
}
