package main

// FU-2/C6 moved the pr orchestration (review and create) to internal/app/pr as
// a pure one-subcommand split: this file keeps the flag parsing, the option
// and seam structs the package-main tests construct, the production wiring
// (realPrCreateDeps, wiringPr) and thin dispatch wrappers into the flows.
// Movement hazard cleared: the 21 live block records cite snapshot.go,
// comandos_runs.go and siblings — none cites cmd/sentinel/comandos_pr.go —
// and there are no standing dispositions (no dispositions.jsonl), so moving
// the orchestration out of this file orphans nothing.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/app/pr"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// honestNetIntention aliases the intent string that now lives with the flows.
const honestNetIntention = pr.HonestNetIntention

// flagsPrReview are the options of pr review.
type flagsPrReview struct {
	base        string
	onlyPending bool // --only-unaudited
	overview    bool // --overview
	jsonOut     bool // --json
	parent      string
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
			flags.onlyPending = true
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
	return "The legacy 'sentinel pr [gh arguments]' passthrough was removed because it bypassed the guardian's review flow. Use 'sentinel pr create' to publish a reviewed pull request or 'sentinel pr review' for a dry-run analysis.", 1
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
		Base:        f.base,
		OnlyPending: f.onlyPending,
		Overview:    f.overview,
		JsonOut:     f.jsonOut,
		Parent:      f.parent,
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
		GetGitDir:        d.getGitDir,
		GetHeadSHA:       d.getHeadSHA,
		RunValidation:    d.runValidation,
		AnalyzeBranch:    d.analyzeBranch,
		Verify:           d.verify,
		Publish:          d.publish,
		RecordEvent:      d.recordEvent,
		GetGitCommonDir:  d.getGitCommonDir,
		RecordDecision:   d.recordDecision,
		ResolveActor:     d.resolveActor,
		WriteTemplate:    d.writeTemplate,
		BlobStore:        d.blobStore,
		ReadDispositions: d.loadDispositions,
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

// runPrReview analyzes the branch against the base and prints the audit
// matrix, the summary and the single/chain decision. It is a dry-run: nothing
// is published. It records the pr-review event when done; the flow lives in
// internal/app/pr. It owns the JSON-safe routing here: the payload writer
// carries the human motion (⏳ spinners, warnings) normally, and stderr
// carries it in --json mode so byte 0 of stdout stays '{'.
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

func semanticAdvisory(records []review.Record) (warn bool, blockers []review.ReviewFinding) {
	return pr.SemanticNotice(records)
}

func semanticAdvisoryWithDispositions(records []review.Record, dispositions []review.FindingDisposition) (warn bool, blockers []review.ReviewFinding) {
	return pr.SemanticNoticeWithDispositions(records, dispositions)
}

func detailPrCreateEvent(prURL string, fallback, chain, force bool, reason string) (ops.EventDetail, error) {
	return pr.PrCreateEventDetail(prURL, fallback, chain, force, reason)
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

// publishPROptions groups the injectable dependencies of publishPRWith:
// ghAvailable decides whether gh is on the PATH, runGh launches gh and returns
// its output (full args, including the worktree as cwd), copy is used only in
// the fallback (clipboard).
type publishPROptions struct {
	ghAvailable func(string) bool
	runGh       func(worktree string, args ...string) ([]byte, error)
	copy        func(string) error
}

// publishPR publishes the PR with gh pr create --draft -F template. If gh is
// not on the PATH, fall back to file + clipboard (guide §12.4): the body is
// re-read from the freshly written file. It returns the PR URL (empty in the
// fallback) and whether the fallback was used. It stays in package main
// because the production runGh closure reports gh's exit code through
// exitCodeFromError, which the status command shares.
func publishPR(worktree, templatePath, base string) (string, bool, error) {
	return publishPRWith(worktree, templatePath, base, publishPROptions{
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

func publishPRWith(worktree, templatePath, base string, options publishPROptions) (string, bool, error) {
	return pr.PublishPRWith(worktree, templatePath, base, options.ghAvailable, options.runGh, options.copy)
}

func resolveBlobStore(worktree string) (review.StoreBlobs, error) {
	return pr.ResolveBlobStore(worktree)
}

// depsPrCreate groups the injectable seams of the pr create pipeline (T1.8):
// it lets tests exercise the ORDER (validation before auditing, zero tokens
// if it fails) without real git, agents or gh. In production, runPrCreate
// resolves them to the real functions.
type depsPrCreate struct {
	loadConfig    func(worktree string) (config.Config, error)
	getGitDir     func() (string, error)
	getHeadSHA    func() (string, error)
	runValidation func(profile string, scope []string, opts validation.RunOptions) ([]validation.ValidationRun, error)
	// analyzeBranch receives the WORKTREE, not a gitDir: where the review
	// ledger is anchored is a production decision that lives in
	// sharedReviewLedger, not something the caller picks per invocation.
	analyzeBranch func(worktree string, opts review.BranchOptions) (*review.BranchResult, error)
	verify        func(worktree, gitDir string, cfg config.Config, modelVerifier *modelprobe.Verifier) review.TemplateVerification
	publish       func(worktree, templatePath, base string) (string, bool, error)
	recordEvent   func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error
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
	resolveActor func(worktree string) string
	// writeTemplate allows tests to observe whether the PR template was
	// created. When nil, runPrCreateCon uses pr.WritePRTemplate.
	writeTemplate func(string) (string, error)
	// blobStore builds the content-addressed store that lets AnalyzeBranch
	// reuse reviews after a rebase (F8 criterion 2). It is a seam because
	// resolveBlobStore shells out to git, which depsPrCreate exists to avoid;
	// nil means no reuse, the behaviour before this wiring.
	blobStore func(worktree string) (review.StoreBlobs, error)
	// loadDispositions reads the standing human answers for the advisory
	// overlay. It is a seam because loadDispositionsForWorktree resolves
	// the real git common dir, which depsPrCreate exists to avoid; nil
	// means no standing answers, the fixture every older test builds.
	loadDispositions func(worktree string) ([]review.FindingDisposition, error)
}

// realPrCreateDeps resolves the production seams of pr create. Extracted from
// runPrCreate for the same reason as branchReviewOptions: runPrCreate calls
// os.Exit, so a seam silently losing its production wiring — the blob store of
// F8 criterion 2 among them — would fail no test.
func realPrCreateDeps() depsPrCreate {
	return depsPrCreate{
		// STRICT config (orchestrator finding, F1): pr create is exactly the
		// command whose whole point is "the validation rules", so a broken yml
		// must fail loudly just like gate/pr review/status, never continue
		// silently with the default config.
		loadConfig:    config.LoadStrictLocalConfig,
		getGitDir:     git.GetGitDir,
		getHeadSHA:    git.SHAHead,
		runValidation: validation.RunProfileOnCandidate,
		analyzeBranch: func(worktree string, opts review.BranchOptions) (*review.BranchResult, error) {
			ledger, err := sharedReviewLedger(worktree)
			if err != nil {
				return nil, err
			}
			return review.AnalyzeBranch(ledger, opts)
		},
		verify: func(worktree, gitDir string, cfg config.Config, modelVerifier *modelprobe.Verifier) review.TemplateVerification {
			return verifyForTemplateWith(worktree, gitDir, cfg, modelVerifier, ops.Verify)
		},
		publish:         publishPR,
		recordEvent:     ops.RecordEvent,
		getGitCommonDir: git.GetGitCommonDir,
		recordDecision: func(commonDir string, d *store.Decision) error {
			return store.NewStore(commonDir).RecordDecision(d)
		},
		resolveActor:     resolveActor,
		writeTemplate:    pr.WritePRTemplate,
		blobStore:        resolveBlobStore,
		loadDispositions: loadDispositionsForWorktree,
	}
}

// runPrCreate implements pr create (T1.8): it validates BEFORE auditing (if
// it fails without --force, AnalyzeBranch is not even called: zero tokens),
// advisory notice of the semantic verdict, honest template with the two
// natures of evidence, and publication with gh or the clipboard fallback.
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
