package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/secret"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// runReview audits one or more commits against the engine and saves the record
// in the ledger. Exit codes (guide §9): 0 ok/warn, 1 block, 3 questions,
// 4 provider_unavailable. With --gate, any CRITICAL also escalates to 1.
// reviewPlan decides the bundles to audit and, only when needed, reads the
// evidence the derivation requires.
//
// The order matters: explicit dimensions replace the derived plan outright, so
// reading .gitattributes before looking at dims aborted a run whose bundles
// the caller had already chosen. readAttributes is injected so that the skip
// is testable without running a whole review.
func reviewPlan(dims []string, profile change.ChangeProfile, files []string, diff string, readAttributes func() (string, error)) ([]review.ReviewBundle, error) {
	if len(dims) > 0 {
		return []review.ReviewBundle{{Name: "requested", Dimensions: dims, Priority: review.PriorityRequired, Cost: 1}}, nil
	}
	attributes, err := readAttributes()
	if err != nil {
		return nil, err
	}
	return review.PlanForProfile(profile, files, diff, attributes).Bundles, nil
}

func runReview(worktree string, args []string) {
	flags, err := parseAuditFlags(args)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	// STRICT config (orchestrator finding, outside the original ticket text):
	// an unknown key in the yml must cut here with an explicit error, not
	// continue silently with the default config.
	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	cfg = applyTimeoutFlag(cfg, flags)
	// Resolved from the worktree being operated on, never from the process
	// cwd: see the comment of git.GetGitDirFrom.
	gitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	// --prune runs BEFORE the shared ledger is built, and that order is load
	// bearing. Purging enumerates every per-checkout ledger and keeps a
	// documented fallback for when the common directory cannot be resolved;
	// building the shared ledger first turned that same resolution failure into
	// an exit, so the fallback became unreachable.
	if flags.prune {
		// --prune is a standalone mode: combining it with targets or audit
		// flags would silently ignore them (cf. flagsNotApplicableToStatus).
		// Without targets: parseAuditFlags no longer fills in the "HEAD"
		// default (B17), so "only --prune" is detected by an EMPTY targets,
		// not by targets == ["HEAD"].
		pruneOnly := len(flags.targets) == 0 &&
			len(flags.dims) == 0 && !flags.all && !flags.chain && !flags.gate &&
			flags.profile == "" && flags.answer == "" && flags.timeout == 0
		if !pruneOnly {
			fmt.Println("? review --prune cannot be combined with targets or audit flags (--dims/--all/--chain/--gate/--profile/--answer/--timeout).")
			os.Exit(1)
		}
		os.Exit(runPruneAndReport(worktree, gitDir, flags.jsonOut))
	}

	// The ledger is anchored on the common directory, not on gitDir: see
	// sharedReviewLedger. gitDir stays for the operational event log, which is
	// per-checkout on purpose.
	ledger, err := sharedReviewLedger(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	shas, err := resolveAuditSHAs(ledger, flags)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	if len(shas) == 0 {
		fmt.Println("✅ No commits to audit.")
		return
	}

	// Standing human answers apply to every re-audit below. A corrupt
	// dispositions log fails closed here: auditing as if no human ever
	// answered would re-report findings a person already refuted.
	dispositions, err := loadDispositionsForWorktree(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	gitCommonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	reviewStore := store.NewStore(gitCommonDir)
	modelVerifier := newModelVerifier(worktree)
	finalExit := 0
	// The ⏳ progress lines are human motion, not payload: with --json the
	// machine-consumed stdout must start at '{', so the progress rides the
	// JSON-safe stderr channel, the same discipline as the durable-run
	// announcer below.
	progress := io.Writer(os.Stdout)
	if flags.jsonOut {
		progress = os.Stderr
	}

	total := len(shas)

	for idx, sha := range shas {
		fmt.Fprintf(progress, "⏳ [%d/%d] Audit %s\n", idx+1, total, shortSHA(sha))
		message, err := git.CommitMessage(sha)
		if err != nil {
			fmt.Printf("⚠️ %s: could not read the message: %v\n", sha[:8], err)
			continue
		}
		reuse, err := review.DecideBlobReuse(ledger, reviewStore, sha, message)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}
		reauditSpec := reuse.Reused() && reuse.ReauditSpec
		if reuse.Reused() && len(flags.dims) > 0 {
			fmt.Printf("❌ --dims cannot be combined with review reuse for %s\n", shortSHA(sha))
			os.Exit(1)
		}
		var result review.AuditResult
		var pending []review.AgentQuestion
		var files []string
		var secretAdvisories []string
		var fixed bool
		if reuse.Reused() {
			if err := ledger.AdoptRecordWithMessage(reuse.OriginSHA, sha, message); err != nil {
				fmt.Printf("❌ %v\n", err)
				os.Exit(1)
			}
			if !reauditSpec {
				record, err := ledger.ReadRecord(sha)
				if err != nil {
					fmt.Printf("❌ %v\n", err)
					os.Exit(1)
				}
				if record == nil {
					fmt.Printf("❌ adopted record for %s was not saved\n", sha[:8])
					os.Exit(1)
				}
				latest, _, ok := review.LastAuthoritativeRevision(*record)
				if !ok {
					fmt.Printf("❌ adopted record for %s has no authoritative revision\n", sha[:8])
					os.Exit(1)
				}
				files, err = git.FilesOfCommit(sha)
				if err != nil {
					fmt.Printf("⚠️ %s: could not read the files: %v\n", sha[:8], err)
					continue
				}
				result = review.AuditResultFromRevision(sha, latest)
				fixed = latest.Fixed
			}
		}
		if !reuse.Reused() || reauditSpec {
			diff, err := git.DiffCommit(sha)
			if err != nil {
				fmt.Printf("⚠️ %s: could not read the diff: %v\n", sha[:8], err)
				continue
			}
			files, err = git.FilesOfCommit(sha)
			if err != nil {
				fmt.Printf("⚠️ %s: could not read the files: %v\n", sha[:8], err)
				continue
			}

			profile, err := change.ComputeCommitProfile(sha)
			if err != nil {
				fmt.Printf("⚠️ %s: could not derive the change profile: %v\n", sha[:8], err)
				os.Exit(1)
			}
			bundles, err := reviewPlan(flags.dims, profile, files, diff, func() (string, error) { return git.Attributes(sha) })
			if err != nil {
				fmt.Printf("⚠️ %s: could not read the attributes: %v\n", sha[:8], err)
				os.Exit(1)
			}
			if reauditSpec {
				bundles = []review.ReviewBundle{{Name: "reused_spec", Dimensions: []string{review.DimSpec}, Priority: review.PriorityRequired, Cost: 1}}
			}

			// FU-11: deterministic exposed-credential incident, independent of
			// security_sensitive and of the scheduled bundles. It lands in
			// result.Findings through HallazgosDeterministas, so it is
			// reported even when the plan schedules no dimension.
			var secretFindings []review.Finding
			secretFindings, secretAdvisories = secret.SecretFindingsAndAdvisories(files, diff)
			if reauditSpec {
				secretFindings = nil
				secretAdvisories = nil
			}

			// The collector records which agent attended each dimension so the
			// record stores the real author, not the requested profile (H4/T0.2).
			authorship := &authorshipCollector{}
			factory := func(_ review.ReviewBundle, dimension string) (review.AgentReviewer, string, error) {
				profile := config.ResolveProfile(cfg, reviewcontract.DefaultProfile(dimension), flags.profile)
				adapter, err := agentadapter.NewAdapterWithProfile(cfg, profile)
				if err != nil {
					return nil, profile.Name, err
				}
				modelVerifier.Verify(profile.Name, profile.Model, adapter)
				return &observedAgent{AgentReviewer: adapter, authorship: authorship}, profile.Name, nil
			}
			reviewTransport, metricsFinalizer := announcedReviewTransportWithMetrics(cfg, worktree, sha, files, os.Stderr)
			options := review.AuditOptions{
				SHA:             sha,
				Message:         message,
				Diff:            diff,
				Bundles:         bundles,
				Answers:         flags.answer,
				ProfileOverride: flags.profile,
				ContextProvider: reviewContextProvider(cfg, worktree),
				ContextPaths:    files,
				// The announcer lives at this command boundary only: each durable
				// review run is announced on stderr (the JSON-safe channel) the
				// moment it is admitted, with its `runs attach --follow` command,
				// so an operator can attach while the review is still executing.
				ReviewTransportWithEvidence: reviewTransport,
				FinalizeMetrics:             metricsFinalizer,
				// Standing human answers recorded against this SHA win over a
				// fresh agent verdict for the same fingerprint (FU-6).
				Dispositions: review.FilterDispositionsForSHA(dispositions, sha),
				// The prober already ran inside the auditor factory above;
				// the engine consults it here without importing it.
				ModelVerifier: modelVerifier,
				OnDimension: func(dim string) {
					fmt.Fprintf(progress, "  ⏳ %s …\n", dim)
				},
				// FU-11: deterministic credential incidents ride along without
				// scheduling any dimension and without touching the verdict.
				DeterministicFindings: secretFindings,
			}
			result = review.AuditCommit(factory, cfg.Review.Parallel, auditOptionsWithRefuter(options, cfg, modelVerifier))

			result, pending, err = applyPendingQuestions(worktree, sha, factory, cfg, modelVerifier, options, result)
			if err != nil {
				fmt.Printf("❌ %v\n", err)
				os.Exit(1)
			}
			if flags.jsonOut && result.Verdict == review.VerdictQuestion {
				printPendingQuestionsJSON(sha, pending)
			}

			model := flags.profile
			if model == "" {
				model = "default"
			}
			effective := authorship.consolidate()
			// Coverage records how this revision's plan was chosen (piece 2): a run
			// with no --dims derives its plan from the change and is authoritative;
			// a run the operator narrowed with --dims is supplementary.
			coverage := review.CoverageAuthoritative
			if len(flags.dims) > 0 && !reauditSpec {
				coverage = review.CoverageSupplementary
			}
			// RULE 1 applies to the Fixed claim too, and internal/review owns it:
			// coverage is passed in rather than pre-applied here.
			fixed = review.RevisionFixesPriorBlock(ledger, sha, result.Verdict, coverage)
			revision := review.Revision{
				At:                 time.Now(),
				Result:             result.Verdict,
				Fixed:              fixed,
				Coverage:           coverage,
				Agent:              effective.Binary,
				Model:              effective.Model,
				Effort:             effective.Effort,
				Dims:               review.DimensionResultsForRecord(result.Dims),
				AggregatedFindings: result.Findings,
			}
			if reauditSpec {
				if err := ledger.SaveReusedSpecRevision(sha, revision); err != nil {
					fmt.Printf("⚠️ %s: could not save the reused spec record: %v\n", sha[:8], err)
				}
			} else if err := ledger.SaveRevision(sha, message, "", model, revision); err != nil {
				fmt.Printf("⚠️ %s: could not save the record: %v\n", sha[:8], err)
			}
			if !reauditSpec {
				if coverage == review.CoverageAuthoritative {
					if err := review.RegisterCommitBlobs(reviewStore, sha, files); err != nil {
						fmt.Printf("⚠️ %s: could not register blobs for future review reuse: %v\n", sha[:8], err)
					}
				}
			} else {
				record, err := ledger.ReadRecord(sha)
				if err != nil {
					fmt.Printf("❌ %v\n", err)
					os.Exit(1)
				}
				if record == nil {
					fmt.Printf("❌ reused spec record for %s was not saved\n", sha[:8])
					os.Exit(1)
				}
				latest, _, ok := review.LastAuthoritativeRevision(*record)
				if !ok {
					fmt.Printf("❌ reused spec record for %s has no authoritative revision\n", sha[:8])
					os.Exit(1)
				}
				result = review.AuditResultFromRevision(sha, latest)
				fixed = latest.Fixed
			}
		}

		if reuse.Reused() {
			fmt.Fprintf(progress, "♻️ Reused review of %s from %s\n", shortSHA(sha), shortSHA(reuse.OriginSHA))
		}
		fmt.Print(result.String())
		for _, advisory := range secretAdvisories {
			fmt.Println(advisory)
		}
		if fixed {
			fmt.Printf("  ✅ previous revision in block corrected by this review\n")
		}

		exit := verdictExitCode(result.Verdict)
		if flags.gate && hasCriticalFindings(result) {
			exit = 1
		}
		if exit > finalExit {
			finalExit = exit
		}
		detail := reviewEventDetail(flags, result)
		if err := ops.RecordEvent(gitDir, "review", exit, []string{sha}, detail, worktree); err != nil {
			fmt.Printf("⚠️ Could not record the event: %v\n", err)
		}
		recordFixes(ledger, gitDir, sha, files, message, exit, worktree)
	}
	os.Exit(finalExit)
}

func reviewEventDetail(flags auditFlags, result review.AuditResult) ops.EventDetail {
	detail := ops.EventDetail{
		"all":   flags.all,
		"chain": flags.chain,
		"gate":  flags.gate,
		"dims":  flags.dims,
	}
	if result.ContextSkipReason != "" {
		detail["context_skip_reason"] = result.ContextSkipReason
	}
	var failures []ops.EventDetail
	for _, dimension := range result.Dims {
		if dimension.Result != nil && dimension.Result.Verdict != review.VerdictUnavailable {
			continue
		}
		if dimension.Result == nil && dimension.Error == nil {
			continue
		}
		bundle, dim, reason := dimension.Bundle, dimension.Dim, ""
		if dimension.Result != nil {
			if bundle == "" {
				bundle = dimension.Result.Bundle
			}
			if dim == "" {
				dim = dimension.Result.Dim
			}
			reason = dimension.Result.Reason
		}
		if strings.TrimSpace(reason) == "" && dimension.Error != nil {
			reason = dimension.Error.Error()
		}
		reason = review.CompactProviderCause(reason)
		if strings.TrimSpace(reason) == "" {
			continue
		}
		failures = append(failures, ops.EventDetail{
			"bundle":    bundle,
			"dimension": dim,
			"reason":    reason,
		})
	}
	if len(failures) > 0 {
		detail["reviewer_failures"] = failures
	}
	return detail
}

var newModelVerifier = func(worktree string) *modelprobe.Verifier {
	gitCommonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		return modelprobe.NewVerifier(nil)
	}
	return modelprobe.NewVerifier(store.NewStore(gitCommonDir))
}

