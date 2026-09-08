// Package reviewsnapshot owns the read-only review snapshot discipline shared
// by every adapter family: audited paths are materialized from COMMITTED
// content (git ls-tree / git show) into a shared, per-commit snapshot store,
// only committed regular files survive filtering, and cleanup stays
// caller-owned as an idempotent lease release.
//
// It lives outside internal/agentadapter on purpose: both the CLI adapters
// (agentadapter) and the ACP/acpx adapter (acpadapter) must run the exact
// same discipline, and neither package can own it without forcing a dependency
// direction between the two adapter families. This package depends on nothing
// but the standard library plus the same golang.org/x/sys syscall wrappers
// internal/git's snapshot locks use, so both families import it freely.
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
// control or glob metacharacters, and sensitive files are dropped; the
// survivors are slash-normalized and cleaned. A template or sample copy of a
// sensitive filename (for example .env.example) remains reviewable. Shared
// by every adapter family so the review snapshot accepts exactly the same
// path vocabulary everywhere.
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
// same safety and sensitivity gates apply to both. Keeping both callers
// funneled through this one function is deliberate: SafePaths (audited
// paths) and the whole-tree enumeration in Create must never diverge on
// what counts as sensitive.
func safePath(p string) (string, bool) {
	normalized := strings.ReplaceAll(p, "\\", "/")
	clean := path.Clean(normalized)
	drive := len(clean) >= 2 && clean[1] == ':'
	if p == "" || path.IsAbs(clean) || drive || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(p, "-") || strings.ContainsAny(p, "\x00\r\n*?[]{}!") || isSensitiveFile(clean) {
		return "", false
	}
	return clean, true
}

