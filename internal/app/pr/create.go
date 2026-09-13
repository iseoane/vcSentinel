// Package pr contains the local PR review authoring flow and the persisted-review
// publisher. CI polling is deterministic evidence collection, not semantic review.
package pr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// FlagsPrCreate carries the parsed `pr create` options across the dispatch
// boundary. pr create publishes a previously authored branch judgement; it does
// not own semantic review flags.
type FlagsPrCreate struct {
	Base    string
	ChainPR bool
	Force   bool
	Reason  string
	Parent  string
}

// PrCreateEventDetail builds the detail of the pr-create event. The historical
// unaudited field remains in the event schema for compatibility, but Piece 5
// does not re-analyse the branch and therefore records zero for it.
func PrCreateEventDetail(prURL string, fallback, chain, force bool, reason string, unaudited int) (ops.EventDetail, error) {
	detail := ops.EventDetail{
		"pr_url":    prURL,
		"fallback":  fallback,
		"chain_pr":  chain,
		"force":     force,
		"unaudited": unaudited,
	}
	if force {
		detail["motivo"] = reason
	}
	return detail, nil
}

// DepsPrCreate is the injectable boundary for the publisher. Semantic review
// collaborators are intentionally absent: this command only consumes the
// persisted entry and deterministic CI/validation evidence.
type DepsPrCreate struct {
	LoadConfig      func(worktree string) (config.Config, error)
	GetGitDir       func() (string, error)
	GetGitDirAt     func(worktree string) (string, error)
	GetHeadSHA      func() (string, error)
	GetHeadSHAAt    func(worktree string) (string, error)
	CurrentBranch   func(worktree string) (string, error)
	GetGitCommonDir func(worktree string) (string, error)
	ReadPRReview    func(commonDir, branch string) (*store.PRReviewEntry, error)
	EvidenceAtHEAD  func(worktree, evidencePath string) (bool, string, error)
	RunValidation   func(profile string, scope []string, opts validation.RunOptions) ([]validation.ValidationRun, error)
	RunCI           func(context.Context, io.Writer, string, string, string, config.CIConfig) (CIOutcome, error)
	ComposeBody     func(store.PRReviewEntry, CIOutcome) (string, error)
	PublishStored   func(worktree, title, templatePath, base string) (string, bool, error)
	WriteTemplate   func(string) (string, error)
	RecordEvent     func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error
	RecordDecision  func(commonDir string, d *store.Decision) error
	ResolveActor    func(worktree string) string
}