func reviewContextProvider(cfg config.Config, worktree string) review.ContextProvider {
	if !cfg.Review.CodeGraphContext || !allowsExternalAgentDiff(worktree) {
		return nil
	}
	return graph.DetectCodeGraphProvider(worktree)
}

func auditOptionsWithRefuter(opts review.AuditOptions, cfg config.Config, verifier *modelprobe.Verifier) review.AuditOptions {
	opts.RefuterFactory = refuterFactory(cfg, verifier)
	return opts
}

// applyPendingQuestions deduplicates result.Questions against
// answers already persisted in the store (T7.5/T7.6): if a question from
// this pass was already answered for the same content blob, it is folded
// back into a retry round exactly as if the user had repeated it via
// --answer in this same invocation. It also persists any fresh "id=text"
// answer the user supplies now (parseAuditAnswers, filtered to
// real question IDs by filterAnswersByRealIDs), so a future run over
// the same blob does not ask again. It never aborts the command: a
// missing/unavailable store, or any error while resolving blobs or looking
// up answers, degrades to reporting the raw (non-deduplicated) questions.
//
// Before returning, it always writes the final pending list back into
// result.Questions so every other consumer of result (the ledger, the
// human-readable text output, the exit code) sees the same deduplicated
// state. It also reconciles result.Verdict: a model is not guaranteed
// to stop asking just because it received the clarification in the retry
// round, so if it re-emits "question" with only questions this run already
// has a registered answer for, that must not be a permanent block (exit 3
// forever on every future run over identical content) — it is downgraded to
// warn instead. That downgrade only fires when a retry actually happened
// with real answers (known or fresh): a "question" verdict with an empty
// questions list from the very first pass (malformed model output, no
// answer involved at all) is left as-is, not silently masked as warn.
func applyPendingQuestions(worktree, sha string, factory review.ReviewerFactory, cfg config.Config, verifier *modelprobe.Verifier, options review.AuditOptions, result review.AuditResult) (review.AuditResult, []review.AgentQuestion, error) {
	if result.Verdict != review.VerdictQuestion {
		return result, result.Questions, nil
	}
	gitCommonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		return result, result.Questions, nil
	}
	st := store.NewStore(gitCommonDir)
	resolveBlob := func(file string) (string, error) { return git.BlobFileAtCommit(sha, file) }

	originals := result.Questions
	pending, answered, err := review.SplitPendingQuestions(originals, resolveBlob, st.RecordedAnswer)
	if err != nil {
		return result, originals, nil
	}

	rawByID, rawLeftover := parseAuditAnswers(options.Answers)
	// rawByID may contain "ids" that are really free prose with a literal "="
	// (e.g. --answer "the flag --gate=true is set"): they are filtered here,
	// before building the retry prompt and before the persistence loop, so an
	// id that does not exist never reaches either of them.
	byQuestion, leftover, err := resolveQuestionAnswers(rawByID, rawLeftover, originals)
	if err != nil {
		return result, originals, err
	}

	// Persist the fresh answers BEFORE the retry (not after): the
	// SplitPendingQuestions call after the retry consults the store, so if a
	// question freshly answered by the user shows up again in that second
	// pass, it must be recognized as already answered in this same
	// invocation, not only in a future run.
	for question, text := range byQuestion {
		if question.File == "" {
			continue // question without a File: no blob can be resolved, not an error case
		}
		blob, err := resolveBlob(question.File)
		if err != nil {
			fmt.Printf("⚠️ %s: could not resolve the blob of %q to persist the answer to %q: %v\n", shortSHA(sha), question.File, question.ID, err)
			continue
		}
		if err := st.RecordAnswer(blob, question.ID, text, resolveActor(worktree)); err != nil {
			fmt.Printf("⚠️ %s: could not persist the answer to %q: %v\n", shortSHA(sha), question.ID, err)
		}
	}

	// retried distinguishes "AuditarCommit was re-run with real answers" from
	// "pending came back empty because originals was already empty" (a
	// "question" verdict with an empty question list is malformed model
	// output, not a resolved deadlock): without this flag, the verdict
	// downgrade below would mask that case as warn without any known answer
	// being involved.
	retried := false
	if len(answered) > 0 || len(byQuestion) > 0 {
		// Union of what the store already knows and what the user is
		// answering right now (byID): a fresh answer on the same id
		// prevails over the already recorded one, because the user is
		// actively answering in this invocation. Also triggering the
		// retry when there is only byID (no store answers) keeps a
		// fresh answer given in this same invocation from being reported
		// as pending in its own --json.
		var lines []string
		if leftover != "" {
			lines = append(lines, leftover)
		}
		// answered keeps its own (File, Answer): it is not collapsed by
		// ID into a map[string]string, because AgentQuestion.ID is chosen
		// by the model per dimension without cross-dimension uniqueness —
		// two different questions (different files) may legitimately share
		// "q1", and each must reach the retry prompt with its own answer.
		// It is ordered by (ID, File) so the prompt is deterministic. An
		// entry whose ID is also in byID is skipped here: the fresh answer
		// of this very invocation prevails over the already recorded one
		// for THAT id (the byID loop below emits it); without this filter,
		// an id present in both sets would produce two contradictory
		// "id: ..." lines in the same prompt.
		ordered := append([]review.AnsweredQuestion(nil), answered...)
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].Question.ID != ordered[j].Question.ID {
				return ordered[i].Question.ID < ordered[j].Question.ID
			}
			return ordered[i].Question.File < ordered[j].Question.File
		})
		for _, aq := range ordered {
			key := questionKey{ID: aq.Question.ID, File: aq.Question.File}
			if _, fresh := byQuestion[key]; fresh {
				continue
			}
			lines = append(lines, questionSelector(key, originals)+": "+aq.Answer)
		}
		for _, key := range sortedQuestionKeys(byQuestion) {
			lines = append(lines, questionSelector(key, originals)+": "+byQuestion[key])
		}
		options.Answers = strings.Join(lines, "\n")
		result = review.AuditCommit(factory, cfg.Review.Parallel, auditOptionsWithRefuter(options, cfg, verifier))
		retried = true
		if pending, _, err = review.SplitPendingQuestions(result.Questions, resolveBlob, st.RecordedAnswer); err != nil {
			pending = result.Questions
		}
	}

	result.Questions = pending
	if retried && result.Verdict == review.VerdictQuestion && len(pending) == 0 {
		// The agent kept asking even though a registered answer already
		// exists for each of its current questions (a model is not
		// guaranteed to stop asking just because it received the
		// clarification). The system already has an answer for everything
		// pending, so this must not block forever: it is downgraded to warn
		// instead of leaving a permanent deadlock on "question" (exit 3 on
		// every future run over the same content). It only applies when a
		// retry with answers actually happened: a "question" with empty
		// questions from the first pass (no known, no fresh) never enters
		// this branch.
		result.Verdict = review.VerdictWarn
	}
	return result, pending, nil
}

