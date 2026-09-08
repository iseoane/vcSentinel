// Package reviewsnapshot owns the read-only review snapshot discipline shared
// by every adapter family: audited paths are materialized from COMMITTED
// content (git ls-tree / git show) into an isolated temporary directory, only
// committed regular files survive filtering, and cleanup stays caller-owned.
//
// It lives outside internal/agentadapter on purpose: both the CLI adapters
// (agentadapter) and the ACP/acpx adapter (acpadapter) must run the exact
// same discipline, and neither package can own it without forcing a dependency
// direction between the two adapter families. This package depends on
// nothing but the standard library, so both families import it freely.
//
// Any semantic change belongs here so every adapter kind stays byte-identical;
// wrappers elsewhere must remain pure delegation.
package reviewsnapshot

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SafePaths filters and normalizes reviewer-audited paths: absolute paths,
// drive letters, traversal escapes, option-looking strings, paths carrying
// control or glob metacharacters, and sensitive environment files are dropped;
// the survivors are slash-normalized and cleaned. Environment examples ending
// in .env.example remain reviewable. Shared by every adapter family so the
// review snapshot accepts exactly the same path vocabulary everywhere.
func SafePaths(paths []string) []string {
	safe := make([]string, 0, len(paths))
	for _, p := range paths {
		if clean, ok := safePath(p); ok {
			safe = append(safe, clean)
		}
	}
	return safe
}

// safePath applies the exact same vocabulary guard SafePaths applies to
// reviewer-audited input to a single path. It is reused for the whole
// committed tree in Create: a path taken straight from git's own tree
// listing is not automatically more trustworthy than reviewer input, so the
// same safety and sensitivity gates apply to both.
func safePath(p string) (string, bool) {
	normalized := strings.ReplaceAll(p, "\\", "/")
	clean := path.Clean(normalized)
	drive := len(clean) >= 2 && clean[1] == ':'
	if p == "" || path.IsAbs(clean) || drive || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\x00\r\n*?[]{}!") || isSensitiveEnvironmentFile(clean) {
		return "", false
	}
	return clean, true
}

func isSensitiveEnvironmentFile(filePath string) bool {
	name := strings.ToLower(path.Base(filePath))
	if strings.HasSuffix(name, ".env.example") {
		return false
	}
	return strings.HasSuffix(name, ".env") || strings.Contains(name, ".env.")
}

// staleSnapshotAge bounds how long an abandoned snapshot may survive. A review
// can never outlive review.timeout (300s by default), so anything older than a
// day is definitively residue from a process that died before its cleanup ran,
// never work in flight. The margin is deliberately enormous: deleting a live
// snapshot would sabotage a running review, while deleting one a day late costs
// nothing.
const staleSnapshotAge = 24 * time.Hour

// reapAbandonedSnapshots removes review snapshots left behind by processes that
// died before their caller-owned cleanup could run — Ctrl+C, an aborted gate, a
// daemon shutdown. The cleanup Create returns is a defer, and a defer does not
// run when the process is killed, so without this the directories accumulated
// forever: 472 MB were measured on one machine, in a 3.8 GB tmpfs where filling
// /tmp breaks not just reviews but compilation.
//
// It is best-effort by contract: every error is ignored, because failing to
// tidy must never fail the review that was about to start. It only ever touches
// entries carrying this package's own prefix.
func reapAbandonedSnapshots(now time.Time, maxAge time.Duration) int {
	raiz := os.TempDir()
	entradas, err := os.ReadDir(raiz)
	if err != nil {
		return 0
	}
	recolectados := 0
	for _, entrada := range entradas {
		if !entrada.IsDir() || !strings.HasPrefix(entrada.Name(), snapshotPrefix) {
			continue
		}
		info, err := entrada.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if os.RemoveAll(filepath.Join(raiz, entrada.Name())) == nil {
			recolectados++
		}
	}
	return recolectados
}

// snapshotPrefix identifies this package's temporary directories, both when
// creating one and when reaping the ones nobody cleaned up.
const snapshotPrefix = "vas-sentinel-review-"

