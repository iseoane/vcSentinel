package review

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
func WriteEvidence(worktree, branch string, logs []EvidenceLog) ([]string, error) {
	dir := evidenceDirName(branch)
	relativeRoot := filepath.Join(".vas_sentinel", "evidence", dir)
	root := filepath.Join(worktree, relativeRoot)
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
		if err := os.MkdirAll(root, 0755); err != nil {
			return nil, err
		}
		if err := refuseSymlink(root); err != nil {
			return nil, err
		}
		path := filepath.Join(root, step+".log")
		if err := refuseSymlink(path); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(log.Content), 0644); err != nil {
			return nil, err
		}
		paths = append(paths, filepath.ToSlash(filepath.Join(relativeRoot, step+".log")))
	}
	return paths, nil
}

// refuseSymlink rejects a destination that is a symbolic link, instead of
// following it. The evidence directory lives inside the reviewed worktree, so
// its contents are attacker-controlled in the only threat model that matters
// here: a repository can ship a symlink at the evidence directory or at
// <step>.log and make the review process create or truncate a file anywhere
// the reviewing user can write. Lstat does not follow the link, so this sees
// the link itself. A path that does not exist yet is fine — that is the
// ordinary case.
func refuseSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write evidence through the symbolic link %s", path)
	}
	return nil
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