type questionKey struct {
	ID   string
	File string
}

// resolveQuestionAnswers binds bare selectors to one unique question and
// qualified selectors to the exact (ID, File) pair. Unknown selectors retain
// the existing free-text behavior; ambiguous bare IDs fail explicitly.
func resolveQuestionAnswers(raw map[string]string, prose string, questions []review.AgentQuestion) (map[questionKey]string, string, error) {
	byID := make(map[string]map[string]questionKey)
	for _, q := range questions {
		if byID[q.ID] == nil {
			byID[q.ID] = make(map[string]questionKey)
		}
		byID[q.ID][q.File] = questionKey{ID: q.ID, File: q.File}
	}

	resolved := make(map[questionKey]string)
	var unknown []string
	for _, selector := range sortedKeys(raw) {
		id, file, qualified := strings.Cut(selector, "@")
		candidates := byID[id]
		if qualified {
			key, ok := candidates[file]
			if ok {
				resolved[key] = raw[selector]
				continue
			}
			unknown = append(unknown, selector+"="+raw[selector])
			continue
		}
		if len(candidates) == 1 {
			for _, key := range candidates {
				resolved[key] = raw[selector]
			}
			continue
		}
		if len(candidates) > 1 {
			candidateNames := make([]string, 0, len(candidates))
			for _, key := range candidates {
				candidateNames = append(candidateNames, key.ID+"@"+key.File)
			}
			sort.Strings(candidateNames)
			return nil, prose, fmt.Errorf("answer %q is ambiguous; qualify one of: %s", selector, strings.Join(candidateNames, ", "))
		}
		unknown = append(unknown, selector+"="+raw[selector])
	}
	if prose != "" {
		unknown = append(unknown, prose)
	}
	return resolved, strings.Join(unknown, ","), nil
}