// Create materializes the read-only review snapshot for sha. It writes every
// committed regular file of the audited commit's tree into the snapshot, not
// only the paths under audit: a restricted reviewer that can only read the
// files it is auditing has no way to read the context files it needs to
// understand them, and a denied read for one of those silently kills the
// whole turn (confirmed defect, see docs/issues/1). paths still identifies
// what is under audit: allowed returns exactly that subset, unchanged in
// meaning and order, for the prompt and the permission map to consume.
//
// It returns the snapshot directory, the audited paths that survived the
// committed-regular-file filter, a caller-owned cleanup func, and an error.
// An empty worktree resolves to the current working directory, which matches
// production usage (review runs from the worktree root).
//
// ctx is honored end to end: it is checked before any git work starts and
// periodically DURING the whole-tree materialization loop, not only once at
// entry. Materializing the whole committed tree is a long operation, and a
// caller that cancels while it is running (Ctrl+C, controller abort, a
// caller deadline) must observe that promptly instead of waiting for
// materialization to finish before the failure is even noticed. On abort,
// the snapshot directory created so far is removed (cleanup runs before
// Create returns, so the returned cleanup func is nil) and the returned
// error wraps ctx's own error, so errors.Is(err, context.Canceled) and
// errors.Is(err, context.DeadlineExceeded) hold for the respective cases —
// several classifiers in this repository (reviewexec.DefaultClassifier,
// internal/execution's classify) depend on exactly that. A nil ctx is
// tolerated the same way the rest of this codebase does it (see
// reviewWithContextResultPolicy and ReviewWithContextResult, both of which
// substitute context.Background()), because Create is called from paths
// that may not have one.
func Create(ctx context.Context, worktree, sha string, paths []string) (string, []string, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sha == "" {
		return "", nil, nil, fmt.Errorf("semantic review requires an audited commit SHA")
	}
	if worktree == "" {
		var err error
		worktree, err = os.Getwd()
		if err != nil {
			return "", nil, nil, fmt.Errorf("resolve review worktree: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", nil, nil, fmt.Errorf("review snapshot aborted before it started: %w", err)
	}
	// Tidy before creating: the reaper is best-effort and never blocks the
	// review, but tying it to snapshot creation means the residue is bounded by
	// use instead of growing until something else breaks.
	reapAbandonedSnapshots(time.Now(), staleSnapshotAge)
	snapshot, err := os.MkdirTemp("", snapshotPrefix)
	if err != nil {
		return "", nil, nil, fmt.Errorf("create review snapshot: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(snapshot) }
	abort := func(err error) (string, []string, func(), error) {
		cleanup()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", nil, nil, fmt.Errorf("review snapshot aborted: %w", ctxErr)
		}
		return "", nil, nil, err
	}

	entries, err := gitTreeEntries(ctx, worktree, sha)
	if err != nil {
		return abort(err)
	}
	regularFiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.objectType != "blob" || !strings.HasPrefix(entry.mode, "100") {
			continue
		}
		if clean, ok := safePath(entry.path); ok && clean == entry.path {
			regularFiles = append(regularFiles, entry.path)
		}
	}
	if err := materializeTree(ctx, worktree, sha, snapshot, regularFiles); err != nil {
		return abort(err)
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}

	allowed := make([]string, 0, len(paths))
	for _, filePath := range SafePaths(paths) {
		if err := ctx.Err(); err != nil {
			return abort(err)
		}
		mode, objectType, exists, err := gitTreeEntry(ctx, worktree, sha, filePath)
		if err != nil {
			return abort(err)
		}
		if !exists || objectType != "blob" || !strings.HasPrefix(mode, "100") {
			continue
		}
		allowed = append(allowed, filePath)
	}
	return snapshot, allowed, cleanup, nil
}

func gitTreeEntry(ctx context.Context, worktree, sha, filePath string) (mode, objectType string, exists bool, err error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "ls-tree", "-z", sha, "--", filePath)
	output, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", "", false, ctxErr
		}
		return "", "", false, fmt.Errorf("read audited tree entry %q: %w", filePath, err)
	}
	if len(output) == 0 {
		return "", "", false, nil
	}
	header, _, ok := bytes.Cut(output, []byte{'\t'})
	if !ok {
		return "", "", false, fmt.Errorf("invalid audited tree entry for %q", filePath)
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 {
		return "", "", false, fmt.Errorf("invalid audited tree entry for %q", filePath)
	}
	return fields[0], fields[1], true, nil
}

