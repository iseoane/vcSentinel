package review

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// EvidenceLog is the complete deterministic log for one Pipeline step.
type EvidenceLog struct{ Step, Content string }

// evidenceDirName is the directory one branch's logs live in. It is the slug
// for a human reading the tree, plus a short hash of the EXACT branch name so
// two branches can never share a directory. The slug alone is lossy:
// "feature/a" and "feature-a" collapse onto it, as does every branch made only
// of punctuation, and the second branch to be reviewed would then overwrite
// the first one's logs with no error. This is the same identity rule
// store.PRReviewKey applies to the entry itself, for the same reason.
func evidenceDirName(branch string) string {
	identity := sha256.Sum256([]byte(branch))
	return evidenceSlug(branch) + "-" + hex.EncodeToString(identity[:6])
}

// WriteEvidence writes stable, repository-relative evidence logs. It never
// adds timestamps or run-local metadata: a commit-then-re-review loop must
// converge when the reviewed evidence did not change.
//
// Every path operation goes through os.Root, which confines it to the
// worktree. That is the whole defence and it has to be, because the evidence
// directory lives inside the tree under review: a repository can ship a
// symlink at `.vas_sentinel`, at `.vas_sentinel/evidence`, or at the log file
// itself. Checking only the endpoints with Lstat does not help — MkdirAll
// follows a symlinked ancestor first, and by the time the endpoint is
// inspected it is a real directory at the attacker's target. os.Root refuses
// traversal that escapes the root at every component, and resolves each one
// under the same handle, so there is also no window between the check and the
// write.
func WriteEvidence(worktree, branch string, logs []EvidenceLog) ([]string, error) {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	dir := evidenceDirName(branch)
	relativeRoot := path.Join(".vas_sentinel", "evidence", dir)
	paths := make([]string, 0, len(logs))
	seen := make(map[string]struct{}, len(logs))
	for _, log := range logs {
		step := evidenceSlug(log.Step)
		if step == "" {
			return nil, errors.New("evidence log requires a step name")
		}
		if _, ok := seen[step]; ok {
			return nil, fmt.Errorf("duplicate evidence step %q", log.Step)
		}
		seen[step] = struct{}{}
		if err := root.MkdirAll(relativeRoot, 0755); err != nil {
			return nil, err
		}
		relativePath := path.Join(relativeRoot, step+".log")
		if err := refuseSymlink(root, relativePath); err != nil {
			return nil, err
		}
		if err := writeEvidenceFile(root, relativePath, log.Content); err != nil {
			return nil, err
		}
		paths = append(paths, relativePath)
	}
	return paths, nil
}

// refuseSymlink rejects a log path that already exists as a symbolic link.
// os.Root already stops a link that escapes the worktree; this stops one that
// stays inside it, which would otherwise let the reviewed tree choose which of
// its own files the review truncates. A path that does not exist yet is the
// ordinary case and is fine.
func refuseSymlink(root *os.Root, relativePath string) error {
	info, err := root.Lstat(relativePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write evidence through the symbolic link %s", relativePath)
	}
	return nil
}

func writeEvidenceFile(root *os.Root, relativePath, content string) error {
	file, err := root.OpenFile(relativePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// EvidenceAtHEAD reports whether path is present at HEAD and its working-tree
// bytes match the blob at HEAD. A tracked but staged or edited path is not a
// permalink target for this review body.
//
// It distinguishes two things the previous version merged: "this path is not
// in HEAD", which is a legitimate answer about the repository, and "the git
// command failed", which is an operational failure and must reach the caller.
// Treating a broken worktree, a missing git, or an unreadable object as
// "not in HEAD" reported a fact about the repository that was never
// established.
func EvidenceAtHEAD(worktree, path string) (bool, string, error) {
	gitPath := filepath.ToSlash(path)
	headOID, absent, err := revParseBlob(worktree, "HEAD:"+gitPath)
	if err != nil {
		return false, "", err
	}
	if absent {
		return false, "not in HEAD", nil
	}
	local := exec.Command("git", "-C", worktree, "hash-object", gitPath)
	localOID, err := local.Output()
	if err != nil {
		return false, "", fmt.Errorf("could not hash %s: %w", gitPath, err)
	}
	if headOID != strings.TrimSpace(string(localOID)) {
		return false, "differs from HEAD", nil
	}
	return true, "", nil
}

// revParseBlob resolves one revision to its object id. It reports absent only
// for the specific failure that means "this revision does not name an object";
// git signals that with exit status 128 and a message on stderr, while an
// unusable repository or a missing binary fails differently. Anything it
// cannot classify is returned as an error rather than guessed.
func revParseBlob(worktree, revision string) (oid string, absent bool, err error) {
	command := exec.Command("git", "-C", worktree, "rev-parse", "--verify", "--quiet", revision)
	output, err := command.Output()
	if err == nil {
		return strings.TrimSpace(string(output)), false, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		// --quiet turns "not a valid object name" into exit 1 with no output.
		return "", true, nil
	}
	return "", false, fmt.Errorf("could not resolve %s: %w", revision, err)
}

// evidenceSlug renders a value as a readable path segment. It is never the
// identity of anything on its own: see evidenceDirName.
func evidenceSlug(value string) string {
	var b strings.Builder
	previousDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			previousDash = false
			continue
		}
		if !previousDash && b.Len() > 0 {
			b.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