// isSensitiveFile decides whether filePath must never leave the working tree
// through a review snapshot. It used to match only dotenv-style names, which
// was proportionate when a snapshot held only the handful of paths under
// audit. Create now materializes every committed regular file as read-only
// context (see Create's doc comment), so this filter is the only thing
// standing between a third-party reviewer process and every committed
// private key, credential store, or token-bearing config in the repository —
// it has to cover the whole tree, not just the files a reviewer asked for.
//
// Each blocked class below is a known way real repositories leak secrets.
// Two carve-outs keep it from over-blocking material reviewers legitimately
// need: a documentation template (.example/.sample/.template, mirroring the
// pre-existing .env.example allowance) is never a secret in this tree, and
// public halves of a keypair (SSH .pub files, X.509 certificates) are meant
// to be shared and are not the sensitive half.
func isSensitiveFile(filePath string) bool {
	name := strings.ToLower(path.Base(filePath))
	full := strings.ToLower(filePath)

	if isSensitiveFileTemplate(name) {
		return false
	}
	if strings.HasSuffix(name, ".pub") {
		// The public half of an SSH/GPG keypair is shareable by design; only
		// the private half (see below) is the secret.
		return false
	}
	if strings.HasSuffix(name, ".crt") || strings.HasSuffix(name, ".cer") || strings.HasSuffix(name, ".cert") {
		// X.509 certificates are public by design (that is the point of a
		// certificate); the paired private key is what must stay out.
		return false
	}

	// Dotenv-style environment files: the original, narrower filter this
	// function replaces. Kept verbatim so existing behavior does not regress.
	if strings.HasSuffix(name, ".env") || strings.Contains(name, ".env.") {
		return true
	}

	// Private keys and other key material: TLS/SSH private keys, PKCS#12 and
	// Java keystores, and PuTTY private keys are routinely committed by
	// accident and grant direct access to whatever they authenticate.
	for _, ext := range []string{".pem", ".key", ".pfx", ".p12", ".jks", ".keystore", ".ppk"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	switch name {
	case "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		// ssh-keygen's default unencrypted private key filenames: no
		// extension, so they need an explicit name match rather than a
		// suffix check.
		return true
	}

	// Credential and token stores: package manager, VCS and generic
	// credential helper files that hold bearer tokens or plaintext
	// passwords for whatever service they configure.
	switch name {
	case ".npmrc", ".pypirc", ".netrc", "_netrc", ".git-credentials", ".dockercfg", ".htpasswd", ".pgpass":
		return true
	}

	// Well-known credential paths that only carry secrets at a specific,
	// conventional location: the bare filename ("credentials", "config",
	// "config.json") is far too generic to block everywhere, but at these
	// exact paths it is always a cloud provider's credential or auth store.
	for _, suffix := range []string{"/.aws/credentials", "/.docker/config.json"} {
		if full == strings.TrimPrefix(suffix, "/") || strings.HasSuffix(full, suffix) {
			return true
		}
	}
	if name == "application_default_credentials.json" {
		// gcloud's default application credentials file.
		return true
	}

	return false
}

// isSensitiveFileTemplate reports whether name is a documentation template
// or sample copy of an otherwise-sensitive filename, generalizing the
// pre-existing .env.example carve-out to every sensitive class above (for
// example id_rsa.example, service.key.sample).
func isSensitiveFileTemplate(name string) bool {
	for _, suffix := range []string{".example", ".sample", ".template"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
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
// Legacy per-invocation snapshot directories — the vas-sentinel-review-*
// prefix this package created before snapshots became shared and reusable —
// are removed purely by age, exactly as before. Shared-store entries are
// removed lock-aware: reapSharedStore takes each candidate SHA's nonblocking
// exclusive lock first, so a tree a live lease still holds in any process is
// skipped even when its directory already looks stale, while abandoned
// trees, staging directories, orphaned readiness artifacts, and stale
// per-provider state roots go away.
//
// It is best-effort by contract: every error is ignored, because failing to
// tidy must never fail the review that was about to start. It only ever
// touches entries carrying this package's own names: the legacy temporary
// prefix and the shared store directory.
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
		if removeReadOnlyStoreEntry(filepath.Join(raiz, entrada.Name())) == nil {
			recolectados++
		}
	}
	return recolectados + reapSharedStore(storeRoot(), now, maxAge)
}

// snapshotPrefix identifies this package's LEGACY per-invocation temporary
// directories. Nothing creates them anymore — snapshots are shared and
// reusable now — but the reaper keeps cleaning them so machines upgraded
// mid-flight do not accumulate residue forever.
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
// Snapshots are shared, not per-call: one published tree exists per audited
// commit SHA in a package-private store under os.TempDir(), materialized once
// and reused by every caller for that SHA — sequential or concurrent, in
// this process or across processes. A call materializes only when no
// complete tree is published yet: it writes into a staging directory,
// publishes a readiness manifest and marker, and atomically renames the
// staging directory onto the SHA's published name, so no caller can ever
// observe a partial tree. Published evidence is immutable and validated:
// regular files are owner read-only, the manifest records every committed
// file's mode and size, and every lease validates marker, manifest, and
// on-disk tree — a corrupted cache is never handed out; Create rebuilds it
// from Git under the per-SHA transition lock. The returned dir is leased,
// not owned: cleanup is an idempotent lease release that must still run as
// the caller's defer, and it never deletes the published tree — the snapshot
// is retained on disk so the next invocation auditing the same SHA (a format
// or transport retry, a second provider, a later review) leases the very
// same directory, and the lock-aware stale reaper that runs at every Create
// is the only thing that ever removes it, once no lease in any process holds
// it and its mtime is past staleSnapshotAge.
//
// It returns the snapshot directory, the audited paths that survived the
// committed-regular-file filter, a caller-owned cleanup func, and an error.
// sha must be a full hexadecimal Git object id — 40 characters for SHA-1
// repositories, 64 for SHA-256 — because it is the store's storage key;
// abbreviations and revspecs are rejected before any filesystem work. An
// empty worktree resolves to the current working directory, which matches
// production usage (review runs from the worktree root).
//
// ctx is honored end to end: it is checked before any git work starts and
// periodically DURING the whole-tree materialization loop, not only once at
// entry. Materializing the whole committed tree is a long operation, and a
// caller that cancels while it is running (Ctrl+C, controller abort, a
// caller deadline) must observe that promptly instead of waiting for
// materialization to finish before the failure is even noticed. On abort,
// the staging directory materialized so far is removed and nothing is
// published (cleanup runs before Create returns, so the returned cleanup
// func is nil) and the returned error wraps ctx's own error, so
// errors.Is(err, context.Canceled) and errors.Is(err, context.DeadlineExceeded)
// hold for the respective cases — several classifiers in this repository
// (reviewexec.DefaultClassifier, internal/execution's classify) depend on
// exactly that. A nil ctx is tolerated the same way the rest of this
// codebase does it (see reviewWithContextResultPolicy and
// ReviewWithContextResult, both of which substitute context.Background()),
// because Create is called from paths that may not have one.
func Create(ctx context.Context, worktree, sha string, paths []string) (string, []string, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sha == "" {
		return "", nil, nil, fmt.Errorf("semantic review requires an audited commit SHA")
	}
	// The object id is the shared store's storage key and ends up in file
	// and directory names, so it is validated before any filesystem work —
	// including the reaper's scan — can happen. Callers pass what git
	// rev-parse produced: full canonical hexadecimal object ids.
	if !validObjectID(sha) {
		return "", nil, nil, fmt.Errorf("invalid audited commit SHA %q: want a full hexadecimal git object id (40 or 64 hex characters)", sha)
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

	// Lease a published tree when one exists; otherwise create one and come
	// back to lease it. The cycle converges because publication only happens
	// under the SHA's exclusive lock and every lease validates completeness
	// under its own shared lock.
	for {
		if err := ctx.Err(); err != nil {
			return "", nil, nil, fmt.Errorf("review snapshot aborted: %w", err)
		}
		lease, err := leasePublishedSnapshot(sha)
		if err != nil {
			return "", nil, nil, err
		}
		if lease == nil {
			// Nothing reusable is published for this SHA yet: create it —
			// concurrent callers in this process single-flight behind one
			// materialization — and lease it on the next cycle.
			if err := publishSnapshotForSHA(ctx, worktree, sha); err != nil {
				return "", nil, nil, err
			}
			continue
		}
		allowed, err := allowedSnapshotPaths(ctx, worktree, sha, paths)
		if err != nil {
			lease.release()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return "", nil, nil, fmt.Errorf("review snapshot aborted: %w", ctxErr)
			}
			return "", nil, nil, err
		}
		return lease.dir, allowed, lease.release, nil
	}
}

// allowedSnapshotPaths verifies the audited paths against sha's committed
// tree: only paths that exist there as committed regular files survive,
// unchanged in meaning and order. It runs on every Create call even when the
// snapshot tree itself is reused, so the allowed vocabulary always reflects
// the audited commit rather than whoever happened to materialize it first.
func allowedSnapshotPaths(ctx context.Context, worktree, sha string, paths []string) ([]string, error) {
	allowed := make([]string, 0, len(paths))
	for _, filePath := range SafePaths(paths) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		mode, objectType, exists, err := gitTreeEntry(ctx, worktree, sha, filePath)
		if err != nil {
			return nil, err
		}
		if !exists || !isCommittedRegularFile(mode, objectType) {
			continue
		}
		allowed = append(allowed, filePath)
	}
	return allowed, nil
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
// because this package's contract is byte-identical committed content. Its
// `git cat-file --batch` response headers carry "<oid> <type> <size>" —
// field 0 is the blob OID, never a mode — so committed modes travel to the
// publisher from gitTreeEntries instead.
func materializeTree(ctx context.Context, worktree, sha, snapshot string, paths []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	// stderr must be wired BEFORE Start: assigned afterwards, os/exec sends
	// the child's stderr to the null device and the Wait diagnostics below
	// lose whatever git tried to report.
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

	// released guards the deferred forced release below. The happy path
	// (reached only after every response has been read and the writer
	// goroutine has reported success) calls cmd.Wait() itself, right before
	// returning, so a genuine cat-file failure keeps surfacing its stderr
	// exactly as before. released is set to true immediately before that
	// call so the defer below skips it.
	//
	// Every early return — a read error, a malformed batch entry, a
	// filesystem failure writing the snapshot, or a canceled context — used
	// to leave released false and call only cmd.Wait(). That deadlocks:
	// cat-file blocks writing its next response into a stdout pipe this
	// function has stopped draining, and the writer goroutine blocks writing
	// the next request into a stdin pipe cat-file is no longer reading, so
	// nothing ever exits and Wait never returns. Killing the process breaks
	// both blocks at once — a dead process can neither read stdin nor write
	// stdout, so the writer goroutine's pending write fails and the goroutine
	// returns, and Wait itself returns as soon as the kernel reaps the
	// killed process. Closing stdin here too is a harmless, idempotent
	// second signal for the writer goroutine (it closes stdin itself via its
	// own defer already; closing an already-closed pipe just returns an
	// ignored error).
	released := false
	defer func() {
		if released {
			return
		}
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	reader := bufio.NewReader(stdout)
	for _, filePath := range paths {
		// Checked on every iteration, not only once at entry: this loop can
		// run once per file in the whole committed tree, and a caller that
		// cancels while it is midway through must be observed promptly
		// instead of after every remaining file has been written.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		header, err := reader.ReadString('\n')
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read committed tree batch header for %q: %w", filePath, err)
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return fmt.Errorf("unexpected committed tree batch entry for %q: %q", filePath, strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid committed tree batch size for %q: %q", filePath, strings.TrimSpace(header))
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(reader, content); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read committed tree batch content for %q: %w", filePath, err)
		}
		// Every batch response carries one trailing LF after the object bytes.
		if _, err := reader.Discard(1); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read committed tree batch trailer for %q: %w", filePath, err)
		}
		target := filepath.Join(snapshot, filepath.FromSlash(filePath))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create review snapshot directory: %w", err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return fmt.Errorf("write review snapshot file: %w", err)
		}
	}
	if err := <-writeErr; err != nil {
		return fmt.Errorf("write committed tree batch request for %q: %w", sha, err)
	}
	released = true
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("materialize committed tree batch for %q: %w (%s)", sha, err, stderr.String())
	}
	return nil
}
