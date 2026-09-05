package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// acceptOptions carries the non-interactive human acceptance request: the
// reviewed SHA plus the finding's stable fingerprint address the finding,
// and the reason carries the judgement. Acceptance records that a person saw
// the risk and acknowledged it; it never clears the block. There is no
// evidence range because there is no premise to refute.
type acceptOptions struct {
	sha         string
	fingerprint string
	reason      string
}

func parseAcceptArgs(args []string) (acceptOptions, error) {
	var opts acceptOptions
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
				return opts, fmt.Errorf("--reason requires a value (why the risk is acknowledged)")
			}
			opts.reason = args[i]
		default:
			return opts, fmt.Errorf("unknown flag %q (usage: sentinel accept --sha SHA --fingerprint FP --reason TEXT)", args[i])
		}
	}
	if strings.TrimSpace(opts.sha) == "" {
		return opts, fmt.Errorf("missing required --sha (the reviewed commit SHA)")
	}
	if strings.TrimSpace(opts.fingerprint) == "" {
		return opts, fmt.Errorf("missing required --fingerprint (the finding's stable fingerprint)")
	}
	if strings.TrimSpace(opts.reason) == "" {
		return opts, fmt.Errorf("missing required --reason (why the risk is acknowledged)")
	}
	return opts, nil
}

// runAcceptance records one human acceptance. Every failure before append is
// closed without persisting anything: a missing review record, a missing or
// ambiguous fingerprint, an already-answered finding, an unsafe finding path,
// or a corrupt dispositions log. Acceptance never clears a block, so it
// needs no evidence gate and no snapshot; the compare-and-append discipline
// matches runRefutation so a concurrent re-audit still fails closed.
func runAcceptance(deps *refuteDeps, opts acceptOptions) (refuteOutcome, error) {
	if deps == nil || deps.ledger == nil || deps.store == nil {
		return refuteOutcome{}, fmt.Errorf("accept: missing dependencies")
	}
	now := time.Now
	if deps.now != nil {
		now = deps.now
	}
	sha := strings.TrimSpace(opts.sha)
	fingerprint := strings.TrimSpace(opts.fingerprint)
	reason := strings.TrimSpace(opts.reason)
	if sha == "" || fingerprint == "" || reason == "" {
		return refuteOutcome{}, fmt.Errorf("accept: --sha, --fingerprint, and --reason are required")
	}
	if strings.HasPrefix(sha, "-") {
		return refuteOutcome{}, fmt.Errorf("accept: invalid reviewed SHA")
	}
	ficha, err := deps.ledger.LeerFicha(sha)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("accept: reading the review record: %w", err)
	}
	if ficha == nil || len(ficha.Revisions) == 0 {
		return refuteOutcome{}, fmt.Errorf("accept: no review record for SHA %q", sha)
	}
	target, err := review.ResolveDispositionTarget(ficha.Revisions[len(ficha.Revisions)-1], fingerprint)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("accept: %w", err)
	}
	// The acceptance path derives exclusively from the addressed finding: the
	// caller supplies no path, so an acceptance cannot be redirected at a
	// file the finding never cited. Unsafe finding paths fail closed here,
	// exactly as the refutation gate refuses them there.
	if len(review.RutasRevisionSeguras([]string{target.Location.Archivo})) != 1 {
		return refuteOutcome{}, fmt.Errorf("accept: the finding path %q is unsafe", target.Location.Archivo)
	}
	disposition := &review.FindingDisposition{
		SHA:               sha,
		Fingerprint:       review.EffectiveFingerprint(target),
		Status:            review.StatusAcceptedByUser,
		Reason:            reason,
		Path:              target.Location.Archivo,
		Actor:             review.RefutationActorHuman,
		Source:            review.DispositionSourceHuman,
		At:                now().UTC(),
		TargetDimension:   target.Dimension,
		TargetLine:        target.Location.LineaInicio,
		TargetDescription: target.Description,
	}
	// Compare-and-append against the authoritative record, mirroring
	// runRefutation: the fingerprint above was resolved against a ficha read
	// before any lock, and a concurrent re-audit may have retired it since.
	expected, err := json.Marshal(ficha)
	if err != nil {
		return refuteOutcome{}, fmt.Errorf("accept: reading the review record: %w", err)
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
			return fmt.Errorf("accept: reading the review record: %w", err)
		}
		if !bytes.Equal(fresh, expected) {
			return fmt.Errorf("accept: the review record changed during acceptance; re-resolve the finding and retry")
		}
		dispositions, err := deps.store.ReadDispositions()
		if err != nil {
			return fmt.Errorf("accept: reading dispositions before append: %w", err)
		}
		currentRevision := current.Revisions[len(current.Revisions)-1]
		effectiveTarget, err := review.ResolveDispositionTargetWithDispositions(
			currentRevision, fingerprint, review.FilterDispositionsForSHA(dispositions, sha))
		if err != nil {
			return fmt.Errorf("accept: %w", err)
		}
		// Only an unanswered finding can be accepted. Anything already
		// answered — accepted, refuted, fixed, or reopened — fails closed
		// so a second answer cannot accumulate against settled judgement.
		switch review.NormalizeStatus(effectiveTarget.Status) {
		case review.StatusPending, review.StatusConfirmed, "":
		default:
			return fmt.Errorf("accept: finding %q is already answered (status %q)", fingerprint, effectiveTarget.Status)
		}
		if err := deps.store.AppendDisposition(disposition); err != nil {
			return fmt.Errorf("accept: persisting the disposition: %w", err)
		}
		completed = true
		return nil
	}); err != nil {
		if completed && errors.Is(err, review.ErrBloqueoNoLiberado) {
			return refuteOutcome{
				disposition:       disposition,
				completionWarning: fmt.Errorf("accept: disposition recorded but ficha lock cleanup failed: %w", err),
			}, nil
		}
		return refuteOutcome{}, err
	}
	return refuteOutcome{disposition: disposition}, nil
}

// ejecutarAccept implements `sentinel accept`: parse, resolve, record.
// Exit 0 records one disposition, including the completed-but-warning case
// where only the ficha lock cleanup failed after persistence.
func ejecutarAccept(w io.Writer, worktree string, args []string) int {
	opts, err := parseAcceptArgs(args)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	deps, err := refuteDepsResolver(worktree)
	if err != nil {
		fmt.Fprintf(w, "❌ Cannot resolve the review ledger: %v\n", err)
		return 1
	}
	outcome, err := runAcceptance(deps, opts)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}
	return reportAcceptanceResult(w, outcome)
}

func reportAcceptanceResult(w io.Writer, outcome refuteOutcome) int {
	if outcome.disposition == nil {
		fmt.Fprintln(w, "❌ accept completed without a disposition")
		return 1
	}
	disposition := outcome.disposition
	fmt.Fprintf(w, "✅ Human acceptance recorded for finding %q on %s (%s). The block stands: acceptance documents judgement, never clears it.\n",
		disposition.Fingerprint, disposition.SHA, disposition.Path)
	if outcome.completionWarning != nil {
		fmt.Fprintf(w, "⚠️  The acceptance is recorded, but ficha lock cleanup failed: %v\n", outcome.completionWarning)
	}
	return 0
}
