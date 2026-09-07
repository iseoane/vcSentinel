package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// refuteOptions carries the non-interactive human refutation request: the
// reviewed SHA plus the finding's stable fingerprint address the finding,
// and the reason plus the immutable snapshot line range carry the answer.
// The evidence itself is always read from the audited Git object, never
// supplied by the caller.
type refuteOptions struct {
	sha         string
	fingerprint string
	reason      string
	lineStart   int
	lineEnd     int
}

func parseRefuteArgs(args []string) (refuteOptions, error) {
	var opts refuteOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--sha":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--sha requires a value (the reviewed commit SHA)")
			}
			opts.sha = args[i]
		case "--fingerprint":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--fingerprint requires a value (the finding's stable fingerprint)")
			}
			opts.fingerprint = args[i]
		case "--reason":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--reason requires a value (why the finding's premise is false)")
			}
			opts.reason = args[i]
		case "--line-start":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--line-start requires a value (first evidence line, 1-based)")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				return opts, fmt.Errorf("--line-start %q is not a positive line number", args[i])
			}
			opts.lineStart = n
		case "--line-end":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("--line-end requires a value (last evidence line, 1-based)")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				return opts, fmt.Errorf("--line-end %q is not a positive line number", args[i])
			}
			opts.lineEnd = n
		default:
			return opts, fmt.Errorf("unknown flag %q (usage: sentinel refute --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M)", args[i])
		}
	}
	if strings.TrimSpace(opts.sha) == "" {
		return opts, fmt.Errorf("missing required --sha (the reviewed commit SHA)")
	}
	if strings.TrimSpace(opts.fingerprint) == "" {
		return opts, fmt.Errorf("missing required --fingerprint (the finding's stable fingerprint)")
	}
	if strings.TrimSpace(opts.reason) == "" {
		return opts, fmt.Errorf("missing required --reason (why the finding's premise is false)")
	}
	if opts.lineStart <= 0 {
		return opts, fmt.Errorf("missing required --line-start (first evidence line, 1-based)")
	}
	if opts.lineEnd <= 0 {
		return opts, fmt.Errorf("missing required --line-end (last evidence line, 1-based)")
	}
	if opts.lineEnd < opts.lineStart {
		return opts, fmt.Errorf("--line-end %d is before --line-start %d", opts.lineEnd, opts.lineStart)
	}
	return opts, nil
}

// refuteDeps groups the seams runRefutation needs so tests drive it without
// git, a worktree, or the shared ledger.
type refuteDeps struct {
	ledger   *review.Ledger
	store    *store.Store
	snapshot review.SnapshotReader
	now      func() time.Time
	// beforeLockedAppend is a test-only seam run after the finding is
	// resolved and validated but before the locked compare-and-append. A
	// test sets it to land a concurrent revision, proving a stale
	// refutation fails closed. Production leaves it nil.
	beforeLockedAppend func()
	// withLockedRecord permits tests to reproduce a lock cleanup failure after
	// a callback already persisted its independent disposition record.
	withLockedRecord func(string, func(*review.Record) error) error
}

// refuteOutcome separates a completed append from a hard refutation failure.
// A completion warning means the disposition is durable and must not be retried.
type refuteOutcome struct {
	disposition       *review.FindingDisposition
	completionWarning error
}

// resolveRefuteDeps wires the production seams for one worktree: the shared
// common-dir ledger and store plus a snapshot reader bound to this
// repository's immutable Git objects.
func resolveRefuteDeps(worktree string) (*refuteDeps, error) {
	ledger, err := sharedReviewLedger(worktree)
	if err != nil {
		return nil, err
	}
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	return &refuteDeps{
		ledger:   ledger,
		store:    store.NewStore(commonDir),
		snapshot: review.NewSnapshotReader(worktree),
		now:      time.Now,
	}, nil
}

// refuteDepsResolver is replaceable only so command-boundary tests can use
// controlled dependencies. Production resolution remains resolveRefuteDeps.
var refuteDepsResolver = resolveRefuteDeps

// loadDispositionsForWorktree reads the append-only human answers for the
// review and gate commands. A corrupt log is an error, never an empty set:
// those commands must fail closed rather than audit as if no human ever
// answered.
func loadDispositionsForWorktree(worktree string) ([]review.FindingDisposition, error) {
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	dispositions, err := store.NewStore(commonDir).ReadDispositions()
	if err != nil {
		return nil, fmt.Errorf("reading human dispositions: %w", err)
	}
	return dispositions, nil
}

