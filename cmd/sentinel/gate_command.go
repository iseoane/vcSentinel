package main

import (
	"fmt"
	"io"
	"math"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/secret"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// validGateStages are the only values accepted by --stage. --stage
// identifies the lifecycle point that invokes the gate (for
// messages/event recording), not which validation profile runs:
// --stage and --profile are independent axes (T1.7 design decision).
var validGateStages = map[string]bool{
	"pre-commit": true,
	"pre-push":   true,
	"pr":         true,
}

// defaultGateProfile is the validation.profiles profile that --profile uses
// when none is given explicitly. T1.7 design decision (the ticket does not
// specify it): "standard" by convention; if it does not exist in the
// configuration, runGate cuts with an explicit error instead of assuming any
// other arbitrary profile.
const defaultGateProfile = "standard"

// runGate parses --stage/--profile, loads the STRICT config (the entry point
// where an unknown key in the yml stops being discarded silently, requirement
// added by the orchestrator for F1's exit criterion #4) and delegates to
// internal/gate the fixed order validation → semantic review of HEAD. It
// returns the exit code without calling os.Exit (same pattern as
// runSlicePlan/runSliceApply) so it can be tested without ending the
// process.
func runGate(w io.Writer, worktree string, args []string) int {
	stage, profile, err := parseGateFlags(args)
	if err != nil {
		fmt.Fprintf(w, "❌ %v\n", err)
		return 1
	}

	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		// A broken yml is configuration infrastructure, not a validation or
		// semantic review finding (requirement added by the orchestrator,
		// outside the original text of T1.7).
		fmt.Fprintf(w, "❌ Configuration error: %v\n", err)
		return finalizeGate(w, worktree, stage, gate.StateInfrastructureError, nil)
	}

	if _, ok := cfg.Validation.Profiles[profile]; !ok {
		fmt.Fprintf(w, "❌ The validation profile %q is not configured. Define validation.profiles.%s in vassentinel.yml or pass --profile with an existing profile.\n", profile, profile)
		return finalizeGate(w, worktree, stage, gate.StateInfrastructureError, nil)
	}

	sha, message, diff, files, err := headCommitData()
	if err != nil {
		fmt.Fprintf(w, "❌ Could not read HEAD: %v\n", err)
		return finalizeGate(w, worktree, stage, gate.StateInfrastructureError, nil)
	}
	changeProfile, err := change.ComputeCommitProfile(sha)
	if err != nil {
		fmt.Fprintf(w, "❌ Could not derive the change profile of HEAD: %v\n", err)
		return finalizeGate(w, worktree, stage, gate.StateInfrastructureError, nil)
	}

	// Fail closed. git.Attributes already returns "" with no error when the
	// tree has no .gitattributes, so an error here is a real read failure, and
	// the attributes now decide route classification and therefore whether the
	// security and concurrency bundles are scheduled at all. Continuing with
	// empty evidence would let the gate succeed on an under-classified plan.
	attributes, err := readAttributesGate(sha)
	if err != nil {
		fmt.Fprintf(w, "❌ Could not read the attributes of %s: %v\n", sha, err)
		return finalizeGate(w, worktree, stage, gate.StateInfrastructureError, nil)
	}

	options := buildGateOptions(cfg, worktree, profile, GateEvidence{
		SHA: sha, Message: message, Diff: diff, Gitattributes: attributes,
		Profile: changeProfile, Files: files,
	})
	applyDurableCutover(&options, worktree, stage, sha)

	result := gate.RunGate(options)

	fmt.Fprintf(w, "🚦 gate [%s] profile=%s → %s\n", stage, profile, result.State)
	// FU-11: the credential incident surfaces next to the gate result on
	// every path, including when validation short-circuits. It is a
	// deterministic scan and not a semantic dimension, so piece 3 leaves it
	// exactly where it was: gate keeps reporting it, advisory only, with
	// State and Messages untouched.
	_, secretAdvisories := secret.SecretFindingsAndAdvisories(files, diff)
	for _, advisory := range secretAdvisories {
		fmt.Fprintln(w, advisory)
	}
	return finalizeGateWithDetails(w, worktree, stage, result.State, result.Messages, result.ContextSkipReason)
}

