package main

// FU-2/C6 moved the pr orchestration (review and create) to internal/app/pr as
// a pure one-subcommand split: this file keeps the flag parsing, the option
// and seam structs the package-main tests construct, the production wiring
// (realPrCreateDeps, wiringPr) and thin dispatch wrappers into the flows.
// Movement hazard cleared: the 21 live block records cite snapshot.go,
// comandos_runs.go and siblings — none cites cmd/vcsentinel/comandos_pr.go —
// and there are no standing dispositions (no dispositions.jsonl), so moving
// the orchestration out of this file orphans nothing.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/app/pr"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vcSentinel/internal/ops"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
	"github.com/ISeoane-Quental/vcSentinel/internal/validation"
)

// flagsPrReview are the options of pr review.
type flagsPrReview struct {
	base     string
	overview bool // --overview
	jsonOut  bool // --json
	parent   string
}

func parseParentFlagValue(args []string, at int) (string, error) {
	if at >= len(args) || strings.TrimSpace(args[at]) == "" || strings.HasPrefix(args[at], "-") {
		return "", fmt.Errorf("--parent requires a non-blank branch value (the stacked parent branch)")
	}
	return args[at], nil
}

// parsePrReviewFlags parses the pr review options with the same simple
// "flag value" pair syntax as the rest of the subcommands.
func parsePrReviewFlags(args []string) (flagsPrReview, error) {
	var flags flagsPrReview
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--base":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("--base requires a value (the comparison branch)")
			}
			i++
			flags.base = args[i]
		case "--parent":
			i++
			val, err := parseParentFlagValue(args, i)
			if err != nil {
				return flags, err
			}
			flags.parent = val
		case "--only-unaudited":
			return flags, fmt.Errorf("--only-unaudited was retired: pr review no longer audits commits; run `vcsentinel review <sha>` for each unaudited commit")
		case "--audit-pending":
			return flags, fmt.Errorf("--audit-pending was retired: pr review no longer audits commits, and never audits them on your behalf. Run `vcsentinel review <sha>` for each unaudited commit; `vcsentinel pr review` reports which ones they are.")
		case "--overview":
			flags.overview = true
		case "--json":
			flags.jsonOut = true
		default:
			return flags, fmt.Errorf("unknown option for pr review: %s", arg)
		}
	}
	return flags, nil
}

// flagsPrCreate are the options of pr create.
type flagsPrCreate struct {
	base    string
	chainPR bool   // --chain-pr: publish the whole branch even if it is oversized
	force   bool   // --force: override a red validation (T1.8: the only gate that blocks)
	reason  string // --reason: explicit and mandatory motive next to --force
	parent  string
}

// parsePrCreateFlags parses the pr create options with the same simple
// "flag value" pair syntax as the rest of the subcommands. --force requires
// --reason (T1.8): with the semantic verdict already advisory, the validation
// is the only real gate that can be forced, and forcing it without a motive
// would not be recorded in the event in any useful form.
func parsePrCreateFlags(args []string) (flagsPrCreate, error) {
	var flags flagsPrCreate
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--base":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("--base requires a value (the comparison branch)")
			}
			i++
			flags.base = args[i]
		case "--parent":
			i++
			val, err := parseParentFlagValue(args, i)
			if err != nil {
				return flags, err
			}
			flags.parent = val
		case "--chain-pr":
			flags.chainPR = true
		case "--audit-pending":
			return flags, fmt.Errorf("--audit-pending was retired: pr review no longer audits commits, and never audits them on your behalf. Run `vcsentinel review <sha>` for each unaudited commit; `vcsentinel pr review` reports which ones they are.")
		case "--force":
			flags.force = true
		case "--reason":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("--reason requires a value (the motive of --force)")
			}
			i++
			flags.reason = args[i]
		default:
			return flags, fmt.Errorf("unknown option for pr create: %s", arg)
		}
	}
	if flags.force && flags.reason == "" {
		return flags, fmt.Errorf("--force requires --reason with the explicit motive of why the validation is being overridden")
	}
	return flags, nil
}

