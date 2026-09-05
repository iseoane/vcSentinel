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

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// reopenOptions carries the non-interactive human reopen request: the
// reviewed SHA plus the finding's stable fingerprint address the finding,
// and the reason plus the immutable snapshot line range carry the answer.
// The evidence itself is always read from the audited Git object, never
// supplied by the caller — the same gate as refutation, because reopening
// re-blocks and must stay evidence-bound rather than becoming a free
// override. It needs its own path because runRefutation rejects the
// non-blocking finding that is exactly a reopen's target.
type reopenOptions struct {
	sha         string
	fingerprint string
	reason      string
	lineStart   int
	lineEnd     int
}

func parseReopenArgs(args []string) (reopenOptions, error) {
	var opts reopenOptions
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
				return opts, fmt.Errorf("--reason requires a value (why the finding applies again)")
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
			return opts, fmt.Errorf("unknown flag %q (usage: sentinel reopen --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M)", args[i])
		}
	}
	if strings.TrimSpace(opts.sha) == "" {
		return opts, fmt.Errorf("missing required --sha (the reviewed commit SHA)")
	}
	if strings.TrimSpace(opts.fingerprint) == "" {
		return opts, fmt.Errorf("missing required --fingerprint (the finding's stable fingerprint)")
	}
	if strings.TrimSpace(opts.reason) == "" {
		return opts, fmt.Errorf("missing required --reason (why the finding applies again)")
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

// runReopen records one evidence-bound human reopen. Every failure before
// append is closed without persisting anything: a missing review record, a
// missing or ambiguous fingerprint, a still-blocking finding (nothing to
// reopen), a finding with no clearing answer to reopen, a range the evidence
// gate rejects, an unreadable snapshot, a review record that changed between
// resolution and append, or a corrupt dispositions log.
func runReopen(deps *refuteDeps, opts reopenOptions) (refuteOutcome, error) {
	if deps == nil || deps.ledger == nil || deps.store == nil || deps.snapshot == nil {
		return refuteOutcome{}, fmt.Errorf("reopen: missing dependencies")
	}
	now := time.Now
	if deps.now != nil {
		now = deps.now
	}
	sha := strings.TrimSpace(opts.sha)
	fingerprint := strings.TrimSpace(opts.fingerprint)
	reason := strings.TrimSpace(opts.reason)
	if sha == "" || fingerprint == "" || reason == "" {
		return refuteOutcome{}, fmt.Errorf("reopen: --sha, --fingerprint, and --reason are required")
	}
	if strings.HasPrefix(sha, "-") {
		return refuteOutcome{}, fmt.Errorf("reopen: invalid reviewed SHA")
	}
	ficha, err := deps.ledger.LeerFicha(sha)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("reopen: reading the review record: %w", err)
	}
	if ficha == nil || len(ficha.Revisions) == 0 {
		return refuteOutcome{}, fmt.Errorf("reopen: no review record for SHA %q", sha)
	}
	target, err := review.ResolveDispositionTarget(ficha.Revisions[len(ficha.Revisions)-1], fingerprint)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("reopen: %w", err)
	}
	// The evidence path derives exclusively from the addressed finding: the
	// caller supplies no path, so a reopen cannot be redirected at a file
	// the finding never cited.
	safePath, evidence, rangeHash, err := review.ValidateHumanRefutationRange(
		deps.snapshot, sha, target.Location.Archivo, target.Location.LineaInicio,
		reason, target.Location.Archivo, opts.lineStart, opts.lineEnd)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("reopen: %w", err)
	}
	disposition := &review.FindingDisposition{
		SHA:               sha,
		Fingerprint:       review.EffectiveFingerprint(target),
		Status:            review.StatusReopened,
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
	// Compare-and-append against the authoritative record, mirroring
	// runRefutation: the effective precondition below is evaluated under the
	// same ficha lock the revision writer holds.
	expected, err := json.Marshal(ficha)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("reopen: reading the review record: %w", err)
	}
	if deps.beforeLockedAppend != nil {
		deps.beforeLockedAppend()
	}
	withLockedFicha := deps.ledger.WithLockedFicha
	if deps.withLockedFicha != nil {
		withLockedFicha = deps.withLockedFicha
	}
	completed := false
	if err := withLockedFicha(sha, func(current *review.Ficha) error {
		fresh, err := json.Marshal(current)
		if err != nil {
			return fmt.Errorf("reopen: reading the review record: %w", err)
		}
		if !bytes.Equal(fresh, expected) {
			return fmt.Errorf("reopen: the review record changed during reopen; re-resolve the finding and retry")
		}
		dispositions, err := deps.store.ReadDispositions()
		if err != nil {
			return fmt.Errorf("reopen: reading dispositions before append: %w", err)
		}
		currentRevision := current.Revisions[len(current.Revisions)-1]
		effectiveTarget, err := review.ResolveDispositionTargetWithDispositions(
			currentRevision, fingerprint, review.FilterDispositionsForSHA(dispositions, sha))
		if err != nil {
			return fmt.Errorf("reopen: %w", err)
		}
		// Only a cleared finding can be reopened: refuted or fixed are the
		// states that stopped blocking. A still-blocking finding has
		// nothing to reopen, and any other status was never cleared.
		if review.IsBlocking(effectiveTarget.Severity, effectiveTarget.Status) {
			return fmt.Errorf("reopen: finding %q still blocks (status %q): nothing to reopen", fingerprint, effectiveTarget.Status)
		}
		switch review.NormalizeStatus(effectiveTarget.Status) {
		case review.StatusRefuted, review.StatusFixed:
		default:
			return fmt.Errorf("reopen: finding %q was never cleared (status %q)", fingerprint, effectiveTarget.Status)
		}
		if err := deps.store.AppendDisposition(disposition); err != nil {
			return fmt.Errorf("reopen: persisting the disposition: %w", err)
		}
		completed = true
		return nil
	}); err != nil {
		if completed && errors.Is(err, review.ErrBloqueoNoLiberado) {
			return refuteOutcome{
				disposition:       disposition,
				completionWarning: fmt.Errorf("reopen: disposition recorded but ficha lock cleanup failed: %w", err),
			}, nil
		}
		return refuteOutcome{}, err
	}
	return refuteOutcome{disposition: disposition}, nil
}

// ejecutarReopen implements `sentinel reopen`: parse, resolve, record.
// Exit 0 records one disposition, including the completed-but-warning case
// where only the ficha lock cleanup failed after persistence.
func ejecutarReopen(w io.Writer, worktree string, args []string) int {
	opts, err := parseReopenArgs(args)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	deps, err := refuteDepsResolver(worktree)
	if err != nil {
		fmt.Fprintf(w, "❌ Cannot resolve the review ledger: %v\n", err)
		return 1
	}
	outcome, err := runReopen(deps, opts)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	return reportReopenResult(w, outcome)
}

func reportReopenResult(w io.Writer, outcome refuteOutcome) int {
	if outcome.disposition == nil {
		fmt.Fprintln(w, "❌ reopen completed without a disposition")
		return 1
	}
	disposition := outcome.disposition
	fmt.Fprintf(w, "✅ Human reopen recorded for finding %q on %s (%s lines %d-%d, range %s). The finding blocks again.\n",
		disposition.Fingerprint, disposition.SHA, disposition.Path,
		disposition.LineStart, disposition.LineEnd, disposition.RangeHash)
	if outcome.completionWarning != nil {
		fmt.Fprintf(w, "⚠️  The reopen is recorded, but ficha lock cleanup failed: %v\n", outcome.completionWarning)
	}
	return 0
}