// buildGateOptions assembles the gate.Options the command hands to
// internal/gate: validation profile, HEAD-derived revision inputs, the real
// reviewer seams, and the per-commit review transport (nil unless
// review.durable_routes is on). Shared by runGate and the cutover tests,
// so tests exercise the exact production construction.
// GateEvidence groups what the gate derives from HEAD. It is a struct and not
// a parameter list because those were six adjacent strings: a forgotten,
// shifted or swapped argument compiled the same, and one of them decides
// whether the security review is scheduled. The compiler no longer allows it.
type GateEvidence struct {
	SHA           string
	Message       string
	Diff          string
	Gitattributes string
	Profile       change.ChangeProfile
	Files         []string
}

func buildGateOptions(cfg config.Config, worktree, profile string, evidence GateEvidence) gate.Options {
	return gate.Options{
		Profile:      profile,
		ChangedPaths: evidence.Files,
		ValidationOptions: validation.RunOptions{
			Worktree: worktree,
			Cfg:      cfg,
			ProviderGraph: func(snapshot, treeOID string) graph.GraphProvider {
				return graph.NewNativeProvider(snapshot, treeOID)
			},
		},
	}
}

// gateRefuterFactory resolves the explicit cheap profile separately from the
// per-dimension auditor so each semantic CRITICAL gets an independent refuter.
func gateRefuterFactory(cfg config.Config, verifier *modelprobe.Verifier) review.RefuterFactory {
	return refuterFactory(cfg, verifier)
}

// gateAuditorFactory builds the agent of each dimension of the
// semantic review with the contract-selected provider profile, without a
// review override: gate --profile selects a validation profile only.
func gateAuditorFactory(cfg config.Config, verifier *modelprobe.Verifier) review.ReviewerFactory {
	return func(_ review.ReviewBundle, dimension string) (review.AgentReviewer, string, error) {
		profile := config.ResolveProfile(cfg, reviewcontract.DefaultProfile(dimension), "")
		adapter, err := agentadapter.NewAdapterWithProfile(cfg, profile)
		if err != nil {
			return nil, profile.Name, err
		}
		verifier.Verify(profile.Name, profile.Model, adapter)
		return adapter, profile.Name, nil
	}
}

// readAttributesGate is the gate's .gitattributes read seam: a variable so a
// test can exercise the fail-closed branch, the one that decides whether the
// gate continues with an under-classified plan.
var readAttributesGate = git.Attributes

// headCommitData resolves the SHA of HEAD and reads its message/diff/files:
// the gate always audits HEAD (unlike 'review', which accepts an explicit
// target), because that is the point pre-commit/pre-push/pr already froze.
func headCommitData() (sha, message, diff string, files []string, err error) {
	sha, err = git.ResolveSHA("HEAD")
	if err != nil {
		return "", "", "", nil, err
	}
	message, err = git.CommitMessage(sha)
	if err != nil {
		return "", "", "", nil, err
	}
	diff, err = git.DiffCommit(sha)
	if err != nil {
		return "", "", "", nil, err
	}
	files, err = git.FilesOfCommit(sha)
	if err != nil {
		return "", "", "", nil, err
	}
	return sha, message, diff, files, nil
}

// finalizeGate prints the messages of the result, records the event with the
// final state (mechanism that already exists in internal/ops, same pattern as
// runReview) and returns the exact exit code of the record.
func finalizeGate(w io.Writer, worktree, stage, state string, messages []string) int {
	return finalizeGateWithDetails(w, worktree, stage, state, messages, "")
}