func retiredPassthroughDisposition() (string, int) {
	return "The legacy 'vcsentinel pr [gh arguments]' passthrough was removed because it bypassed the guardian's review flow. Use 'vcsentinel pr review' to author and persist a local judgement and evidence, or 'vcsentinel pr create' to publish a reviewed pull request.", 1
}

func prVerb(args []string) string {
	if len(args) > 0 {
		switch args[0] {
		case "review":
			return "review"
		case "create":
			return "create"
		}
	}
	return ""
}

func runPr(worktree string, args []string) {
	switch prVerb(args) {
	case "review":
		runPrReview(worktree, args[1:])
	case "create":
		runPrCreate(worktree, args[1:])
	default:
		msg, code := retiredPassthroughDisposition()
		fmt.Println(msg)
		os.Exit(code)
	}
}

// The adapters below translate the package-main option structs into their
// exported counterparts inside internal/app/pr. They are dumb field mappings:
// the orchestration itself lives across the boundary.

func flagsPrReviewToPr(f flagsPrReview) pr.FlagsPrReview {
	return pr.FlagsPrReview{
		Base:     f.base,
		Overview: f.overview,
		JsonOut:  f.jsonOut,
		Parent:   f.parent,
	}
}

func flagsPrCreateToPr(f flagsPrCreate) pr.FlagsPrCreate {
	return pr.FlagsPrCreate{
		Base:    f.base,
		ChainPR: f.chainPR,
		Force:   f.force,
		Reason:  f.reason,
		Parent:  f.parent,
	}
}

func depsPrCreateToPr(d depsPrCreate) pr.DepsPrCreate {
	return pr.DepsPrCreate{
		LoadConfig:       d.loadConfig,
		RunValidation:    d.runValidation,
		RecordEvent:      d.recordEvent,
		GetGitCommonDir:  d.getGitCommonDir,
		RecordDecision:   d.recordDecision,
		ResolveActor:     d.resolveActor,
		ResolveParent:    d.resolveParent,
		WriteTemplate:    d.writeTemplate,
		GetGitDirAt:      d.getGitDirAt,
		GetHeadSHAAt:     d.getHeadSHAAt,
		CurrentBranch:    d.currentBranch,
		ReadPRReview:     d.readPRReview,
		EvidenceAtHEAD:   d.evidenceAtHEAD,
		RunCI:            d.runCI,
		ComposeBody:      d.composeBody,
		PublishStored:    d.publishStored,
		RemoteBranchHead: d.remoteBranchHead,
	}
}

func detailPrReviewEvent(base string, res *review.BranchResult, ci bool) (ops.EventDetail, error) {
	return pr.PrReviewEventDetail(base, res, ci)
}

func applyPrReviewDispositions(options review.BranchOptions, worktree string, load func(string) ([]review.FindingDisposition, error)) (review.BranchOptions, error) {
	return pr.ApplyPrReviewDispositions(options, worktree, load)
}

func decisionText(decision string, volume int) string {
	return pr.DecisionText(decision, volume)
}

func prReviewJSONOutput(base string, res *review.BranchResult) map[string]any {
	return pr.PrReviewJSONOutput(base, res)
}

// branchReviewOptions assembles the pr review branch options; the assembler
// lives in internal/app/pr and receives the package-main collaborators through
// wiringPr. progress is the injected spinner channel: the cmd caller routes it
// (payload writer normally, stderr in --json mode).
func branchReviewOptions(cfg config.Config, verifier *modelprobe.Verifier, worktree string, flags flagsPrReview, factory review.ReviewerFactory, progress io.Writer) (review.BranchOptions, error) {
	return pr.BranchPrReviewOptions(cfg, verifier, worktree, flagsPrReviewToPr(flags), factory, wiringPr(), progress)
}