func sortedQuestionKeys(answers map[questionKey]string) []questionKey {
	keys := make([]questionKey, 0, len(answers))
	for key := range answers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ID != keys[j].ID {
			return keys[i].ID < keys[j].ID
		}
		return keys[i].File < keys[j].File
	})
	return keys
}

func questionSelector(key questionKey, questions []review.AgentQuestion) string {
	files := make(map[string]struct{})
	for _, q := range questions {
		if q.ID == key.ID {
			files[q.File] = struct{}{}
		}
	}
	if len(files) > 1 {
		return key.ID + "@" + key.File
	}
	return key.ID
}

// filterAnswersByRealIDs separates byID (from parseAuditAnswers)
// into the entries whose id actually matches one of questions' real
// question IDs from this pass, and the entries that don't. An "id=text"
// token whose id was never asked in this pass is not a targeted answer: it
// is ordinary free text that happened to contain a literal "=" (e.g.
// --answer "the flag --gate=true is set" must not be parsed as an answer to
// a nonexistent question "the flag --gate"). Non-matching entries are folded
// back into leftover, reconstructed as "id=text", so they keep behaving as
// plain prose instead of being silently dropped or persisted as garbage.
// parseAuditAnswers itself stays pure/context-free — it doesn't
// know about real question IDs — which is why this filtering lives here,
// where that context (questions) is available.
func filterAnswersByRealIDs(byID map[string]string, leftover string, questions []review.AgentQuestion) (map[string]string, string) {
	real := make(map[string]bool, len(questions))
	for _, q := range questions {
		real[q.ID] = true
	}

	validated := make(map[string]string, len(byID))
	var foreignProse []string
	for _, id := range sortedKeys(byID) {
		if real[id] {
			validated[id] = byID[id]
			continue
		}
		foreignProse = append(foreignProse, id+"="+byID[id])
	}
	if len(foreignProse) == 0 {
		return validated, leftover
	}
	if leftover != "" {
		foreignProse = append(foreignProse, leftover)
	}
	return validated, strings.Join(foreignProse, ",")
}