func finalizeGateWithDetails(w io.Writer, worktree, stage, state string, messages []string, contextSkipReason string) int {
	for _, message := range messages {
		fmt.Fprintln(w, message)
	}
	recordGateEventWithDetails(worktree, stage, state, contextSkipReason)
	if stage == "pre-push" {
		// T9.5 event-driven retention: a push is the moment published
		// work stops being in-flight, so the pass runs after the gate
		// decides, for every verdict. Verdict-independence is safe by
		// construction: the predicate collects only commits that are
		// already ancestors of origin/main, so a blocked gate keeps
		// every stream of the rejected HEAD (still unpublished and
		// still cited) while collecting older published detail.
		// Best-effort by contract: it reports to the same writer and
		// never alters the exit code computed below.
		tryRetentionAfterPublish(w, worktree)
	}
	return gate.ExitCode(state)
}

// recordGateEventWithDetails appends operational metadata to the repository
// that owns worktree. An unresolved Git directory is intentionally ignored:
// writing relative to the process directory could contaminate another
// repository's event log.
func recordGateEventWithDetails(worktree, stage, state, contextSkipReason string) {
	gitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		return
	}
	detail := ops.EventDetail{"stage": stage, "state": state}
	if contextSkipReason != "" {
		detail["context_skip_reason"] = contextSkipReason
	}
	_ = ops.RecordEvent(gitDir, "gate", gate.ExitCode(state), nil, detail, worktree)
}

// maxTimeoutSeconds is the largest --timeout value time.Duration can
// represent. Without this ceiling a positive, perfectly parseable value
// overflows when multiplied by time.Second and becomes a negative duration,
// so the override would be accepted without representing what was asked.
//
// It is int64 and not int on purpose: the value does not fit in a 32-bit int,
// and declaring it that way used to make the whole package FAIL TO COMPILE on
// those targets even though time.Duration stayed int64 and could represent
// it. On 32 bits the check turns out to be unreachable —the maximum of an int
// is smaller— and that is correct: there no parseable value can overflow.
const maxTimeoutSeconds int64 = math.MaxInt64 / int64(time.Second)

// parseGateFlags extracts --stage (required, fixed values), --profile
// (optional, defaultGateProfile if omitted) and --timeout (optional).
//
// --timeout has exactly the same semantics as in review: it overrides
// review.timeout ONLY in this invocation. The gate runs the same semantic
// review, so denying it the lever forced editing the yml —a global,
// persistent change— for one particular large candidate. Widening the budget
// weakens no gate: a review that was going to block still blocks, it just
// gets to finish.
func parseGateFlags(args []string) (stage, profile string, err error) {
	profile = defaultGateProfile
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--stage":
			i++
			if i >= len(args) {
				return "", "", fmt.Errorf("--stage requires a value (pre-commit, pre-push, or pr)")
			}
			stage = args[i]
		case "--profile":
			i++
			if i >= len(args) {
				return "", "", fmt.Errorf("--profile requires a value")
			}
			profile = args[i]
		case "--timeout":
			// Piece 3 retired the semantic phase, and --timeout only ever
			// widened its budget. There is nothing left for it to extend, so
			// it is refused rather than accepted and ignored: a flag that is
			// silently a no-op tells a script its request was honoured when
			// it was not. Refusing is louder, and loud is the honest failure
			// for a guardian.
			return "", "", fmt.Errorf("--timeout no longer applies to gate: it extended the semantic review budget, and gate is deterministic since it stopped auditing. Use `sentinel review --timeout` for the per-commit audit")
		default:
			return "", "", fmt.Errorf("unrecognized flag: %q (use --stage and --profile)", args[i])
		}
	}
	if stage == "" {
		return "", "", fmt.Errorf("--stage is required (values: pre-commit, pre-push, pr)")
	}
	if !validGateStages[stage] {
		return "", "", fmt.Errorf("--stage %q is not valid (values: pre-commit, pre-push, pr)", stage)
	}
	return stage, profile, nil
}