// RunPrCreateWith validates and publishes exactly one stored Piece 4 review.
// The preflight is intentionally before deterministic validation and every
// remote/publication action: a missing or mismatched judgement cannot cause a
// push, a CI request, or a PR side effect.
func RunPrCreateWith(w io.Writer, worktree string, flags FlagsPrCreate, deps DepsPrCreate, wiring Wiring) int {
	if flags.Force && strings.TrimSpace(flags.Reason) == "" {
		fmt.Fprintln(w, "--force requires --reason with the explicit motive of why the validation is being overridden")
		return 1
	}
	if deps.CurrentBranch == nil {
		fmt.Fprintln(w, "? could not resolve the current branch")
		return 1
	}
	branch, err := deps.CurrentBranch(worktree)
	if err != nil || strings.TrimSpace(branch) == "" {
		if err == nil {
			err = errors.New("current branch is empty")
		}
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	getHead := deps.GetHeadSHAAt
	if getHead == nil && deps.GetHeadSHA != nil {
		getHead = func(string) (string, error) { return deps.GetHeadSHA() }
	}
	if getHead == nil {
		fmt.Fprintln(w, "? could not resolve the current HEAD")
		return 1
	}
	head, err := getHead(worktree)
	if err != nil || !store.IsValidGitObjectID(head) {
		if err == nil {
			err = fmt.Errorf("current HEAD %q is not a canonical Git object ID", head)
		}
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	commonDir, err := resolveCommonDir(worktree, deps)
	if err != nil {
		fmt.Fprintf(w, "? could not resolve the git-common-dir: %v\n", err)
		return 1
	}
	if deps.ReadPRReview == nil {
		fmt.Fprintln(w, "? pr create is not wired to the persisted pr review store")
		return 1
	}
	entry, err := deps.ReadPRReview(commonDir, branch)
	if err != nil {
		fmt.Fprintf(w, "? could not read the stored pr review: %v\n", err)
		return 1
	}
	if entry == nil {
		fmt.Fprintln(w, "No pr review exists for this branch. Run 'sentinel pr review' first: pr create publishes its judgement and never authors one.")
		return 1
	}
	if entry.HeadSHA != head {
		fmt.Fprintf(w, "The stored pr review covers %s, but this branch is now at %s. Re-run 'sentinel pr review'.\n", shortObjectID(entry.HeadSHA), shortObjectID(head))
		return 1
	}
	if _, err := ValidatePRReviewEntry(entry, branch, head); err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if err := validateStoredEvidence(worktree, entry, deps.EvidenceAtHEAD); err != nil {
		fmt.Fprintf(w, "%v\n", err)
		return 1
	}

	loadConfig := deps.LoadConfig
	if loadConfig == nil {
		fmt.Fprintln(w, "? pr create is not wired to configuration")
		return 1
	}
	cfg, err := loadConfig(worktree)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	gitDir, err := resolveGitDir(worktree, deps)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if deps.RunValidation == nil {
		fmt.Fprintln(w, "? pr create is not wired to deterministic validation")
		return 1
	}
	if err := verifyStoredSnapshot(worktree, branch, head, entry, deps); err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	runs, err := deps.RunValidation(wiring.DefaultGateProfile, nil, validation.RunOptions{Worktree: worktree, Cfg: cfg})
	if err != nil {
		fmt.Fprintf(w, "? Could not run the validation: %v\n", err)
		return 1
	}
	findings := validation.Findings(runs, cfg.Validation.Capabilities)
	forcedRedValidation := len(findings) > 0 && flags.Force
	if len(findings) > 0 && !flags.Force {
		fmt.Fprintln(w, "🚨 Red validation: the PR is not published. Commands:")
		for _, finding := range findings {
			fmt.Fprintf(w, "  - ✖ %s (%s):\n%s\n", finding.Capability, finding.Command, strings.TrimSpace(finding.Evidence))
		}
		fmt.Fprintln(w, "Fix the red commands or repeat with --force --reason \"reason\" to publish anyway.")
		return 1
	}
	if forcedRedValidation {
		fmt.Fprintf(w, "⚠️  Red validation overridden with --force (reason: %s).\n", flags.Reason)
		if deps.RecordDecision != nil {
			actor := "unknown"
			if deps.ResolveActor != nil {
				actor = deps.ResolveActor(worktree)
			}
			if err := deps.RecordDecision(commonDir, &store.Decision{
				Decision: store.DecisionForceBypass,
				Actor:    actor,
				At:       time.Now().UTC(),
				Reason:   flags.Reason,
				Scope:    store.ScopePrCreate,
			}); err != nil {
				fmt.Fprintf(w, "⚠️  Warning: could not write the --force decision to decisions.jsonl (%v).\n", err)
			}
		}
	}

	if err := verifyStoredSnapshot(worktree, branch, head, entry, deps); err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}

	outcome := DefaultCIOutcome()
	if strings.TrimSpace(cfg.CI.Workflow) != "" {
		if deps.RunCI == nil {
			fmt.Fprintln(w, "? CI is configured but pr create has no CI runner")
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		outcome, err = deps.RunCI(ctx, w, worktree, branch, head, cfg.CI)
		stop()
		if err != nil {
			fmt.Fprintf(w, "? %v\n", err)
			return 1
		}
	}

	compose := deps.ComposeBody
	if compose == nil {
		compose = ComposePRBody
	}
	body, err := compose(*entry, outcome)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	writeTemplate := deps.WriteTemplate
	if writeTemplate == nil {
		writeTemplate = WritePRTemplate
	}
	templatePath, err := writeTemplate(body)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	base := flags.Base
	if base == "" {
		base = "main"
	}
	if flags.Parent != "" {
		base = flags.Parent
	}
	if err := verifyStoredSnapshot(worktree, branch, head, entry, deps); err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if deps.PublishStored == nil {
		fmt.Fprintln(w, "? pr create is not wired to publication")
		return 1
	}
	prURL, fallback, err := deps.PublishStored(worktree, entry.Title, templatePath, base)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	if fallback {
		if strings.TrimSpace(cfg.CI.Workflow) != "" {
			fmt.Fprintln(w, "? Branch pushed, but no PR was created; the composed body is on the clipboard and in the template file.")
		} else {
			fmt.Fprintln(w, "? Template on the clipboard: create the PR manually with that content.")
		}
	} else {
		fmt.Fprintf(w, "? PR created: %s\n", prURL)
		if err := os.Remove(templatePath); err != nil {
			fmt.Fprintf(w, "? Warning: could not clean up the temporary file (%v).\n", err)
		}
	}

	detail, err := PrCreateEventDetail(prURL, fallback, flags.ChainPR, forcedRedValidation, flags.Reason, 0)
	if err != nil {
		fmt.Fprintf(w, "? Warning: could not build the event detail: %v\n", err)
	}
	if deps.RecordEvent != nil {
		if err := deps.RecordEvent(gitDir, "pr-create", 0, []string{head}, detail, worktree); err != nil {
			fmt.Fprintf(w, "? Warning: could not record the event: %v\n", err)
		}
	}
	return 0
}

func resolveCommonDir(worktree string, deps DepsPrCreate) (string, error) {
	if deps.GetGitCommonDir == nil {
		return "", errors.New("pr create is not wired to the git-common-dir")
	}
	return deps.GetGitCommonDir(worktree)
}

func resolveGitDir(worktree string, deps DepsPrCreate) (string, error) {
	if deps.GetGitDirAt != nil {
		return deps.GetGitDirAt(worktree)
	}
	if deps.GetGitDir != nil {
		return deps.GetGitDir()
	}
	return "", errors.New("pr create is not wired to the Git directory")
}

func shortObjectID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func verifyStoredSnapshot(worktree, branch, head string, entry *store.PRReviewEntry, deps DepsPrCreate) error {
	currentBranch, err := deps.CurrentBranch(worktree)
	if err != nil {
		return fmt.Errorf("could not re-check the current branch: %w", err)
	}
	if currentBranch != branch {
		return fmt.Errorf("current branch changed from %q to %q; re-run sentinel pr review", branch, currentBranch)
	}
	getHead := deps.GetHeadSHAAt
	if getHead == nil && deps.GetHeadSHA != nil {
		getHead = func(string) (string, error) { return deps.GetHeadSHA() }
	}
	if getHead == nil {
		return errors.New("could not re-check the current HEAD")
	}
	currentHead, err := getHead(worktree)
	if err != nil {
		return fmt.Errorf("could not re-check the current HEAD: %w", err)
	}
	if currentHead != head {
		return fmt.Errorf("current HEAD changed from %s to %s; re-run sentinel pr review", shortObjectID(head), shortObjectID(currentHead))
	}
	if _, err := ValidatePRReviewEntry(entry, branch, head); err != nil {
		return err
	}
	return validateStoredEvidence(worktree, entry, deps.EvidenceAtHEAD)
}

func validateStoredEvidence(worktree string, entry *store.PRReviewEntry, check func(string, string) (bool, string, error)) error {
	if len(entry.Evidence) == 0 {
		return fmt.Errorf("? %v: no evidence paths", errStoredReviewInvalid)
	}
	if check == nil {
		return errors.New("? could not validate the pr review evidence: evidence checker is unavailable")
	}
	var missing []string
	for _, evidencePath := range entry.Evidence {
		if err := validateEvidencePath(evidencePath); err != nil {
			return fmt.Errorf("? could not validate the pr review evidence: %v", err)
		}
		ok, reason, err := check(worktree, evidencePath)
		if err != nil {
			return fmt.Errorf("? could not validate the pr review evidence %q: %v", evidencePath, err)
		}
		if !ok {
			missing = append(missing, evidencePath)
			if reason != "" {
				continue
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("The pr review evidence is not committed: %s. Run 'git add .vas_sentinel/evidence && git commit -m \"chore(evidence): record the pr review logs\"' and re-run 'sentinel pr review'.", strings.Join(missing, ", "))
	}
	return nil
}

func validateEvidencePath(value string) error {
	if value == "" || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || filepath.VolumeName(value) != "" {
		return fmt.Errorf("unsafe evidence path %q", value)
	}
	clean := path.Clean(value)
	if clean != value || !strings.HasPrefix(value, ".vas_sentinel/evidence/") {
		return fmt.Errorf("unsafe evidence path %q", value)
	}
	return nil
}