// sortedKeys returns the keys of m ordered: a map does not iterate in
// deterministic order and this text is sent literally to an agent or
// reconstructed as output — two identical runs must produce the same result.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// questionFile returns the File of question id within questions, or
// "" if it does not appear (id not asked in this pass, or asked without a
// File).
func questionFile(questions []review.AgentQuestion, id string) string {
	for _, q := range questions {
		if q.ID == id {
			return q.File
		}
	}
	return ""
}

// parseAuditAnswers separates --answer into "id=text" targeted
// answers and the leftover free-text prose, so a targeted answer can be
// deduplicated/persisted (T7.6) while every other shape of --answer keeps
// behaving exactly as before. Tokens are comma-separated; this is
// deliberately simple, not a CSV/quoting parser, so free-text prose
// containing a literal comma is split into several prose tokens — an
// accepted limitation of this simple transport.
func parseAuditAnswers(answer string) (byID map[string]string, leftover string) {
	byID = make(map[string]string)
	if answer == "" {
		return byID, ""
	}
	var prose []string
	for _, token := range strings.Split(answer, ",") {
		id, text, hasEqual := strings.Cut(token, "=")
		id = strings.TrimSpace(id)
		if !hasEqual || id == "" {
			prose = append(prose, token)
			continue
		}
		byID[id] = strings.TrimSpace(text)
	}
	return byID, strings.Join(prose, ",")
}