// treeEntry is one line of a recursive git ls-tree listing.
type treeEntry struct {
	mode       string
	objectType string
	path       string
}

// gitTreeEntries lists every entry of sha's tree recursively in a single git
// invocation, so materializing the whole committed tree never costs one
// child process per file. -z NUL-terminates records and disables filename
// quoting, so a path may safely contain any byte except NUL. --full-tree
// pins every listed path to the repository root regardless of worktree:
// without it, `ls-tree -r` with no pathspec lists paths relative to the
// invocation's current directory whenever that directory happens to sit
// inside the repository (for example a test binary's package directory),
// which would then feed cat-file (always root-relative) paths it cannot
// resolve.
func gitTreeEntries(ctx context.Context, worktree, sha string) ([]treeEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "ls-tree", "-r", "-z", "--full-tree", sha)
	output, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("list committed tree for %q: %w", sha, err)
	}
	var entries []treeEntry
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		header, filePath, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("invalid committed tree entry for %q", sha)
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid committed tree entry for %q", sha)
		}
		entries = append(entries, treeEntry{mode: fields[0], objectType: fields[1], path: string(filePath)})
	}
	return entries, nil
}

// materializeTree writes the committed content of every path in paths into
// snapshot, using a single `git cat-file --batch` process rather than one
// `git show`/`git archive` invocation per file or per commit. cat-file reads
// raw blob objects straight from the object database: unlike `git archive`,
// it never runs the tree's own .gitattributes (export-ignore could silently
// drop a file, export-subst could silently rewrite its bytes), which matters
// because this package's contract is byte-identical committed content.
func materializeTree(ctx context.Context, worktree, sha, snapshot string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", worktree, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open committed tree batch reader for %q: %w", sha, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open committed tree batch reader for %q: %w", sha, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start committed tree batch reader for %q: %w", sha, err)
	}

	writeErr := make(chan error, 1)
	go func() {
		defer func() { _ = stdin.Close() }()
		writer := bufio.NewWriter(stdin)
		for _, filePath := range paths {
			if _, err := fmt.Fprintf(writer, "%s:%s\n", sha, filePath); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- writer.Flush()
	}()

	reader := bufio.NewReader(stdout)
	for _, filePath := range paths {
		// Checked on every iteration, not only once at entry: this loop can
		// run once per file in the whole committed tree, and a caller that
		// cancels while it is midway through must be observed promptly
		// instead of after every remaining file has been written.
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return ctxErr
		}
		header, err := reader.ReadString('\n')
		if err != nil {
			_ = cmd.Wait()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read committed tree batch header for %q: %w", filePath, err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			_ = cmd.Wait()
			return fmt.Errorf("unexpected committed tree batch entry for %q: %q", filePath, strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			_ = cmd.Wait()
			return fmt.Errorf("invalid committed tree batch size for %q: %q", filePath, strings.TrimSpace(header))
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(reader, content); err != nil {
			_ = cmd.Wait()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read committed tree batch content for %q: %w", filePath, err)
		}
		// Every batch response carries one trailing LF after the object bytes.
		if _, err := reader.Discard(1); err != nil {
			_ = cmd.Wait()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read committed tree batch trailer for %q: %w", filePath, err)
		}
		target := filepath.Join(snapshot, filepath.FromSlash(filePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("create review snapshot directory: %w", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("write review snapshot file: %w", err)
		}
	}
	if err := <-writeErr; err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("write committed tree batch request for %q: %w", sha, err)
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("materialize committed tree batch for %q: %w (%s)", sha, err, stderr.String())
	}
	return nil
}