// runPrReview analyzes the branch against the base, then authors and persists
// its local judgement and evidence. It does not publish a pull request. It
// records the pr-review event when done; the flow lives in internal/app/pr. It
// owns the JSON-safe routing here: the payload writer carries the human motion
// (⏳ spinners, warnings) normally, and stderr carries it in --json mode so byte
// 0 of stdout stays '{'.
func runPrReview(worktree string, args []string) {
	flags, err := parsePrReviewFlags(args)
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
	progress := io.Writer(os.Stdout)
	if flags.jsonOut {
		progress = os.Stderr
	}
	pr.RunPrReview(os.Stdout, progress, worktree, flagsPrReviewToPr(flags), wiringPr())
}

func detailPrCreateEvent(prURL string, fallback, chain, force bool, reason string, unaudited int) (ops.EventDetail, error) {
	return pr.PrCreateEventDetail(prURL, fallback, chain, force, reason, unaudited)
}

// resolveActor identifies who runs the process, for the traceability of T7.5
// decisions (M3 report: --force with no trace of who or why). No identity
// helper existed in the codebase (verified): it tries `git config user.name`
// first (with cmd.Dir=worktree, NEVER the process cwd: if the worktree
// differs from the current repository and that other repository defines its
// own local user.name, the decision would be attributed to the wrong actor),
// falls back to $USER (POSIX) / $USERNAME (Windows) if it is empty or fails,
// and uses an explicit placeholder as the last resort instead of leaving the
// field empty.
func resolveActor(worktree string) string {
	cmd := exec.Command("git", "config", "user.name")
	cmd.Dir = worktree
	if output, err := cmd.Output(); err == nil {
		if name := strings.TrimSpace(string(output)); name != "" {
			return name
		}
	}
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	if u := strings.TrimSpace(os.Getenv("USERNAME")); u != "" {
		return u
	}
	return "unknown"
}

// verifyForTemplate runs the honest verification (guide §12.3) and translates
// it to the template section: real exit codes per configured command, the
// agent's tested contract or the motive for omission. A verification failure
// is NEVER silent: it is reflected as a motive in the template so the PR is
// transparent about what was checked.
func verifyForTemplate(worktree, gitDir string, cfg config.Config) review.TemplateVerification {
	return verifyForTemplateWith(worktree, gitDir, cfg, newModelVerifier(worktree), ops.Verify)
}

// verifyForTemplateWith delegates to internal/app/pr; the model-probe
// constructor travels as a closure so the package-main var is read at call
// time (tests swap it).
func verifyForTemplateWith(worktree, gitDir string, cfg config.Config, modelVerifier *modelprobe.Verifier,
	verify func(ops.VerifyOptions) (ops.VerificationResult, error)) review.TemplateVerification {
	return pr.VerifyForTemplateWith(worktree, gitDir, cfg, modelVerifier, verify,
		func(worktree string) *modelprobe.Verifier { return newModelVerifier(worktree) })
}

// copyToClipboard copies text to the system clipboard according to the
// platform: clip (Windows), wl-copy (Wayland), xclip (X11).
func copyToClipboard(text string) error {
	return copyToClipboardWith(text,
		func(name string) bool { _, err := exec.LookPath(name); return err == nil },
		func(name, content string) error {
			c := exec.Command(name)
			c.Stdin = strings.NewReader(content)
			return c.Run()
		})
}

func copyToClipboardWith(text string, available func(string) bool, run func(string, string) error) error {
	return pr.CopyToClipboardWith(text, available, run)
}

// publishPROptions groups the injectable dependencies of publishPRStoredWith:
// ghAvailable decides whether gh is on the PATH, runGh launches gh and returns
// its output (full args, including the worktree as cwd), copy is used only in
// the fallback (clipboard).
type publishPROptions struct {
	ghAvailable func(string) bool
	runGh       func(worktree string, args ...string) ([]byte, error)
	copy        func(string) error
}