// runRefutation records one evidence-bound human refutation. Every failure
// before append is closed without persisting anything: a missing review
// record, a missing or ambiguous fingerprint, an already-cleared finding, a
// range the refutation gate rejects, an unreadable snapshot, a review record
// that changed between resolution and append, or a corrupt dispositions log.
// A record lock cleanup error after a completed append is returned as a
// completion warning; all other errors are hard failures.
func runRefutation(deps *refuteDeps, opts refuteOptions) (refuteOutcome, error) {
	if deps == nil || deps.ledger == nil || deps.store == nil || deps.snapshot == nil {
		return refuteOutcome{}, fmt.Errorf("refute: missing dependencies")
	}
	now := time.Now
	if deps.now != nil {
		now = deps.now
	}
	sha := strings.TrimSpace(opts.sha)
	fingerprint := strings.TrimSpace(opts.fingerprint)
	reason := strings.TrimSpace(opts.reason)
	if sha == "" || fingerprint == "" || reason == "" {
		return refuteOutcome{}, fmt.Errorf("refute: --sha, --fingerprint, and --reason are required")
	}
	if strings.HasPrefix(sha, "-") {
		return refuteOutcome{}, fmt.Errorf("refute: invalid reviewed SHA")
	}
	record, err := deps.ledger.ReadRecord(sha)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("refute: reading the review record: %w", err)
	}
	if record == nil || len(record.Revisions) == 0 {
		return refuteOutcome{}, fmt.Errorf("refute: no review record for SHA %q", sha)
	}
	target, err := review.ResolveDispositionTarget(record.Revisions[len(record.Revisions)-1], fingerprint)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("refute: %w", err)
	}
	if !review.IsBlocking(target.Severity, target.Status) {
		return refuteOutcome{}, fmt.Errorf("refute: finding %q does not block (status %q)", fingerprint, target.Status)
	}
	// The evidence path derives exclusively from the addressed finding: the
	// caller supplies no path, so a refutation cannot be redirected at a
	// file the finding never cited.
	safePath, evidence, rangeHash, err := review.ValidateHumanRefutationRange(
		deps.snapshot, sha, target.Location.File, target.Location.LineStart,
		reason, target.Location.File, opts.lineStart, opts.lineEnd)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("refute: %w", err)
	}
	disposition := &review.FindingDisposition{
		SHA:               sha,
		Fingerprint:       review.EffectiveFingerprint(target),
		Status:            review.StatusRefuted,
		Reason:            reason,
		Path:              safePath,
		LineStart:         opts.lineStart,
		LineEnd:           opts.lineEnd,
		Evidence:          evidence,
		RangeHash:         rangeHash,
		Actor:             review.RefutationActorHuman,
		Source:            review.DispositionSourceHuman,
		At:                now().UTC(),
		TargetDimension:   target.Dimension,
		TargetLine:        target.Location.LineStart,
		TargetDescription: target.Description,
		TargetSeverity:    target.Severity,
	}
	// Compare-and-append against the authoritative record: the fingerprint
	// above was resolved against a record read before any lock, and a
	// concurrent re-audit may have retired it since. The expected bytes are
	// snapshotted here and re-checked under the same record lock the
	// revision writer holds; any difference fails closed with nothing
	// persisted. The store append itself runs inside the lock so no writer
	// can slip between the re-check and the write.
	expected, err := json.Marshal(record)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("refute: reading the review record: %w", err)
	}
	if deps.beforeLockedAppend != nil {
		deps.beforeLockedAppend()
	}
	withLockedRecord := deps.ledger.WithLockedRecord
	if deps.withLockedRecord != nil {
		withLockedRecord = deps.withLockedRecord
	}
	completed := false
	if err := withLockedRecord(sha, func(current *review.Record) error {
		fresh, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("refute: reading the review record: %w", err)
		}
		if !bytes.Equal(fresh, expected) {
			return fmt.Errorf("refute: the review record changed during refutation; re-resolve the finding and retry")
		}
		dispositions, err := deps.store.ReadDispositions()
		if err != nil {
			return fmt.Errorf("refute: reading dispositions before append: %w", err)
		}
		currentRevision := current.Revisions[len(current.Revisions)-1]
		effectiveTarget, err := review.ResolveDispositionTargetWithDispositions(
			currentRevision, fingerprint, review.FilterDispositionsForSHA(dispositions, sha))
		if err != nil {
			return fmt.Errorf("refute: %w", err)
		}
		if !review.IsBlocking(effectiveTarget.Severity, effectiveTarget.Status) {
			return fmt.Errorf("refute: finding %q does not block (status %q)", fingerprint, effectiveTarget.Status)
		}
		if err := deps.store.AppendDisposition(disposition); err != nil {
			return fmt.Errorf("refute: persisting the disposition: %w", err)
		}
		completed = true
		return nil
	}); err != nil {
		if completed && errors.Is(err, review.ErrLockNotReleased) {
			return refuteOutcome{
				disposition:       disposition,
				completionWarning: fmt.Errorf("refute: disposition recorded but record lock cleanup failed: %w", err),
			}, nil
		}
		return refuteOutcome{}, err
	}
	return refuteOutcome{disposition: disposition}, nil
}

// runRefute implements `sentinel refute`: parse, resolve, record.
// Exit 0 records one disposition, including the completed-but-warning case
// where only the record lock cleanup failed after persistence.
func runRefute(w io.Writer, worktree string, args []string) int {
	opts, err := parseRefuteArgs(args)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	deps, err := refuteDepsResolver(worktree)
	if err != nil {
		fmt.Fprintf(w, "❌ Cannot resolve the review ledger: %v\n", err)
		return 1
	}
	outcome, err := runRefutation(deps, opts)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	return reportRefutationResult(w, outcome)
}

func reportRefutationResult(w io.Writer, outcome refuteOutcome) int {
	if outcome.disposition == nil {
		fmt.Fprintln(w, "❌ refute completed without a disposition")
		return 1
	}
	disposition := outcome.disposition
	fmt.Fprintf(w, "✅ Human refutation recorded for finding %q on %s (%s lines %d-%d, range %s).\n",
		disposition.Fingerprint, disposition.SHA, disposition.Path,
		disposition.LineStart, disposition.LineEnd, disposition.RangeHash)
	if outcome.completionWarning != nil {
		fmt.Fprintf(w, "⚠️  The refutation is recorded, but record lock cleanup failed: %v\n", outcome.completionWarning)
	}
	return 0
}
