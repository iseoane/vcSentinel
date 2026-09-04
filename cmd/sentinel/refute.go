package main

import (
	"bytes"
	"encoding/json"
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
}

// resolveRefuteDeps wires the production seams for one worktree: the shared
// common-dir ledger and store plus a snapshot reader bound to this
// repository's immutable Git objects.
func resolveRefuteDeps(worktree string) (*refuteDeps, error) {
	ledger, err := sharedReviewLedger(worktree)
	if err != nil {
		return nil, err
	}
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	return &refuteDeps{
		ledger:   ledger,
		store:    store.NuevoStore(commonDir),
		snapshot: review.NewSnapshotReader(worktree),
		now:      time.Now,
	}, nil
}

// loadDispositionsForWorktree reads the append-only human answers for the
// review and gate commands. A corrupt log is an error, never an empty set:
// those commands must fail closed rather than audit as if no human ever
// answered.
func loadDispositionsForWorktree(worktree string) ([]review.FindingDisposition, error) {
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	dispositions, err := store.NuevoStore(commonDir).ReadDispositions()
	if err != nil {
		return nil, fmt.Errorf("reading human dispositions: %w", err)
	}
	return dispositions, nil
}

// runRefutation records one evidence-bound human refutation. Every failure
// is closed without persisting anything: a missing review record, a missing
// or ambiguous fingerprint, an already-cleared finding, a range the
// refutation gate rejects, an unreadable snapshot, a review record that
// changed between resolution and append, and a corrupt dispositions log all
// abort before the append. Persisted revisions are never mutated; the answer
// lands in the separate append-only log.
func runRefutation(deps *refuteDeps, opts refuteOptions) (*review.FindingDisposition, error) {
	if deps == nil || deps.ledger == nil || deps.store == nil || deps.snapshot == nil {
		return nil, fmt.Errorf("refute: missing dependencies")
	}
	now := time.Now
	if deps.now != nil {
		now = deps.now
	}
	sha := strings.TrimSpace(opts.sha)
	fingerprint := strings.TrimSpace(opts.fingerprint)
	reason := strings.TrimSpace(opts.reason)
	if sha == "" || fingerprint == "" || reason == "" {
		return nil, fmt.Errorf("refute: --sha, --fingerprint, and --reason are required")
	}
	if strings.HasPrefix(sha, "-") {
		return nil, fmt.Errorf("refute: invalid reviewed SHA")
	}
	ficha, err := deps.ledger.LeerFicha(sha)
	if err != nil {
		return nil, fmt.Errorf("refute: reading the review record: %w", err)
	}
	if ficha == nil || len(ficha.Revisions) == 0 {
		return nil, fmt.Errorf("refute: no review record for SHA %q", sha)
	}
	target, err := review.ResolveDispositionTarget(ficha.Revisions[len(ficha.Revisions)-1], fingerprint)
	if err != nil {
		return nil, fmt.Errorf("refute: %w", err)
	}
	if !review.IsBlocking(target.Severity, target.Status) {
		return nil, fmt.Errorf("refute: finding %q does not block (status %q)", fingerprint, target.Status)
	}
	// The evidence path derives exclusively from the addressed finding: the
	// caller supplies no path, so a refutation cannot be redirected at a
	// file the finding never cited.
	safePath, evidence, rangeHash, err := review.ValidateHumanRefutationRange(
		deps.snapshot, sha, target.Location.Archivo, target.Location.LineaInicio,
		reason, target.Location.Archivo, opts.lineStart, opts.lineEnd)
	if err != nil {
		return nil, fmt.Errorf("refute: %w", err)
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
		TargetLine:        target.Location.LineaInicio,
		TargetDescription: target.Description,
	}
	// Compare-and-append against the authoritative record: the fingerprint
	// above was resolved against a ficha read before any lock, and a
	// concurrent re-audit may have retired it since. The expected bytes are
	// snapshotted here and re-checked under the same ficha lock the
	// revision writer holds; any difference fails closed with nothing
	// persisted. The store append itself runs inside the lock so no writer
	// can slip between the re-check and the write.
	expected, err := json.Marshal(ficha)
	if err != nil {
		return nil, fmt.Errorf("refute: reading the review record: %w", err)
	}
	if deps.beforeLockedAppend != nil {
		deps.beforeLockedAppend()
	}
	if err := deps.ledger.WithLockedFicha(sha, func(current *review.Ficha) error {
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
		return nil
	}); err != nil {
		return nil, err
	}
	return disposition, nil
}

// ejecutarRefute implements `sentinel refute`: parse, resolve, record.
// Exit 0 records one disposition; exit 1 reports the reason and persists
// nothing.
func ejecutarRefute(w io.Writer, worktree string, args []string) int {
	opts, err := parseRefuteArgs(args)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	deps, err := resolveRefuteDeps(worktree)
	if err != nil {
		fmt.Fprintf(w, "❌ Cannot resolve the review ledger: %v\n", err)
		return 1
	}
	disposition, err := runRefutation(deps, opts)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	fmt.Fprintf(w, "✅ Human refutation recorded for finding %q on %s (%s lines %d-%d, range %s).\n",
		disposition.Fingerprint, disposition.SHA, disposition.Path,
		disposition.LineStart, disposition.LineEnd, disposition.RangeHash)
	return 0
}
