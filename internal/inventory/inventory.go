// Package inventory converts one explicit repository path into the plain
// facts the Control Center dashboard needs (read-only over git: no daemon,
// no TUI wiring, no writes, no $HOME scanning).
package inventory

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Worktree is one `git worktree list --porcelain` entry plus its probe outcome.
type Worktree struct {
	Path     string
	Branch   string // empty when Detached is true
	Detached bool
	Clean    bool
}

// Snapshot is the read-only fact sheet of one repository.
type Snapshot struct {
	Repository string
	Origin     string     // empty when no origin remote exists
	Worktrees  []Worktree // deterministically sorted by Path
}

// runner executes git inside an explicit dir and returns stdout; test seam.
type runner interface {
	run(dir string, args ...string) ([]byte, error)
}

// execRunner runs the real git binary (programmatic args, cmd.Dir, no shell).
type execRunner struct{}

func (execRunner) run(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if err != nil && errors.As(err, &exitErr) && len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
		return out, fmt.Errorf("%w: %s", err, bytes.TrimSpace(exitErr.Stderr))
	}
	return out, err
}

// headHashRE accepts lowercase object names of both supported hash sizes:
// 40 hex for sha1 and 64 hex for sha256 (`git init --object-format=sha256`).
var headHashRE = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// normalizeWorktreePath converts the slash-separated absolute path git emits
// (even on Windows) into a native cleaned absolute path on the host OS.
func normalizeWorktreePath(p string) (string, error) {
	abs, err := filepath.Abs(filepath.FromSlash(p))
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// Inspect normalizes repoPath and collects the snapshot. Every git failure
// wraps the command and the failing path; never a partial snapshot with nil.
func Inspect(repoPath string) (Snapshot, error) {
	return inspect(repoPath, execRunner{})
}

func inspect(repoPath string, r runner) (Snapshot, error) {
	if strings.TrimSpace(repoPath) == "" {
		return Snapshot{}, fmt.Errorf("inventory: repository path is empty")
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inventory: resolve path %q: %w", repoPath, err)
	}
	root = filepath.Clean(root)
	if _, err := r.run(root, "rev-parse", "--git-dir"); err != nil {
		return Snapshot{}, fmt.Errorf("inventory: git rev-parse --git-dir at %q: %w", root, err)
	}
	origin, err := originURL(r, root)
	if err != nil {
		return Snapshot{}, err
	}
	entries, err := listWorktrees(r, root)
	if err != nil {
		return Snapshot{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].entry.Path < entries[j].entry.Path })

	worktrees := make([]Worktree, 0, len(entries))
	for _, parsed := range entries {
		wt := parsed.entry
		if parsed.bare {
			wt.Clean = true // no working tree; probing would fail bare repos
		} else if wt.Clean, err = worktreeIsClean(r, wt.Path); err != nil {
			return Snapshot{}, err
		}
		worktrees = append(worktrees, wt)
	}
	return Snapshot{Repository: filepath.Base(root), Origin: origin, Worktrees: worktrees}, nil
}

// originURL returns the origin remote URL, or "" when absent (not an error):
// `git config --get` exits 1 with empty output iff the key is missing.
func originURL(r runner, root string) (string, error) {
	out, err := r.run(root, "config", "--get", "remote.origin.url")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(out) == 0 {
			return "", nil
		}
		return "", fmt.Errorf("inventory: git config --get remote.origin.url at %q: %w", root, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// parsedEntry pairs one porcelain block with its bare attribute (main-only).
type parsedEntry struct {
	entry Worktree
	bare  bool
}

// listWorktrees runs and strictly parses `git worktree list --porcelain`.
// Only the documented tokens (worktree, HEAD, branch, bare, detached,
// locked [reason]) are accepted; anything unknown, duplicated, contradictory,
// or a block not starting with `worktree` is an error. Every block must carry
// exactly one HEAD token, bare is main-entry-only, and zero blocks is a
// malformed-input error.
func listWorktrees(r runner, root string) ([]parsedEntry, error) {
	const command = "git worktree list --porcelain"
	out, err := r.run(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("inventory: %s at %q: %w", command, root, err)
	}

	var (
		entries  []parsedEntry
		current  *Worktree
		bare     bool
		sawHead  bool
		mainSeen bool
	)
	fail := func(lineIdx int, format string, args ...any) error {
		return fmt.Errorf("inventory: %s at %q: malformed porcelain output at line %d: %s",
			command, root, lineIdx+1, fmt.Sprintf(format, args...))
	}
	finalize := func(endLine int) error {
		if current == nil {
			return nil
		}
		if !sawHead {
			return fail(endLine, "worktree %q carries no HEAD token", current.Path)
		}
		if bare && mainSeen {
			return fail(endLine, "linked worktree %q carries the bare attribute", current.Path)
		}
		entries = append(entries, parsedEntry{entry: *current, bare: bare})
		mainSeen = true
		return nil
	}

	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if line == "" {
			if err := finalize(i); err != nil {
				return nil, err
			}
			current, bare, sawHead = nil, false, false
			continue
		}
		token, rest, _ := strings.Cut(line, " ")
		switch token {
		case "worktree":
			if current != nil {
				return nil, fail(i, "new worktree entry starts before the previous block ends")
			}
			path := strings.TrimSpace(rest)
			if path == "" {
				return nil, fail(i, "worktree token without a path")
			}
			native, err := normalizeWorktreePath(path)
			if err != nil {
				return nil, fmt.Errorf("inventory: %s at %q: worktree path %q: %w", command, root, path, err)
			}
			current = &Worktree{Path: native}
		case "HEAD":
			if current == nil || sawHead {
				return nil, fail(i, "unexpected HEAD token %q", line)
			}
			if hash := strings.TrimSpace(rest); !headHashRE.MatchString(hash) {
				return nil, fail(i, "HEAD hash %q is not 40 or 64 hexadecimal characters", hash)
			}
			sawHead = true // validated but deliberately not stored in this slice
		case "branch":
			if current == nil || current.Detached || current.Branch != "" {
				return nil, fail(i, "unexpected branch token %q", line)
			}
			name, found := strings.CutPrefix(strings.TrimSpace(rest), "refs/heads/")
			if !found || name == "" {
				return nil, fail(i, "branch reference %q lacks the refs/heads/ prefix", strings.TrimSpace(rest))
			}
			current.Branch = name
		case "bare":
			if current == nil || bare {
				return nil, fail(i, "unexpected bare token %q", line)
			}
			bare = true
		case "detached":
			if current == nil || current.Detached || current.Branch != "" {
				return nil, fail(i, "unexpected detached token %q", line)
			}
			current.Detached = true
		case "locked": // optional reason suffix; nothing stored in this slice
			if current == nil {
				return nil, fail(i, "locked token outside a worktree block")
			}
		default:
			return nil, fail(i, "unknown token %q", token)
		}
	}
	if err := finalize(len(lines)); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("inventory: %s at %q: malformed porcelain output: zero worktree blocks", command, root)
	}
	return entries, nil
}

// worktreeIsClean reports whether `git status --porcelain` in dir is empty.
func worktreeIsClean(r runner, dir string) (bool, error) {
	out, err := r.run(dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("inventory: git status --porcelain at %q: %w", dir, err)
	}
	return len(bytes.TrimSpace(out)) == 0, nil
}