// printPendingQuestionsJSON prints, on a single line, the questions that
// remain pending after deduplication (T7.6). The shape is a presentation
// concern of cmd/sentinel, not of internal/review.
func printPendingQuestionsJSON(sha string, pending []review.AgentQuestion) {
	type pendingQuestion struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		File string `json:"file,omitempty"`
	}
	payload := struct {
		SHA              string            `json:"sha"`
		PendingQuestions []pendingQuestion `json:"pending_questions"`
	}{SHA: sha, PendingQuestions: []pendingQuestion{}}
	for _, q := range pending {
		payload.PendingQuestions = append(payload.PendingQuestions, pendingQuestion{ID: q.ID, Text: q.Text, File: q.File})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("⚠️ %s: could not serialize the pending questions: %v\n", shortSHA(sha), err)
		return
	}
	fmt.Println(string(data))
}

// refuterFactory resolves the explicit cheap profile for the independent
// challenge that runs once for each semantic CRITICAL finding.
func refuterFactory(cfg config.Config, verifier *modelprobe.Verifier) review.RefuterFactory {
	return func() (review.AgentReviewer, string, error) {
		profile := config.ResolveProfile(cfg, "", "cheap")
		adapter, err := agentadapter.NewAdapterWithProfile(cfg, profile)
		if err != nil {
			return nil, profile.Name, err
		}
		verifier.Verify(profile.Name, profile.Model, adapter)
		return adapter, profile.Name, nil
	}
}