func publishPRStored(worktree, title, templatePath, base string) (string, bool, error) {
	return publishPRStoredWith(worktree, title, templatePath, base, publishPROptions{
		ghAvailable: func(name string) bool { _, err := exec.LookPath(name); return err == nil },
		runGh: func(worktree string, args ...string) ([]byte, error) {
			cmd := exec.Command("gh", args...)
			cmd.Dir = worktree
			var stderr strings.Builder
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("gh pr create failed (exit code %d): %s",
					exitCodeFromError(err), strings.TrimSpace(stderr.String()))
			}
			return output, nil
		},
		copy: copyToClipboard,
	})
}

func publishPRStoredWith(worktree, title, templatePath, base string, options publishPROptions) (string, bool, error) {
	return pr.PublishPRWithTitle(worktree, title, templatePath, base, options.ghAvailable, options.runGh, options.copy)
}

func remoteBranchHead(worktree, branch string) (string, error) {
	return pr.RemoteBranchHeadWith(context.Background(), worktree, branch, git.BranchRemoteFrom,
		func(ctx context.Context, worktree string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, "git", append([]string{"-C", worktree}, args...)...).Output()
		})
}

func resolveBlobStore(worktree string) (review.StoreBlobs, error) {
	return pr.ResolveBlobStore(worktree)
}

// depsPrCreate groups the injectable seams of the persisted-review publisher;
// tests can exercise preflight, deterministic validation, CI, and publication
// without real git, agents, or gh. In production, runPrCreate resolves them to
// the real functions.
type depsPrCreate struct {
	loadConfig     func(worktree string) (config.Config, error)
	runValidation  func(profile string, scope []string, opts validation.RunOptions) ([]validation.ValidationRun, error)
	getGitDirAt    func(worktree string) (string, error)
	getHeadSHAAt   func(worktree string) (string, error)
	currentBranch  func(worktree string) (string, error)
	readPRReview   func(commonDir, branch string) (*store.PRReviewEntry, error)
	evidenceAtHEAD func(worktree, evidencePath string) (bool, string, error)
	runCI          func(context.Context, io.Writer, string, string, string, config.CIConfig) (pr.CIOutcome, error)
	composeBody    func(store.PRReviewEntry, pr.CIOutcome) (string, error)
	publishStored  func(worktree, title, templatePath, base string) (string, bool, error)
	// remoteBranchHead reads the SHA the remote branch points at so publication
	// refuses when another actor pushed over the reviewed head.
	remoteBranchHead func(worktree, branch string) (string, error)
	recordEvent      func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error
	// getGitCommonDir and recordDecision cover T7.5 (M3 report): the --force
	// that overrides a red validation stops being an untraceable exception.
	// store.NewStore requires the git common dir (shared across linked
	// worktrees), NEVER the per-worktree gitDir that recordEvent above
	// already uses: they are two directories with two distinct contracts
	// (see the doc comment of store.NewStore).
	getGitCommonDir func(worktree string) (string, error)
	recordDecision  func(commonDir string, d *store.Decision) error
	// resolveActor is one more seam of this same effort: without it,
	// runPrCreateCon would call resolveActor(worktree) directly, which shells
	// out to a real `git config user.name`, breaking depsPrCreate's promise
	// of testing "without real git, agents or gh" (comment above).
	resolveActor  func(worktree string) string
	resolveParent func(git.ParentResolutionOptions) (git.ParentResolution, error)
	// writeTemplate allows tests to observe whether the PR template was
	// created. When nil, runPrCreateCon uses pr.WritePRTemplate.
	writeTemplate func(string) (string, error)
}

