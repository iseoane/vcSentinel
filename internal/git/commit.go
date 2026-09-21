package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/intent"
)

// CommitMessage returns the first line of a commit's message.
func CommitMessage(sha string) (string, error) {
	out, err := runGitOutput("log", "-1", "--format=%s", sha)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitIntent reads the complete commit message with Git's %B placeholder and
// delegates trailer parsing to internal/intent. A commit without a complete
// recognized pair returns the zero intent, not an inferred source.
func CommitIntent(sha string) (intent.Intent, error) {
	out, err := runGitOutput("show", "-s", "--format=%B", sha)
	if err != nil {
		return intent.Intent{}, err
	}
	return intent.Parse(out), nil
}

// stableDiffPrefixes pins the diff header prefixes. This is not cosmetic: the
// review planner parses this output to extract the added lines and
// recognizes the path by its "b/" prefix. diff.noprefix, diff.mnemonicPrefix
// and diff.srcPrefix/dstPrefix change that format, and a header the parser
// does not recognize loses its lines silently, leaving the detectors that
// read content blind. Forcing them here keeps the user's configuration from
// reintroducing the divergence FU-10 records.
var stableDiffPrefixes = []string{"--src-prefix=a/", "--dst-prefix=b/"}

// DiffCommit returns the full diff of a commit (without the message),
// including new, modified, and renamed files. It also works for the
// repository's first commit (root commit).
// For a merge commit (two or more parents) it diffs against the first
// parent: `git show` emits a combined diff that stays empty on clean
// merges, and an audit reading that diff would see the merge as touching
// nothing.
func DiffCommit(sha string) (string, error) {
	isMerge, err := isMergeCommit(sha)
	if err != nil {
		return "", err
	}
	var out string
	if isMerge {
		args := append([]string{"diff", "--no-color"}, stableDiffPrefixes...)
		out, err = runGitOutput(append(args, sha+"^1", sha, "--")...)
	} else {
		args := append([]string{"show", "--format=", "--no-color"}, stableDiffPrefixes...)
		out, err = runGitOutput(append(args, sha, "--")...)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// isMergeCommit reports whether the SHA points at a commit with more than one
// parent. `rev-list --parents -n 1` prints "sha parent [parent...]", so three
// or more fields betray a merge.
func isMergeCommit(sha string) (bool, error) {
	out, err := runGitOutput("rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return false, err
	}
	return len(strings.Fields(out)) > 2, nil
}

// RangeDiff returns the diff of base..head with the same stable prefixes as
// DiffCommit, for callers that reason over the diff output instead of over
// a commit.
func RangeDiff(base, head string) (string, error) {
	args := append([]string{"diff", "--no-color"}, stableDiffPrefixes...)
	out, err := runGitOutput(append(args, base+".."+head, "--")...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// SHAHead returns the full SHA of the HEAD commit in the process repository.
// Call SHAHeadFrom when the caller owns an explicit worktree.
func SHAHead() (string, error) {
	out, err := runGitOutput("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// SHAHeadFrom returns the full SHA of HEAD for the supplied worktree. Using an
// explicit path is important for commands that can operate on linked or foreign
// worktrees: ambient Git state must never select another repository.
func SHAHeadFrom(worktree string) (string, error) {
	out, err := GitInIsolated(worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// RangeSHAs returns the SHAs from "from" (exclusive) to "to" (inclusive),
// in chronological order. For the first commit of a branch an empty SHA is
// used as "from".
func RangeSHAs(from, to string) ([]string, error) {
	revision := from + ".." + to
	if from == "" {
		revision = to
	}
	out, err := runGitOutput("rev-list", "--reverse", revision)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		if sha := strings.TrimSpace(line); sha != "" {
			shas = append(shas, sha)
		}
	}
	return shas, nil
}

// UpToSHAs returns the SHAs of every commit reachable from the expression,
// oldest to newest. Useful for --all: auditing the whole pending history.
func UpToSHAs(expression string) ([]string, error) {
	out, err := runGitOutput("rev-list", "--reverse", expression)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		if sha := strings.TrimSpace(line); sha != "" {
			shas = append(shas, sha)
		}
	}
	return shas, nil
}

// CurrentBranch returns the (short) name of the current branch in the process
// repository. Call CurrentBranchFrom when the caller owns an explicit worktree.
func CurrentBranch() (string, error) {
	out, err := runGitOutput("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentBranchFrom returns the exact branch name for the supplied worktree.
func CurrentBranchFrom(worktree string) (string, error) {
	out, err := GitInIsolated(worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// FilesOfCommit returns the paths of the files a commit touches, useful for
// inferring the audit's layer.
//
// Like DiffCommit, on a merge it lists the diff against the first parent:
// `git show --name-only` also emits an empty list on clean merges.
func FilesOfCommit(sha string) ([]string, error) {
	isMerge, err := isMergeCommit(sha)
	if err != nil {
		return nil, err
	}
	var out string
	if isMerge {
		out, err = runGitOutput("diff", "--name-only", sha+"^1", sha, "--")
	} else {
		out, err = runGitOutput("show", "--name-only", "--format=", sha, "--")
	}
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if path := strings.TrimSpace(line); path != "" {
			files = append(files, path)
		}
	}
	return files, nil
}

// FileContentAtCommit returns the exact content of a file as it existed in
// a specific commit ("git show <sha>:<file>"). If the file does not exist
// in that commit (renamed, deleted, misspelled by the auditing agent), it
// returns an explicit error instead of a silent empty string: the caller
// needs to distinguish "empty file" from "unresolved file" to decide
// whether a finding is valid.
func FileContentAtCommit(sha, file string) (string, error) {
	// filepath.ToSlash: git always expects "/" in a <rev>:<path> pathspec,
	// even when the file arrives with Windows separators (the project's
	// cross-platform rule: never concatenate paths into git without
	// normalizing first).
	out, err := runGitOutput("show", sha+":"+filepath.ToSlash(file))
	if err != nil {
		return "", fmt.Errorf("could not read %q in commit %q: %w", file, sha, err)
	}
	return out, nil
}

// BlobFileAtCommit returns the blob hash (git object) of a file's content as
// it existed in a specific commit, via "git rev-parse <sha>:<path>" (that
// form already resolves directly to the blob, without needing the "^{blob}"
// suffix). It is the key that survives a rebase: the commit SHA changes, but
// the blob of a file whose content did not change is identical under any SHA
// that contains it. If the file does not exist in that commit, an explicit
// error (same criterion as FileContentAtCommit).
func BlobFileAtCommit(sha, file string) (string, error) {
	// Same reason as FileContentAtCommit: the <rev>:<path> pathspec
	// of git always uses "/", whatever separator the path arrived with.
	out, err := runGitOutput("rev-parse", sha+":"+filepath.ToSlash(file))
	if err != nil {
		return "", fmt.Errorf("could not resolve the blob of %q in commit %q: %w", file, sha, err)
	}
	return strings.TrimSpace(out), nil
}

// ResolveSHA returns the full SHA of an expression (HEAD, HEAD~2, an
// abbreviated sha...).
func ResolveSHA(expression string) (string, error) {
	out, err := runGitOutput("rev-parse", "--verify", expression+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitExists reports whether the SHA exists as a commit in the object
// store. Note: a commit rewritten by rebase/amend/squash still exists as a
// dangling object; use ContentInSomeRef to know whether it is still live in
// history.
func CommitExists(sha string) bool {
	_, err := runGitOutput("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// ContentInSomeRef reports whether the SHA is reachable from some local or
// remote ref (git branch -a --contains). A commit rewritten by rebase, amend
// or squash stops being contained in any ref and returns false here: that is
// the correct semantics for detecting orphaned ledger records.
func ContentInSomeRef(sha string) bool {
	out, err := runGitOutput("branch", "-a", "--contains", sha)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) != ""
}

// ContentInSomeRefFrom answers the same question but against the worktree's
// repository instead of the process's working directory.
//
// The difference matters wherever the answer decides a deletion. A caller
// purging the ledgers of several checkouts at once while classifying against
// the CWD would delete live records as soon as the process ran from another
// repository, because a legitimate SHA of this repo does not appear in that
// one's refs.
func ContentInSomeRefFrom(worktree, sha string) (bool, error) {
	// NO failure is read as absence. That was the missing property: while an
	// error meant "it is not there", every environment variable that could
	// break the query —a foreign object store, a redirected repository—
	// turned into a deletion of live records, and patching them one by one
	// only changed which failure reached the wrong rule.
	//
	// rev-parse separates the two cases by exit code: 1 is "this object does
	// not exist", the legitimate orphan, and anything else is a real failure
	// that must abort the purge instead of deciding it.
	if _, err := gitIn(worktree, "rev-parse", "--verify", "--quiet", sha+"^{commit}"); err != nil {
		var out *exec.ExitError
		if errors.As(err, &out) && out.ExitCode() == 1 {
			// KNOWN LIMIT, not an oversight (FU-15): an unreadable object while
			// HEAD remains readable produces the same code. Telling "collected
			// by gc" from "corrupt in the store" would require fsck-level
			// verification on every purge, disproportionate here. It is
			// accepted because the normal orphan case after rebase or amend
			// never reaches this point: there the object still exists and
			// `branch --contains` decides, and it does distinguish error from
			// empty.
			return false, nil
		}
		return false, fmt.Errorf("resolving %s in %s: %w", sha, worktree, err)
	}
	// The object exists, so an error here can no longer mean "it is not there".
	out, err := gitIn(worktree, "branch", "-a", "--contains", sha)
	if err != nil {
		return false, fmt.Errorf("checking containment of %s in %s: %w", sha, worktree, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// RequireUsableRepository confirms that worktree resolves to a Git
// repository. A deletion driven by ContentInSomeRefFrom must call it once
// before classifying anything: it is what separates "this commit is gone"
// from "I could not ask".
func RequireUsableRepository(worktree string) error {
	if _, err := gitIn(worktree, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("%s is not a usable git repository: %w", worktree, err)
	}
	// HEAD is the anchor, and it is needed because the exit code is NOT
	// enough. With a GIT_OBJECT_DIRECTORY that exists but does not contain
	// the repository's objects, `rev-parse --verify --quiet <sha>` exits
	// with 1, exactly like a genuinely unknown object: the repository
	// resolves and its objects do not. A repository that cannot resolve its
	// own HEAD is in no position to decide whether a commit is still live,
	// and without this check it would answer "does not exist" to everything
	// and empty the ledgers.
	if _, err := gitIn(worktree, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return fmt.Errorf("%s cannot resolve its own HEAD, so it cannot answer whether a commit is still live: %w", worktree, err)
	}
	return nil
}

// gitIn runs git against worktree with the environment stripped of the
// variables that select the repository.
//
// This is not theoretical caution: GIT_DIR TAKES PRIORITY OVER "-C". With
// GIT_DIR pointing elsewhere, `git -C <path> rev-parse --git-dir` answers
// for GIT_DIR's repository, not for the path's. Sentinel runs inside its
// own pre-commit hook, which is exactly a context where Git exports those
// variables, so a query that decides deletions cannot trust "-C" without
// cleaning them.
func gitIn(worktree string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	cmd.Env = envWithoutRepoSelection()
	out, err := cmd.Output()
	return string(out), err
}

// envWithoutRepoSelection removes ONLY what selects a repository or
// restricts which refs are visible. It deliberately does NOT touch
// GIT_OBJECT_DIRECTORY nor GIT_ALTERNATE_OBJECT_DIRECTORIES: those say
// where the objects live, not which repository it is, and removing them
// would break a repository whose objects live where the environment says.
// `branch --contains` would fail for perfectly reachable commits and the
// failure would read as "orphan".
//
// The comparison ignores case because on Windows variable names are not
// case-sensitive: a lowercase `git_dir` would survive a case-sensitive
// filter and override "-C" again.
func envWithoutRepoSelection() []string {
	repoSelectionVars := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_NAMESPACE"}
	environ := os.Environ()
	clean := make([]string, 0, len(environ))
	for _, variable := range environ {
		name, _, _ := strings.Cut(variable, "=")
		discard := false
		for _, candidate := range repoSelectionVars {
			if strings.EqualFold(name, candidate) {
				discard = true
				break
			}
		}
		if !discard {
			clean = append(clean, variable)
		}
	}
	return clean
}

// GitInIsolated runs git against worktree with the same sanitized
// environment the reachability probe uses. It exists so that everything
// taking part in a deletion decision looks at the SAME repository:
// sanitizing only one of the two queries mixes identities, and classifying
// one repository's ledgers against another's refs deletes live records.
func GitInIsolated(worktree string, args ...string) (string, error) {
	return gitIn(worktree, args...)
}

// IsAncestorOf reports whether ancestor is reachable from descendant, that
// is, whether both are on the same line of history and in that order.
//
// The exit codes of `merge-base --is-ancestor` separate the two cases: 0 is
// yes, 1 is no, and anything else is a real failure. None of those failures
// is read as "no": an unanswerable query never decides, which here means
// never attributing a correction to a commit from another branch.
func IsAncestorOf(worktree, ancestor, descendant string) (bool, error) {
	if _, err := gitIn(worktree, "merge-base", "--is-ancestor", ancestor, descendant); err != nil {
		var out *exec.ExitError
		if errors.As(err, &out) && out.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("checking whether %s is an ancestor of %s in %s: %w", ancestor, descendant, worktree, err)
	}
	return true, nil
}

// PublishedToRemote reports whether sha is an ancestor of origin/main in
// worktree's repository: the T9.5 publication boundary. A published commit's
// in-flight detail (review dimensions, corrections, guarantees) has no
// operational reader left, so retention may collect its execution streams.
//
// The remote base branch is pinned by the phase contract, not derived per
// repository: the phase verified that a PR-merged trigger never fires here
// (no pr-create events exist) and that a local-main trigger retires records
// before gate can read them. origin/main is the only boundary that holds
// for both flows. Repositories on another default branch skip retention
// with a visible one-line note rather than a silent misfire.
//
// Any query failure is an error, never a negative. merge-base answers 1
// only when both refs resolve and are unrelated; an unknown or unreadable
// object fails differently (FU-15), and a missing origin/main fails as
// well. Reading any of those as "unpublished" would either leak published
// detail forever or, worse, authorize collection on an unanswerable query.
// Callers treat the error as fail-closed and skip retention for that run.
func PublishedToRemote(worktree, sha string) (bool, error) {
	return IsAncestorOf(worktree, sha, "origin/main")
}

// UpstreamOrMain returns the base ref for auditing commit chains: the
// upstream if it exists; otherwise the local main branch; otherwise master.
func UpstreamOrMain() (string, error) {
	for _, ref := range []string{"@{u}", "main", "master"} {
		out, err := runGitOutput("rev-parse", "--verify", "--quiet", ref)
		if err == nil && strings.TrimSpace(out) != "" {
			return ref, nil
		}
	}
	return "", errors.New("no upstream or main/master branch found for the chain")
}

// BranchRemote returns the remote configured for a branch
// (branch.<branch>.remote) or empty if the branch has no remote.
func BranchRemote(branch string) string {
	out, err := runGitOutput("config", "--get", "branch."+branch+".remote")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// BranchRemoteFrom returns the configured remote for a branch in worktree.
func BranchRemoteFrom(worktree, branch string) string {
	out, err := GitInIsolated(worktree, "config", "--get", "branch."+branch+".remote")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Attributes returns the content of .gitattributes at revision, or an
// empty string if the tree does not have it.
//
// Absence and failure are told apart with ls-tree: `git show
// <rev>:.gitattributes` and `git cat-file -e` exit non-zero both when the
// path is missing and when the repository or object cannot be read, so
// either would turn a real failure into "no attributes". A missing path is
// empty output with a zero exit code.
func Attributes(revision string) (string, error) {
	listing, err := runGitOutput("ls-tree", "--name-only", revision, "--", ".gitattributes")
	if err != nil {
		return "", fmt.Errorf("looking for .gitattributes at %s: %w", revision, err)
	}
	if strings.TrimSpace(listing) == "" {
		return "", nil
	}
	content, err := runGitOutput("show", revision+":.gitattributes")
	if err != nil {
		return "", fmt.Errorf("reading .gitattributes at %s: %w", revision, err)
	}
	return content, nil
}