// recordFixes associates a fix commit (a fix(...) message that exits without
// criticals) with previous records in block that touch the same files: it
// marks their FixedIn and registers a fix event.
func recordFixes(ledger *review.Ledger, gitDir, sha string, files []string, message string, exit int, worktree string) {
	if exit != 0 || !strings.HasPrefix(message, "fix(") {
		return
	}
	previousSHAs, err := ledger.ListRecords()
	if err != nil {
		return
	}
	fixFiles := map[string]bool{}
	for _, f := range files {
		fixFiles[f] = true
	}

	fixedSHAs := []string{}
	for _, prevSHA := range previousSHAs {
		if prevSHA == sha {
			continue
		}
		record, err := ledger.ReadRecord(prevSHA)
		if err != nil || record == nil || len(record.Revisions) == 0 || record.FixedIn != "" {
			continue
		}
		// The record currently blocks (RULE 2 in coverage.go): an active
		// supplementary alarm is enough to make a genuine fix eligible for
		// provenance credit, exactly like an authoritative block. FixedIn never
		// retires the record; it only records the first credited fix.
		if !review.RecordHasActiveBlock(*record) {
			continue
		}
		if !fixTouchesFindings(fixFiles, review.CurrentFindings(*record)) {
			continue
		}
		// The audited commit must be an ancestor of the fix, in that order: a
		// fix comes after what it corrects. File-name overlap was the whole
		// test until the ledger became repository-wide, and then a fix( commit
		// on one branch could clear a block recorded on an unrelated branch
		// because both happened to touch the same file (FU-17).
		//
		// A query that cannot be answered attributes nothing. That is the safe
		// direction here — the record's blocking state is unchanged and a later
		// fix can still receive provenance credit — and it matches the rest of
		// this function, which is best-effort and already returns silently when
		// the ledger cannot be listed.
		sameHistory, err := git.IsAncestorOf(worktree, prevSHA, sha)
		if err != nil || !sameHistory {
			continue
		}
		if err := ledger.MarkFixed(prevSHA, sha); err == nil {
			fixedSHAs = append(fixedSHAs, prevSHA)
		}
	}

	if len(fixedSHAs) > 0 {
		_ = ops.RecordEvent(gitDir, "fix", 0, []string{sha},
			ops.EventDetail{"corrects": fixedSHAs}, worktree)
		fmt.Printf("  🔧 fix %s marked as correcting: %s\n", shortSHA(sha), strings.Join(fixedSHAs, ","))
	}
}