// realPrCreateDeps resolves the production seams of pr create. Extracted from
// runPrCreate because the command exits after dispatching the application flow.
func realPrCreateDeps() depsPrCreate {
	return depsPrCreate{
		// STRICT config (orchestrator finding, F1): pr create is exactly the
		// command whose whole point is "the validation rules", so a broken yml
		// must fail loudly just like gate/pr review/status, never continue
		// silently with the default config.
		loadConfig:    config.LoadStrictLocalConfig,
		getGitDirAt:   git.GetGitDirFrom,
		getHeadSHAAt:  git.SHAHeadFrom,
		currentBranch: git.CurrentBranchFrom,
		readPRReview: func(commonDir, branch string) (*store.PRReviewEntry, error) {
			return store.NewStore(commonDir).ReadPRReview(branch)
		},
		evidenceAtHEAD: review.EvidenceAtHEAD,
		runValidation:  validation.RunProfileOnCandidate,
		runCI: func(ctx context.Context, out io.Writer, worktree, branch, head string, cfg config.CIConfig) (pr.CIOutcome, error) {
			return pr.RunConfiguredCI(ctx, out, worktree, branch, head, cfg, pr.CommandCIClient{
				RunGit: func(ctx context.Context, worktree string, args ...string) ([]byte, error) {
					cmd := exec.CommandContext(ctx, "git", append([]string{"-C", worktree}, args...)...)
					return cmd.Output()
				},
				RunGH: func(ctx context.Context, worktree string, args ...string) ([]byte, error) {
					cmd := exec.CommandContext(ctx, "gh", args...)
					cmd.Dir = worktree
					return cmd.Output()
				},
			}, git.BranchRemoteFrom, nil, nil)
		},
		composeBody:      pr.ComposePRBody,
		publishStored:    publishPRStored,
		remoteBranchHead: remoteBranchHead,
		recordEvent:      ops.RecordEvent,
		getGitCommonDir:  git.GetGitCommonDir,
		recordDecision: func(commonDir string, d *store.Decision) error {
			return store.NewStore(commonDir).RecordDecision(d)
		},
		resolveActor:  resolveActor,
		resolveParent: git.ResolveParentBranch,
		writeTemplate: pr.WritePRTemplate,
	}
}

// runPrCreate implements pr create: it validates the persisted branch review,
// runs configured CI, composes deterministic evidence, and publishes the stored
// title/body with gh or the clipboard fallback.
func runPrCreate(worktree string, args []string) {
	os.Exit(runPrCreateCon(os.Stdout, worktree, args, realPrCreateDeps()))
}

// runPrCreateCon is the injectable version of runPrCreate (test seam): it
// returns the exit code without ending the process, same pattern as runGate;
// the flow lives in internal/app/pr.
func runPrCreateCon(w io.Writer, worktree string, args []string, deps depsPrCreate) int {
	flags, err := parsePrCreateFlags(args)
	if err != nil {
		fmt.Fprintf(w, "? %v\n", err)
		return 1
	}
	return pr.RunPrCreateWith(w, worktree, flagsPrCreateToPr(flags), depsPrCreateToPr(deps), wiringPr())
}

// branchOptionsWithRefuter sets the per-finding refuter factory on the branch
// options; it stays here because package-main tests drive it directly and the
// moved flows reach it through wiringPr.
func branchOptionsWithRefuter(cfg config.Config, verifier *modelprobe.Verifier, opts review.BranchOptions) review.BranchOptions {
	opts.RefuterFactory = refuterFactory(cfg, verifier)
	return opts
}

// wiringPr collects the production collaborators that remain in package main
// because the review and gate commands share them. newModelVerifier is
// read through a closure so a test that swaps the var is honored at call time.
func wiringPr() pr.Wiring {
	return pr.Wiring{
		NewModelVerifier:         func(worktree string) *modelprobe.Verifier { return newModelVerifier(worktree) },
		SharedReviewLedger:       sharedReviewLedger,
		LoadDispositions:         loadDispositionsForWorktree,
		TransportFactory:         reviewTransportFactory,
		BranchOptionsWithRefuter: branchOptionsWithRefuter,
		ShortSHA:                 shortSHA,
		DefaultGateProfile:       defaultGateProfile,
		ProjectFindings:          projectValidationFindings,
		Version:                  version,
	}
}