// shortSHA truncates a SHA to 8 characters without crashing when it is
// shorter (tests use short SHAs).
func shortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// fixTouchesFindings reports whether the fix commit touches any file cited in
// the record's current findings.
func fixTouchesFindings(fixFiles map[string]bool, findings []review.Finding) bool {
	for _, finding := range findings {
		if fixFiles[finding.Location.File] {
			return true
		}
	}
	return false
}

// resolveAuditSHAs computes the SHAs to audit according to the flags: the
// requested list of commits (default HEAD), the chain from the base (--chain)
// or all commits without a record (--all). --chain/--all are not combined with
// explicit targets: mixing two selection criteria makes no sense.
// resolveAuditSHAs receives the ledger already built by the caller instead of
// resolving it again: it was the second resolution of the same data and it did
// it from the cwd, so --all read the ledger of the wrong repository when the
// worktree was not the working directory. It now receives the ledger
// itself rather than a directory, so --all cannot resolve a different anchor
// than the one the audit writes to.
func resolveAuditSHAs(ledger *review.Ledger, flags auditFlags) ([]string, error) {
	// Defaulting to HEAD lives here, not in parseAuditFlags (B17): this is the
	// only function that needs a default target (status has no concept of a
	// target). flags is received by value: mutating targets here does not
	// affect the copy flagsNotApplicableToStatus already used.
	if len(flags.targets) == 0 {
		flags.targets = []string{"HEAD"}
	}
	switch {
	case flags.chain:
		if len(flags.targets) > 1 {
			return nil, errors.New("--chain cannot be combined with a list of commits: use a single target as the end")
		}
		base, err := git.UpstreamOrMain()
		if err != nil {
			return nil, err
		}
		return git.RangeSHAs(base, flags.targets[0])
	case flags.all:
		if len(flags.targets) > 1 {
			return nil, errors.New("--all cannot be combined with a list of commits: audit the whole history without a record")
		}
		allSHAs, err := git.UpToSHAs(flags.targets[0])
		if err != nil {
			return nil, err
		}
		audited, err := ledger.ListRecords()
		if err != nil {
			return nil, err
		}
		alreadyAudited := map[string]bool{}
		for _, sha := range audited {
			alreadyAudited[sha] = true
		}
		var pending []string
		for _, sha := range allSHAs {
			if !alreadyAudited[sha] {
				pending = append(pending, sha)
			}
		}
		return pending, nil
	default:
		resolved := make([]string, 0, len(flags.targets))
		for _, expr := range flags.targets {
			sha, err := git.ResolveSHA(expr)
			if err != nil {
				return nil, fmt.Errorf("could not resolve %q: %v", expr, err)
			}
			resolved = append(resolved, sha)
		}
		return resolved, nil
	}
}

// verdictExitCode translates the global verdict to the exit code according to
// the guide §9: ok/warn 0, block 1, question 3, unavailable 4.
func verdictExitCode(verdict string) int {
	switch verdict {
	case review.VerdictBlock:
		return 1
	case review.VerdictQuestion:
		return 3
	case review.VerdictUnavailable:
		return 4
	default:
		return 0
	}
}

// hasCriticalFindings reports whether any dimension reported CRITICAL.
// Decided by the shared IsBlocking rule (FU-6), like every other blocking
// consumer: a refuted or fixed finding no longer escalates the --gate exit.
func hasCriticalFindings(result review.AuditResult) bool {
	for _, finding := range result.Findings {
		if review.IsBlocking(finding.Severity, finding.Status) {
			return true
		}
	}
	for _, rd := range result.Dims {
		if rd.Result == nil {
			continue
		}
		for _, finding := range rd.Result.Findings {
			if review.IsBlocking(finding.Severity, finding.Status) {
				return true
			}
		}
	}
	return false
}
