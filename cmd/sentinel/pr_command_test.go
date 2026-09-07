package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/app/pr"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// TestRunPrReview_UnknownKeyInYml_Exit1WithLine covers Fix 1 (F1, orchestrator
// finding): runPrReview used LoadLocalConfig (no error). With a
// broken yml, before it went ahead silently until failing later with a git
// error unrelated to the real problem (the worktree of this test is not a
// repo); with LoadStrictLocalConfig it must cut right here with
// exit 1 and the yml error visible.
func TestRunPrReview_UnknownKeyInYml_Exit1WithLine(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	writeYmlWithUnknownKey(t, worktree)

	output, exit := runAsSubprocess(t, "runPrReview", worktree, home)

	if exit != 1 {
		t.Errorf("expected exit 1, got %d (output: %q)", exit, output)
	}
	if !strings.Contains(output, "line") {
		t.Errorf("the output must include the line of the yml error, got: %q", output)
	}
}

// TestPrVerb verifies review/create dispatch and retired passthrough handling.
func TestPrVerb(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{}, ""},
		{[]string{"review"}, "review"},
		{[]string{"review", "--base", "dev"}, "review"},
		{[]string{"create"}, "create"},
		{[]string{"create", "--draft"}, "create"},
		{[]string{"--title", "hello"}, ""},
		{[]string{"-t", "hello"}, ""},
	}
	for _, tc := range cases {
		if got := prVerb(tc.args); got != tc.want {
			t.Errorf("prVerb(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
	message, exitCode := retiredPassthroughDisposition()
	if exitCode != 1 ||
		!strings.Contains(message, "was removed") ||
		!strings.Contains(message, "sentinel pr create") ||
		!strings.Contains(message, "sentinel pr review") {
		t.Errorf("retired passthrough = (%d, %q), want exit 1 and removal/create-or-review guidance", exitCode, message)
	}
}

// TestParsePrReviewFlags covers the pr review flags.
func TestParsePrReviewFlags(t *testing.T) {
	flags, err := parsePrReviewFlags([]string{"--base", "dev", "--overview", "--json"})
	if err != nil {
		t.Fatalf("parsePrReviewFlags failed: %v", err)
	}
	if flags.base != "dev" || !flags.overview || !flags.jsonOut || flags.onlyPending {
		t.Errorf("flags = %+v, expected base=dev overview json", flags)
	}

	flags, err = parsePrReviewFlags([]string{"--only-unaudited"})
	if err != nil {
		t.Fatalf("parsePrReviewFlags(--only-unaudited) failed: %v", err)
	}
	if !flags.onlyPending {
		t.Errorf("flags = %+v, expected onlyPending", flags)
	}

	if _, err := parsePrReviewFlags([]string{"--nope"}); err == nil {
		t.Error("unknown flag should fail")
	}
	if _, err := parsePrReviewFlags([]string{"--base"}); err == nil {
		t.Error("--base without a value should fail")
	}
	rf, err := parsePrReviewFlags([]string{"--parent", "layer-a"})
	if err != nil || rf.parent != "layer-a" {
		t.Errorf("--parent accept: (%q,%v)", rf.parent, err)
	}
	for _, args := range [][]string{{"--parent"}, {"--parent", ""}, {"--parent", "--chain-pr"}} {
		_, errR := parsePrReviewFlags(args)
		_, errC := parsePrCreateFlags(args)
		if errR == nil || errC == nil {
			t.Errorf("%v: want an explicit failure, got %v/%v", args, errR, errC)
		}
	}
}

// TestDetailPrReviewEvent: the event detail is JSON with the fields of the
// guide §13 schema.
func TestDetailPrReviewEvent(t *testing.T) {
	res := &review.BranchResult{
		Branch:   "feature/x",
		SHAs:     []string{"a1b2c3d4e5f6"},
		Pending:  []string{"a1b2c3d4e5f6"},
		Records:  []review.Record{{SHA: "a1b2c3d4e5f6", Message: "feat: something"}},
		Volume:   420,
		Decision: "chain",
	}
	detail, err := detailPrReviewEvent("main", res, true)
	if err != nil {
		t.Fatalf("detailPrReviewEvent failed: %v", err)
	}

	var raw map[string]any
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("detail is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("detail is not valid JSON: %v\n%s", err, data)
	}
	for key, expected := range map[string]any{
		"base":      "main",
		"rama":      "feature/x",
		"auditadas": float64(1),
		"nuevas":    float64(1),
		"volumen":   float64(420),
		"ci":        true,
		"overview":  false,
		"chain_pr":  true,
	} {
		if raw[key] != expected {
			t.Errorf("detail[%q] = %v, expected %v", key, raw[key], expected)
		}
	}
	if _, present := raw["overview_error"]; present {
		t.Errorf("detail[overview_error] present without OverviewError: %v", raw["overview_error"])
	}

	// With OverviewError, the event must carry it so an overview failure does
	// not stay silent.
	res.OverviewError = "the branch auditor did not respond: boom"
	detail, err = detailPrReviewEvent("main", res, true)
	if err != nil {
		t.Fatalf("detailPrReviewEvent failed: %v", err)
	}
	data, err = json.Marshal(detail)
	if err != nil {
		t.Fatalf("detail is not valid JSON: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("detail is not valid JSON: %v\n%s", err, data)
	}
	if raw["overview_error"] != "the branch auditor did not respond: boom" {
		t.Errorf("detail[overview_error] = %v", raw["overview_error"])
	}
}

func TestReviewEventDetailCarriesFailureAndContextReasons(t *testing.T) {
	result := review.AuditResult{
		ContextSkipReason: "codegraph context skipped: dirty_worktree",
		Dims: []review.DimensionOutcome{{
			Dim: review.DimLogic,
			Result: &review.DimensionResult{
				Dim:     review.DimLogic,
				Verdict: review.VerdictUnavailable,
				Reason:  "provider reported: ripgrep execution failed | timed out after 600s",
			},
		}},
	}
	detail := reviewEventDetail(auditFlags{all: true}, result)
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal review detail: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatalf("review detail is not an object: %v", err)
	}
	if object["context_skip_reason"] != "codegraph context skipped: dirty_worktree" {
		t.Errorf("context skip reason = %v, want the provider reason", object["context_skip_reason"])
	}
	failures, ok := object["reviewer_failures"].([]any)
	if !ok || len(failures) != 1 {
		t.Fatalf("reviewer failures = %#v, want one structured failure", object["reviewer_failures"])
	}
	failure, ok := failures[0].(map[string]any)
	if !ok || failure["reason"] != "provider reported: ripgrep execution failed" {
		t.Fatalf("failure = %#v, want only the actual compact tool failure cause", failures[0])
	}
}

// TestDecisionTextPrReview: the decision output distinguishes single/chain and
// explains the reason. The rendered strings come from internal/app/pr, so the
// asserted substrings are that package's frozen wording.
func TestDecisionTextPrReview(t *testing.T) {
	single := decisionText("single", 100)
	if !strings.Contains(single, "a single PR") || strings.Contains(single, "chain") {
		t.Errorf(`decisionText("single", 100) = %q`, single)
	}
	chain := decisionText("chain", 450)
	if !strings.Contains(chain, "chain") {
		t.Errorf(`decisionText("chain", 450) = %q`, chain)
	}
	singleLarge := decisionText("single", 450)
	if !strings.Contains(singleLarge, "coherent") {
		t.Errorf(`decisionText("single", 450) = %q`, singleLarge)
	}
}

// createRecordFixture builds a record for the pr create tests.
func createRecordFixture(sha, result string, dims ...review.DimensionResult) review.Record {
	return review.Record{
		SHA:     sha,
		Message: "feat(x): change",
		Model:   "test",
		Revisions: []review.Revision{{
			At:     time.Now().UTC(),
			Result: result,
			Dims:   dims,
		}},
	}
}

// criticalFinding is a minimal CRITICAL finding for the gate.
func criticalFinding() review.ReviewFinding {
	return review.ReviewFinding{
		Dimension:   review.DimSecurity,
		File:        "internal/x/x.go",
		Line:        42,
		Severity:    review.SevCritical,
		Description: "secret in the log",
	}
}

// TestSemanticAdvisoryNoBlockNoAdvisory: without a block verdict there is
// nothing to warn about (before, gateBlock returned allowed=true; now it does
// not even decide whether to publish, T1.8 left that out of that path).
func TestSemanticAdvisoryNoBlockNoAdvisory(t *testing.T) {
	records := []review.Record{
		createRecordFixture("abc1234", review.VerdictOK,
			review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK}),
	}
	warn, blockers := semanticAdvisory(records)
	if warn {
		t.Fatal("without block there is nothing to warn about")
	}
	if len(blockers) != 0 {
		t.Fatalf("without blockers, list = %v", blockers)
	}
}

// TestSemanticAdvisoryWithBlockWarnsAndListsCriticals: the block verdict no
// longer prevents publishing (T1.8 makes it advisory); semanticAdvisory only
// signals the highlighted warning and returns the structured CRITICALs for the
// CLI to display.
func TestSemanticAdvisoryWithBlockWarnsAndListsCriticals(t *testing.T) {
	records := []review.Record{
		createRecordFixture("abc1234", review.VerdictBlock,
			review.DimensionResult{
				Dim:      review.DimSecurity,
				Verdict:  review.VerdictBlock,
				Findings: []review.ReviewFinding{criticalFinding()},
			}),
	}
	warn, blockers := semanticAdvisory(records)
	if !warn {
		t.Fatal("block must trigger the highlighted warning")
	}
	if len(blockers) != 1 {
		t.Fatalf("must list the CRITICAL, list = %v", blockers)
	}
	h := blockers[0]
	if h.Severity != review.SevCritical || h.Description != "secret in the log" {
		t.Errorf("the blocker must keep severity and description: %+v", h)
	}
}

// FU-6: the static revision verdict can remain block after a human answer,
// but the advisory warning must follow the effective blockers rather than
// printing an empty critical warning.
func TestSemanticAdvisoryWithDispositionsSkipsFullyRefutedBlock(t *testing.T) {
	records := []review.Record{{
		SHA: "abc1234",
		Revisions: []review.Revision{{
			Result: review.VerdictBlock,
			AggregatedFindings: []review.Finding{{
				Dimension: review.DimSecurity, Severity: review.SevCritical,
				Status: review.StatusConfirmed, Fingerprint: "fp-critical",
				Description: "refuted critical",
				Location:    review.Location{File: "a.go", LineStart: 2},
			}},
		}},
	}}
	dispositions := []review.FindingDisposition{{
		SHA: "abc1234", Fingerprint: "fp-critical", Status: review.StatusRefuted,
	}}

	warn, blockers := semanticAdvisoryWithDispositions(records, dispositions)
	if warn || len(blockers) != 0 {
		t.Fatalf("effective refutation = warn %t, blockers %+v; want no advisory", warn, blockers)
	}
}

// blockingRecordFp1 builds a single block record whose only critical carries
// fingerprint fp-1: the shared fixture for the FU-6 advisory-overlay tests.
func blockingRecordFp1(status string) review.Record {
	return review.Record{SHA: "abc1234", Revisions: []review.Revision{{
		Result: review.VerdictBlock,
		AggregatedFindings: []review.Finding{{
			Dimension: review.DimSecurity, Severity: review.SevCritical,
			Status: status, Fingerprint: "fp-1",
			Description: "critical fp-1",
			Location:    review.Location{File: "a.go", LineStart: 2},
		}},
	}}}
}

// depsPrCreateGreenBranch builds a green-validation single-record fixture;
// load carries the standing human answers (nil means none recorded).
func depsPrCreateGreenBranch(record review.Record, load func(string) ([]review.FindingDisposition, error)) depsPrCreate {
	return depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{record}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish:          func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/21", false, nil },
		recordEvent:      func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		loadDispositions: load,
	}
}

// FU-6: runPrCreateCon must read standing human answers through the
// loadDispositions seam, never through the real git common dir: these
// fixtures run with a fake worktree, so a direct production call would
// fail and turn every publish green-path red.
func TestRunPrCreateWith_ReadsDispositionsFromTheSeam(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"},
		depsPrCreateGreenBranch(recordOK, func(worktree string) ([]review.FindingDisposition, error) {
			if worktree != "worktree" {
				t.Errorf("loadDispositions worktree = %q, expected %q", worktree, "worktree")
			}
			return nil, errors.New("corrupt dispositions")
		}))
	if code != 1 {
		t.Fatalf("code = %d, expected 1 (the corrupt log fails closed)", code)
	}
	// The injected message must reach the output: later failure paths
	// (template write, publish) also exit 1, so the code alone cannot
	// tell the corrupt-log refusal apart from a downstream failure.
	if !strings.Contains(output.String(), "corrupt dispositions") {
		t.Errorf("the output must show the injected seam error, got %q", output.String())
	}
}

// FU-6: injected standing answers must drive the advisory overlay: a block
// record whose only critical is human-refuted publishes without the
// semantic warning.
func TestRunPrCreateWith_InjectedDispositionSuppressesAdvisory(t *testing.T) {
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"},
		depsPrCreateGreenBranch(blockingRecordFp1(review.StatusConfirmed),
			func(string) ([]review.FindingDisposition, error) {
				return []review.FindingDisposition{{
					SHA: "abc1234", Fingerprint: "fp-1", Status: review.StatusRefuted,
				}}, nil
			}))
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (the refuted blocker neither warns nor blocks): %s", code, output.String())
	}
	if strings.Contains(output.String(), "NOTICE") {
		t.Errorf("the refuted blocker must not warn: %s", output.String())
	}
}

// FU-6: the production deps must wire the real disposition loader, or pr
// create would publish as if no human ever answered.
func TestRealPrCreateDeps_WiresDispositionLoader(t *testing.T) {
	load := realPrCreateDeps().loadDispositions
	if load == nil {
		t.Fatal("realPrCreateDeps must wire loadDispositions")
	}
	// Identity, not just presence: the wired loader resolves the real git
	// common dir, so a bogus worktree must fail rather than report no
	// standing answers.
	if _, err := load(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("the wired loader must fail with a nonexistent worktree, not return empty")
	}
}

// Unit A: standing answers must reach the branch analysis so the net audit
// can carry them across SHAs, not only drive the advisory overlay after it.
func TestRunPrCreateWith_PassesDispositionsToBranch(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	dispositions := []review.FindingDisposition{{
		SHA: "abc1234", Fingerprint: "fp-carry", Status: review.StatusRefuted,
		Reason: "verified safe", Actor: review.RefutationActorHuman, Source: review.DispositionSourceHuman,
	}}
	var got review.BranchOptions
	deps := depsPrCreateGreenBranch(recordOK, func(string) ([]review.FindingDisposition, error) {
		return dispositions, nil
	})
	deps.analyzeBranch = func(string, review.BranchOptions) (*review.BranchResult, error) {
		return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
	}
	// Capture the options the command hands to the analysis.
	orig := deps.analyzeBranch
	deps.analyzeBranch = func(worktree string, o review.BranchOptions) (*review.BranchResult, error) {
		got = o
		return orig(worktree, o)
	}
	var output bytes.Buffer
	if code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"}, deps); code != 0 {
		t.Fatalf("code = %d, expected 0: %s", code, output.String())
	}
	if got.NetReview == nil || len(got.NetReview.Dispositions) != 1 || got.NetReview.Dispositions[0].Fingerprint != "fp-carry" {
		t.Fatalf("options.NetReview.Dispositions = %+v, want the standing answers for the net carry", got.NetReview)
	}
}

// Unit A: a corrupt dispositions log must fail before the branch analysis
// spends review tokens, not after it.
func TestRunPrCreateWith_CorruptLogDoesNotAuditBranch(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	analyzed := false
	deps := depsPrCreateGreenBranch(recordOK, func(string) ([]review.FindingDisposition, error) {
		return nil, errors.New("corrupt dispositions")
	})
	deps.analyzeBranch = func(string, review.BranchOptions) (*review.BranchResult, error) {
		analyzed = true
		return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
	}
	var output bytes.Buffer
	if code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"}, deps); code != 1 {
		t.Fatalf("code = %d, expected 1 (corrupt log fails closed)", code)
	}
	if analyzed {
		t.Fatal("the branch must not be audited with the corrupt log: zero tokens")
	}
}

// Unit A: the pr-review dispositions wiring reaches the net input so the
// cross-SHA carry-over observes standing answers, and a corrupt log aborts
// before the branch analysis spends review tokens.
func TestApplyPrReviewDispositionsCarriesAnswersToNet(t *testing.T) {
	dispositions := []review.FindingDisposition{{
		SHA: "abc1234", Fingerprint: "fp-carry", Status: review.StatusRefuted,
		Reason: "verified safe", Actor: review.RefutationActorHuman, Source: review.DispositionSourceHuman,
	}}
	options, err := applyPrReviewDispositions(
		review.BranchOptions{NetReview: &review.NetReviewOptions{Intention: "x"}},
		"worktree",
		func(string) ([]review.FindingDisposition, error) { return dispositions, nil })
	if err != nil {
		t.Fatalf("loader error: %v", err)
	}
	got := options.NetReview
	if got == nil || len(got.Dispositions) != 1 {
		t.Fatalf("net dispositions = %+v, want the standing answers", got)
	}
	d := got.Dispositions[0]
	if d.Fingerprint != "fp-carry" || d.Status != review.StatusRefuted || d.Actor != review.RefutationActorHuman {
		t.Fatalf("net disposition = %+v, want the recorded answer identity, not a count", d)
	}
}

func TestApplyPrReviewDispositionsFailsWithCorruptLog(t *testing.T) {
	intact := review.BranchOptions{NetReview: &review.NetReviewOptions{Intention: "x"}}
	_, err := applyPrReviewDispositions(intact, "worktree",
		func(string) ([]review.FindingDisposition, error) { return nil, errors.New("corrupt dispositions") })
	if err == nil {
		t.Fatal("corrupt log was swallowed: pr review would audit as if no human answered")
	}
}

// A nil net review (no net audit requested) leaves the options untouched and
// still reports a loader failure: the helper never invents a net input and
// never hides a corrupt log.
func TestApplyPrReviewDispositionsWithoutNet(t *testing.T) {
	options, err := applyPrReviewDispositions(review.BranchOptions{}, "worktree",
		func(string) ([]review.FindingDisposition, error) { return nil, nil })
	if err != nil || options.NetReview != nil {
		t.Fatalf("options = %+v, err = %v; want untouched options", options, err)
	}
	if _, err := applyPrReviewDispositions(review.BranchOptions{}, "worktree",
		func(string) ([]review.FindingDisposition, error) { return nil, errors.New("corrupt dispositions") }); err == nil {
		t.Fatal("corrupt log was swallowed without a net review")
	}
}

// FU-6: positive anchor for the suppression test above: an unrefuted block
// must render the semantic warning with the NOTICE token (frozen
// internal/app/pr wording), so a change that drops the token fails here
// instead of silently vacating the negative assertion.
func TestRunPrCreateWith_SemanticAdvisoryMentionsNOTICE(t *testing.T) {
	blockRecord := blockingRecordFp1(review.StatusConfirmed)
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"},
		depsPrCreateGreenBranch(blockRecord, nil))
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (the advisory warns but does not block): %s", code, output.String())
	}
	if !strings.Contains(output.String(), "NOTICE") {
		t.Errorf("the semantic advisory must mention NOTICE, got %q", output.String())
	}
}

func TestCopyToClipboardWithoutTool(t *testing.T) {
	err := copyToClipboardWith("body",
		func(string) bool { return false },
		func(string, string) error { t.Fatal("must not run anything"); return nil })
	if err == nil {
		t.Fatal("without an available tool it must fail explicitly")
	}
}

func TestCopyToClipboardFirstAvailable(t *testing.T) {
	var ran string
	err := copyToClipboardWith("body",
		func(name string) bool { return name == "wl-copy" },
		func(name, content string) error {
			ran = name
			if content != "body" {
				t.Errorf("content = %q, expected %q", content, "body")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("the fallback should not fail: %v", err)
	}
	if ran != "wl-copy" {
		t.Fatalf("it must use the first available tool, used %q", ran)
	}
}

func TestCopyToClipboardFailurePropagates(t *testing.T) {
	err := copyToClipboardWith("body",
		func(name string) bool { return name == "clip" },
		func(string, string) error { return errors.New("broken clip") })
	if err == nil || !strings.Contains(err.Error(), "clip") {
		t.Fatalf("the tool failure must propagate: %v", err)
	}
}

// TestVerifyForTemplateErrorNeverSilent: a verification failure is reflected
// as a reason in the template ("never silent" contract).
func TestVerifyForTemplateErrorNeverSilent(t *testing.T) {
	template := verifyForTemplateWith("worktree", "gitdir", config.Config{}, nil,
		func(ops.VerifyOptions) (ops.VerificationResult, error) {
			return ops.VerificationResult{}, errors.New("broken build")
		})
	if template.Mode != ops.ModeSkipped {
		t.Errorf("with an error the mode must be omitted, got %q", template.Mode)
	}
	if !strings.Contains(template.Reason, "verification_error") ||
		!strings.Contains(template.Reason, "broken build") {
		t.Errorf("the reason must reflect the real error, got %q", template.Reason)
	}
}

// TestVerifyForTemplateTranslatesCommands: a deterministic result is
// translated to TemplateVerification with its real exit code.
func TestVerifyForTemplateTranslatesCommands(t *testing.T) {
	template := verifyForTemplateWith("worktree", "gitdir", config.Config{}, nil,
		func(ops.VerifyOptions) (ops.VerificationResult, error) {
			return ops.VerificationResult{
				Mode: ops.ModeDeterministic,
				Commands: []ops.CommandResult{
					{Command: "go test ./...", Exit: 0},
					{Command: "go vet ./...", Exit: 1},
				},
			}, nil
		})
	if template.Mode != ops.ModeDeterministic {
		t.Errorf("mode = %q, expected deterministic", template.Mode)
	}
	if len(template.Comandos) != 2 {
		t.Fatalf("must translate the 2 commands, got %d", len(template.Comandos))
	}
	if template.Comandos[0].Comando != "go test ./..." || template.Comandos[0].Exit != 0 {
		t.Errorf("command 1 badly translated: %+v", template.Comandos[0])
	}
	if template.Comandos[1].Exit != 1 {
		t.Errorf("the exit code 1 must be preserved: %+v", template.Comandos[1])
	}
	if template.Reason != "" {
		t.Errorf("without an error there must be no reason, got %q", template.Reason)
	}
}

// TestVerifyForTemplateNilUsesTheRealPath: the defensive guard (verify nil ->
// ops.Verify) cannot be removed without breaking the test: without
// configured commands, the real path goes through the interactive notice and,
// with no answer on stdin, degrades to omitted without panic.
func TestVerifyForTemplateNilUsesTheRealPath(t *testing.T) {
	template := verifyForTemplateWith("worktree", "", config.Config{}, nil, nil)
	if template.Mode != ops.ModeSkipped {
		t.Errorf("with nil the real path must degrade to omitted, got %q", template.Mode)
	}
	if template.Reason != "aviso_no_respondio" {
		t.Errorf("without an answer to the notice the reason must be aviso_no_respondio, got %q", template.Reason)
	}
}

// TestPublishPRFallbackReReadsTheFile: without gh, the fallback body is
// re-read from the freshly written file (not from the lost parameter).
func TestPublishPRFallbackReReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(path, []byte("PR body"), 0o644); err != nil {
		t.Fatal(err)
	}
	var copied string
	url, fallback, err := publishPRWith("worktree", path, "", publishPROptions{
		ghAvailable: func(string) bool { return false },
		copy:        func(text string) error { copied = text; return nil },
	})
	if err != nil {
		t.Fatalf("the fallback should not fail: %v", err)
	}
	if !fallback {
		t.Fatal("without gh the fallback must activate")
	}
	if url != "" {
		t.Errorf("in the fallback the URL must stay empty, got %q", url)
	}
	if copied != "PR body" {
		t.Errorf("the copied body must be re-read from the file, got %q", copied)
	}
}

// TestPublishPRFallbackUnreadableFile: if the file disappears between the
// write and the re-read, the error is explicit and the fallback is marked.
func TestPublishPRFallbackUnreadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.md")
	_, fallback, err := publishPRWith("worktree", path, "", publishPROptions{
		ghAvailable: func(string) bool { return false },
		copy:        func(string) error { t.Fatal("without content it must not copy anything"); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "re-read") {
		t.Fatalf("the unreadable file must fail with a re-read notice: %v", err)
	}
	if !fallback {
		t.Fatal("the re-read failure is still a fallback")
	}
}

// TestPublishPRWithUsesGhWithExactArguments: with gh available, gh is used
// with the contract arguments (pr create --draft -F) and the worktree as cwd;
// the URL comes from gh's output, no clipboard.
func TestPublishPRWithUsesGhWithExactArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seenWorktree string
	var seenArgs []string
	url, fallback, err := publishPRWith("the-worktree", path, "", publishPROptions{
		ghAvailable: func(string) bool { return true },
		runGh: func(worktree string, args ...string) ([]byte, error) {
			seenWorktree = worktree
			seenArgs = args
			return []byte("https://github.com/example/repo/pull/9\n"), nil
		},
		copy: func(string) error { t.Fatal("with gh it must not use the clipboard"); return nil },
	})
	if err != nil {
		t.Fatalf("the fake gh should not fail: %v", err)
	}
	if fallback {
		t.Fatal("with gh the fallback must not activate")
	}
	if url != "https://github.com/example/repo/pull/9" {
		t.Errorf("the URL must come from gh (trimmed), got %q", url)
	}
	if seenWorktree != "the-worktree" {
		t.Errorf("gh must run with the worktree as cwd, got %q", seenWorktree)
	}
	expected := []string{"pr", "create", "--draft", "-F", path}
	if !reflect.DeepEqual(seenArgs, expected) {
		t.Errorf("gh arguments = %v, expected %v", seenArgs, expected)
	}
}

// TestPublishPRWithGhFailurePropagates: if gh ends with an error, its stderr
// propagates in the message and it does not fall back to the clipboard.
func TestPublishPRWithGhFailurePropagates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, fallback, err := publishPRWith("the-worktree", path, "", publishPROptions{
		ghAvailable: func(string) bool { return true },
		runGh:       func(string, ...string) ([]byte, error) { return nil, errors.New("gh: repo not configured") },
		copy:        func(string) error { t.Fatal("with a failed gh it must not copy"); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "gh: repo not configured") {
		t.Fatalf("the gh error must propagate: %v", err)
	}
	if fallback {
		t.Fatal("a gh failure is not a fallback (the fallback only applies without gh)")
	}
}

// TestPublishPRWithExplicitBase: when the user gives --base, that same base
// must propagate to gh pr create (the review and the PR cannot diverge).
func TestPublishPRWithExplicitBase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seenArgs []string
	_, _, err := publishPRWith("the-worktree", path, "develop", publishPROptions{
		ghAvailable: func(string) bool { return true },
		runGh: func(worktree string, args ...string) ([]byte, error) {
			seenArgs = args
			return []byte("https://github.com/example/repo/pull/11\n"), nil
		},
		copy: func(string) error { t.Fatal("with gh it must not use the clipboard"); return nil },
	})
	if err != nil {
		t.Fatalf("the fake gh should not fail: %v", err)
	}
	expected := []string{"pr", "create", "--draft", "--base", "develop", "-F", path}
	if !reflect.DeepEqual(seenArgs, expected) {
		t.Errorf("with --base the gh arguments = %v, expected %v", seenArgs, expected)
	}
}

// TestPublishPRWithEmptyBaseAddsNoFlag: without --base, gh uses the default
// upstream and receives no --base.
func TestPublishPRWithEmptyBaseAddsNoFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seenArgs []string
	_, _, err := publishPRWith("the-worktree", path, "", publishPROptions{
		ghAvailable: func(string) bool { return true },
		runGh: func(worktree string, args ...string) ([]byte, error) {
			seenArgs = args
			return []byte("https://github.com/PR/pull/12\n"), nil
		},
		copy: func(string) error { t.Fatal("with gh it must not use the clipboard"); return nil },
	})
	if err != nil {
		t.Fatalf("the fake gh should not fail: %v", err)
	}
	expected := []string{"pr", "create", "--draft", "-F", path}
	if !reflect.DeepEqual(seenArgs, expected) {
		t.Errorf("without --base the gh arguments = %v, expected %v", seenArgs, expected)
	}
}

func TestDetailPrCreateEvent(t *testing.T) {
	detail, err := detailPrCreateEvent("https://github.com/x/pr/1", false, false, false, "")
	if err != nil {
		t.Fatalf("the detail should not fail: %v", err)
	}
	var raw map[string]any
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("detail must be valid JSON: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("detail must be valid JSON: %v", err)
	}
	if raw["pr_url"] != "https://github.com/x/pr/1" {
		t.Errorf("pr_url = %v", raw["pr_url"])
	}
	if raw["fallback"] != false || raw["chain_pr"] != false || raw["force"] != false {
		t.Errorf("fallback/chain_pr/force = %v/%v/%v", raw["fallback"], raw["chain_pr"], raw["force"])
	}
	// "motivo" is the wire key internal/app/pr emits.
	if _, present := raw["motivo"]; present {
		t.Errorf("without force there must be no reason: %v", raw["motivo"])
	}
}

func TestDetailPrCreateEventFallbackAndChain(t *testing.T) {
	detail, err := detailPrCreateEvent("", true, true, false, "")
	if err != nil {
		t.Fatalf("the detail should not fail: %v", err)
	}
	var raw map[string]any
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("detail must be valid JSON: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("detail must be valid JSON: %v", err)
	}
	if raw["fallback"] != true || raw["chain_pr"] != true {
		t.Errorf("fallback/chain_pr = %v/%v", raw["fallback"], raw["chain_pr"])
	}
	if raw["pr_url"] != "" {
		t.Errorf("pr_url must stay empty in the fallback, = %v", raw["pr_url"])
	}
}

// TestDetailPrCreateEventForceWithReason: the --force exception is recorded in
// the event with the explicit reason (T1.8). The "motivo" key is the wire key
// internal/app/pr emits.
func TestDetailPrCreateEventForceWithReason(t *testing.T) {
	detail, err := detailPrCreateEvent("https://github.com/x/pr/2", false, false, true, "real reason")
	if err != nil {
		t.Fatalf("the detail should not fail: %v", err)
	}
	var raw map[string]any
	data, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("detail must be valid JSON: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("detail must be valid JSON: %v", err)
	}
	if raw["force"] != true || raw["motivo"] != "real reason" {
		t.Errorf("force/motivo = %v/%v", raw["force"], raw["motivo"])
	}
}

func TestParsePrCreateFlags(t *testing.T) {
	flags, err := parsePrCreateFlags([]string{"--base", "develop", "--chain-pr", "--force", "--reason", "real reason"})
	if err != nil {
		t.Fatalf("parsing should not fail: %v", err)
	}
	if flags.base != "develop" || !flags.chainPR || !flags.force || flags.reason != "real reason" {
		t.Errorf("flags = %+v", flags)
	}
}

func TestParsePrCreateFlagsBaseWithoutValue(t *testing.T) {
	if _, err := parsePrCreateFlags([]string{"--base"}); err == nil {
		t.Fatal("--base without a value must fail")
	}
}

func TestParsePrCreateFlagsUnknown(t *testing.T) {
	if _, err := parsePrCreateFlags([]string{"--nope"}); err == nil {
		t.Fatal("unknown option must fail")
	}
}

// TestParsePrCreateFlagsForceWithoutReason: --force without --reason is an
// explicit error (T1.8): the validation is the only real gate and forcing it
// without a reason cannot stay silent.
func TestParsePrCreateFlagsForceWithoutReason(t *testing.T) {
	_, err := parsePrCreateFlags([]string{"--force"})
	if err == nil {
		t.Fatal("--force without --reason must fail")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("the error must ask for the reason, got: %v", err)
	}
}

// TestParsePrCreateFlagsReasonWithoutValue: --reason without a value fails
// just like the rest of the value flags.
func TestParsePrCreateFlagsReasonWithoutValue(t *testing.T) {
	if _, err := parsePrCreateFlags([]string{"--force", "--reason"}); err == nil {
		t.Fatal("--reason without a value must fail")
	}
}

// TestRunPrCreateWith_RedValidationWithoutForce_NoPublishNoBranchAudit covers
// the central rule of T1.8: if the validation fails without --force, nothing
// is published and not one token is spent on the semantic review (AnalyzeBranch
// is never called).
func TestRunPrCreateWith_RedValidationWithoutForce_NoPublishNoBranchAudit(t *testing.T) {
	var analyzeBranchCalled, publishCalled bool
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", nil, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1, Output: "FAIL"}}, nil
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			analyzeBranchCalled = true
			return nil, nil
		},
		publish: func(string, string, string) (string, bool, error) {
			publishCalled = true
			return "", false, nil
		},
	})
	if code != 1 {
		t.Fatalf("code = %d, expected 1", code)
	}
	if analyzeBranchCalled {
		t.Fatal("the red validation without --force must not call AnalyzeBranch: zero tokens")
	}
	if publishCalled {
		t.Fatal("the red validation without --force must not publish")
	}
	if !strings.Contains(output.String(), "go test ./...") || !strings.Contains(output.String(), "FAIL") {
		t.Errorf("it must list the red command with its real output: %s", output.String())
	}
}

func TestRunPrCreateWith_SharesTheModelVerifierWithTheTemplate(t *testing.T) {
	previous := newModelVerifier
	t.Cleanup(func() { newModelVerifier = previous })
	var constructions int
	newModelVerifier = func(string) *modelprobe.Verifier {
		constructions++
		return modelprobe.NewVerifier(nil)
	}

	var output bytes.Buffer
	// verify delegates to verifyForTemplateWith with verify=nil, which in turn
	// falls into the real ops.Verify path: that road really calls
	// ops.RecordEvent(gitDir, "pr-verify", ...). A literal "gitdir" here
	// would write outside a temporary directory, into <cwd>/gitdir/vas-sentinel
	// (cwd = the package during `go test`), polluting the repository tree on
	// every run. t.TempDir() keeps the real write this test needs to cover the
	// path, without touching the repository.
	gitDir := t.TempDir()
	code := runPrCreateCon(&output, "worktree", nil, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return gitDir, nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{createRecordFixture("abc1234", review.VerdictOK)}, SHAs: []string{"abc1234"}}, nil
		},
		verify: func(worktree, gitDir string, cfg config.Config, modelVerifier *modelprobe.Verifier) review.TemplateVerification {
			return verifyForTemplateWith(worktree, gitDir, cfg, modelVerifier, nil)
		},
		publish:     func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/1", false, nil },
		recordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0: %s", code, output.String())
	}
	if constructions != 1 {
		t.Fatalf("verifier constructions = %d, expected 1 per pr create invocation", constructions)
	}
}

func TestRefuterFactoryResolvesCheapProfile(t *testing.T) {
	cfg := configWithCheapProfile()
	agent, profile, err := refuterFactory(cfg, modelprobe.NewVerifier(nil))()
	if err != nil {
		t.Fatalf("refuterFactory() error = %v", err)
	}
	if profile != "cheap" {
		t.Fatalf("profile = %q, want cheap", profile)
	}
	adapter, ok := agent.(*agentadapter.CLIAdapter)
	if !ok {
		t.Fatalf("agent = %T, want *agentadapter.CLIAdapter", agent)
	}
	if adapter.Config.Model != "cheap-model" || adapter.Config.ReasoningEffort != "low" {
		t.Fatalf("refuter configuration = %+v, want cheap profile", adapter.Config)
	}
}

func TestAuditOptionsWithRefuterCarriesCheapProfile(t *testing.T) {
	options := auditOptionsWithRefuter(review.AuditOptions{}, configWithCheapProfile(), modelprobe.NewVerifier(nil))
	if options.RefuterFactory == nil {
		t.Fatal("AuditOptions must carry a refuter factory")
	}
	_, profile, err := options.RefuterFactory()
	if err != nil {
		t.Fatalf("RefuterFactory() error = %v", err)
	}
	if profile != "cheap" {
		t.Fatalf("refuter profile = %q, want cheap", profile)
	}
}

func TestBranchOptionsWithRefuterCarriesCheapProfile(t *testing.T) {
	options := branchOptionsWithRefuter(configWithCheapProfile(), modelprobe.NewVerifier(nil), review.BranchOptions{})
	if options.RefuterFactory == nil {
		t.Fatal("BranchOptions must carry a refuter factory")
	}
	_, profile, err := options.RefuterFactory()
	if err != nil {
		t.Fatalf("RefuterFactory() error = %v", err)
	}
	if profile != "cheap" {
		t.Fatalf("refuter profile = %q, want cheap", profile)
	}
}

func TestRunPrCreateWithPassesCheapRefuterFactory(t *testing.T) {
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", nil, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return configWithCheapProfile(), nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		analyzeBranch: func(_ string, opts review.BranchOptions) (*review.BranchResult, error) {
			if opts.RefuterFactory == nil {
				t.Fatal("BranchOptions must carry a refuter factory")
			}
			_, profile, err := opts.RefuterFactory()
			if err != nil {
				t.Fatalf("RefuterFactory() error = %v", err)
			}
			if profile != "cheap" {
				t.Fatalf("refuter profile = %q, want cheap", profile)
			}
			return &review.BranchResult{Records: []review.Record{createRecordFixture("abc1234", review.VerdictOK)}, SHAs: []string{"abc1234"}}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish:     func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/1", false, nil },
		recordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0: %s", code, output.String())
	}
}

func configWithCheapProfile() config.Config {
	return config.Config{
		ActiveAgent: "stub",
		Agents: map[string]config.AgentConfig{
			"stub": {
				Model: "normal-model",
				Profiles: map[string]config.ProfileConfig{
					"cheap": {Model: "cheap-model", ReasoningEffort: "low"},
				},
			},
		},
	}
}

// TestRunPrCreateWith_ForceWithoutReason_ErrorsWithoutTouchingAnything: --force
// without --reason fails at parsing, before touching config/git/validation.
func TestRunPrCreateWith_ForceWithoutReason_ErrorsWithoutTouchingAnything(t *testing.T) {
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) {
			t.Fatal("must not load configuration without --reason: the error is a parse error")
			return config.Config{}, nil
		},
	})
	if code != 1 {
		t.Fatalf("code = %d, expected 1", code)
	}
	if !strings.Contains(output.String(), "reason") {
		t.Errorf("it must ask for the reason explicitly: %s", output.String())
	}
}

// TestRunPrCreateWith_ForceWithReason_PublishesAndRecordsException covers the
// third acceptance scenario: with --force --reason, the red validation is
// overridden, it publishes anyway and the event records force+reason.
func TestRunPrCreateWith_ForceWithReason_PublishesAndRecordsException(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var recordedDetail any
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "real reason"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		getHeadSHA: func() (string, error) { return "abc1234", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1}}, nil
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/9", false, nil },
		recordEvent: func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error {
			recordedDetail = detail
			return nil
		},
		getGitCommonDir: func(string) (string, error) { return "commondir", nil },
		recordDecision:  func(string, *store.Decision) error { return nil },
		resolveActor:    func(string) string { return "test-actor" },
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (--force publishes anyway)", code)
	}
	var raw map[string]any
	data, err := json.Marshal(recordedDetail)
	if err != nil {
		t.Fatalf("the event detail must be valid JSON: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("the event detail must be valid JSON: %v\n%s", err, data)
	}
	if raw["force"] != true || raw["motivo"] != "real reason" {
		t.Errorf("the event must record force and reason, got %+v", raw)
	}
}

// TestRunPrCreateWith_ForceWithRedValidation_PropagatesDeterministicFindings
// covers the wiring that activates T6.2 in production: with --force and a
// red validation, the deterministic findings projected from that validation
// must reach review.AnalyzeBranch via BranchOptions.DeterministicFindings, so
// AuditCommit can supersede the equivalent semantic finding.
func TestRunPrCreateWith_ForceWithRedValidation_PropagatesDeterministicFindings(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var receivedOptions review.BranchOptions
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "real reason"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		getHeadSHA: func() (string, error) { return "abc1234", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "lint", Command: "go vet ./...", Exit: 1}}, nil
		},
		analyzeBranch: func(_ string, opts review.BranchOptions) (*review.BranchResult, error) {
			receivedOptions = opts
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/12", false, nil },
		recordEvent: func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error {
			return nil
		},
		getGitCommonDir: func(string) (string, error) { return "commondir", nil },
		recordDecision:  func(string, *store.Decision) error { return nil },
		resolveActor:    func(string) string { return "test-actor" },
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (--force publishes anyway)", code)
	}
	if receivedOptions.DeterministicFindingsSHA != "abc1234" {
		t.Errorf("DeterministicFindingsSHA = %q, expected the validated HEAD sha", receivedOptions.DeterministicFindingsSHA)
	}
	if len(receivedOptions.DeterministicFindings) != 1 {
		t.Fatalf("DeterministicFindings = %#v, expected 1 projected finding", receivedOptions.DeterministicFindings)
	}
	got := receivedOptions.DeterministicFindings[0]
	if got.Source != review.SourceValidation {
		t.Errorf("Source = %q, expected %q", got.Source, review.SourceValidation)
	}
	if got.Dimension != review.DimStyle {
		t.Errorf("Dimension = %q, expected %q for capability 'lint'", got.Dimension, review.DimStyle)
	}
}

// TestRunPrCreateWith_ForceWithRedValidation_UnresolvableHeadWarnsAndContinues
// is a regression test: when getHeadSHA fails, the command must not
// silently drop the deterministic findings without a trace. It still
// publishes (this failure is unrelated to --force's own decision to
// continue), but it must print an explicit warning and must not
// bind the projected findings to any commit (DeterministicFindingsSHA
// stays empty, so AnalyzeBranch can never mismatch them to the wrong SHA).
func TestRunPrCreateWith_ForceWithRedValidation_UnresolvableHeadWarnsAndContinues(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var receivedOptions review.BranchOptions
	var published bool
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "real reason"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		getHeadSHA: func() (string, error) { return "", errors.New("unresolvable HEAD") },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "lint", Command: "go vet ./...", Exit: 1}}, nil
		},
		analyzeBranch: func(_ string, opts review.BranchOptions) (*review.BranchResult, error) {
			receivedOptions = opts
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish: func(string, string, string) (string, bool, error) {
			published = true
			return "https://github.com/x/pr/14", false, nil
		},
		recordEvent: func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error {
			return nil
		},
		getGitCommonDir: func(string) (string, error) { return "commondir", nil },
		recordDecision:  func(string, *store.Decision) error { return nil },
		resolveActor:    func(string) string { return "test-actor" },
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (the HEAD failure does not block --force)", code)
	}
	if !published {
		t.Error("the PR was expected to publish despite the HEAD failure")
	}
	// The warning text is internal/app/pr output.
	if !strings.Contains(output.String(), "could not resolve the validated commit") {
		t.Errorf("an explicit warning of the HEAD failure was expected, got: %s", output.String())
	}
	if receivedOptions.DeterministicFindingsSHA != "" {
		t.Errorf("DeterministicFindingsSHA = %q, expected empty when HEAD couldn't be resolved", receivedOptions.DeterministicFindingsSHA)
	}
	if len(receivedOptions.DeterministicFindings) != 1 {
		t.Fatalf("DeterministicFindings = %#v, expected the projected finding to be preserved even without a bound SHA", receivedOptions.DeterministicFindings)
	}
	if got := receivedOptions.DeterministicFindings[0]; got.Source != review.SourceValidation || got.Dimension != review.DimStyle {
		t.Errorf("preserved finding = %#v, expected Source=%q Dimension=%q (projected from the 'lint' capability)", got, review.SourceValidation, review.DimStyle)
	}
}

// TestRunPrCreateWith_ForceWithGreenValidation_PropagatesNoDeterministicFindings
// is the complement: with no red validation to force through, there is
// nothing deterministic to supersede with, so the collection must stay
// empty rather than accidentally leaking stale state.
func TestRunPrCreateWith_ForceWithGreenValidation_PropagatesNoDeterministicFindings(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var receivedOptions review.BranchOptions
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "real reason"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil // green validation: no runs, no findings
		},
		analyzeBranch: func(_ string, opts review.BranchOptions) (*review.BranchResult, error) {
			receivedOptions = opts
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/13", false, nil },
		recordEvent: func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error {
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0", code)
	}
	if len(receivedOptions.DeterministicFindings) != 0 {
		t.Errorf("DeterministicFindings = %#v, expected empty without a forced red validation", receivedOptions.DeterministicFindings)
	}
}

// TestRunPrCreateWith_ForceWithGreenValidation_DoesNotRecordAnExceptionThatNeverHappened
// covers Fix 2 (orchestrator finding): --force --reason with the validation
// ALREADY green (no findings) has no real effect to override, so it must not
// warn about an "overridden validation" that never happened, and the event
// must record force:false without a reason — the flag existed in the
// invocation but exercised no effect.
func TestRunPrCreateWith_ForceWithGreenValidation_DoesNotRecordAnExceptionThatNeverHappened(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var recordedDetail any
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "real reason"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil // green validation: no runs, no findings
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish: func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/11", false, nil },
		recordEvent: func(gitDir, kind string, exit int, shas []string, detail ops.EventDetail, worktree string) error {
			recordedDetail = detail
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (no findings, publishes anyway)", code)
	}
	// "overridden" is internal/app/pr output.
	if strings.Contains(output.String(), "overridden") {
		t.Errorf("with no findings to override it must not warn about an overridden validation: %s", output.String())
	}
	var raw map[string]any
	data, err := json.Marshal(recordedDetail)
	if err != nil {
		t.Fatalf("the event detail must be valid JSON: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("the event detail must be valid JSON: %v\n%s", err, data)
	}
	if raw["force"] != false {
		t.Errorf("force must record false: there was nothing to force, got %+v", raw)
	}
	if _, present := raw["motivo"]; present {
		t.Errorf("without a real --force effect no reason may remain in the event: %v", raw)
	}
}

// TestRunPrCreateWith_ForceWithRedValidation_RecordsForceBypassDecision covers
// T7.5 (M3 report): --force that really overrides a red validation must record
// a store.Decision with Decision="force_bypass" and Reason=the --reason,
// exactly once.
func TestRunPrCreateWith_ForceWithRedValidation_RecordsForceBypassDecision(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var calls int
	var recordedDecision *store.Decision
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		getHeadSHA: func() (string, error) { return "abc1234", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1}}, nil
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish:     func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/20", false, nil },
		recordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		getGitCommonDir: func(worktree string) (string, error) {
			if worktree != "worktree" {
				t.Errorf("getGitCommonDir worktree = %q, expected %q", worktree, "worktree")
			}
			return "commondir", nil
		},
		recordDecision: func(commonDir string, d *store.Decision) error {
			calls++
			if commonDir != "commondir" {
				t.Errorf("commonDir = %q, expected %q", commonDir, "commondir")
			}
			recordedDecision = d
			return nil
		},
		resolveActor: func(string) string { return "test-actor" },
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (--force publishes anyway)", code)
	}
	if calls != 1 {
		t.Fatalf("recordDecision was called %d times, expected exactly 1", calls)
	}
	if recordedDecision == nil {
		t.Fatal("no decision was recorded")
	}
	if recordedDecision.Decision != "force_bypass" {
		t.Errorf("Decision = %q, expected %q", recordedDecision.Decision, "force_bypass")
	}
	if recordedDecision.Reason != "x" {
		t.Errorf("Reason = %q, expected %q", recordedDecision.Reason, "x")
	}
	if recordedDecision.Actor != "test-actor" {
		t.Errorf("Actor = %q, expected %q (it must come from deps.resolveActor, not from a real git)", recordedDecision.Actor, "test-actor")
	}
}

// TestRunPrCreateWith_ForceWithGreenValidation_RecordsNoDecision covers the
// complement: --force present but WITHOUT real effect (validation already
// green, same as the red-validation distinction above) must not record any
// bypass decision, because there was no bypass to record.
func TestRunPrCreateWith_ForceWithGreenValidation_RecordsNoDecision(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var recordDecisionCalled bool
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil // green validation: --force has nothing to override
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish:     func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/21", false, nil },
		recordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		getGitCommonDir: func(string) (string, error) {
			t.Fatal("getGitCommonDir must not be called: --force had no real effect to record")
			return "", nil
		},
		recordDecision: func(string, *store.Decision) error {
			recordDecisionCalled = true
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0", code)
	}
	if recordDecisionCalled {
		t.Error("recordDecision must not be called: the validation was already green, --force exercised no effect")
	}
}

// TestRunPrCreateWith_ForceWithRedValidation_GitCommonDirFailureWarnsAndContinues
// covers warn-and-continue when getGitCommonDir fails: --force already decided
// to continue despite the red validation, so a failure resolving where to
// record the decision must not abort the publication, only warn.
func TestRunPrCreateWith_ForceWithRedValidation_GitCommonDirFailureWarnsAndContinues(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		getHeadSHA: func() (string, error) { return "abc1234", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1}}, nil
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish:     func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/22", false, nil },
		recordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		getGitCommonDir: func(string) (string, error) {
			return "", errors.New("boom")
		},
		recordDecision: func(string, *store.Decision) error {
			t.Fatal("recordDecision must not be called: the commonDir could not be resolved")
			return nil
		},
		resolveActor: func(string) string { return "test-actor" },
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (the commonDir resolution failure does not block --force)", code)
	}
	// The warning text is internal/app/pr output.
	if !strings.Contains(output.String(), "could not resolve the git-common-dir") {
		t.Errorf("the output must warn about the commonDir resolution failure, got: %s", output.String())
	}
}

// TestRunPrCreateWith_ForceWithRedValidation_RecordDecisionFailureWarnsAndContinues
// covers warn-and-continue when recordDecision fails (e.g. it could not write
// decisions.jsonl): same criterion, it does not abort the publication.
func TestRunPrCreateWith_ForceWithRedValidation_RecordDecisionFailureWarnsAndContinues(t *testing.T) {
	recordOK := createRecordFixture("abc1234", review.VerdictOK,
		review.DimensionResult{Dim: review.DimLogic, Verdict: review.VerdictOK})
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", []string{"--force", "--reason", "x"}, depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gitdir", nil },
		getHeadSHA: func() (string, error) { return "abc1234", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1}}, nil
		},
		analyzeBranch: func(string, review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Records: []review.Record{recordOK}, SHAs: []string{"abc1234"}, Decision: "single"}, nil
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish:         func(string, string, string) (string, bool, error) { return "https://github.com/x/pr/23", false, nil },
		recordEvent:     func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		getGitCommonDir: func(string) (string, error) { return "commondir", nil },
		recordDecision: func(string, *store.Decision) error {
			return errors.New("boom")
		},
		resolveActor: func(string) string { return "test-actor" },
	})
	if code != 0 {
		t.Fatalf("code = %d, expected 0 (the decision write failure does not block --force)", code)
	}
	// The warning text is internal/app/pr output.
	if !strings.Contains(output.String(), "could not write the --force decision") {
		t.Errorf("the output must warn about the decision write failure, got: %s", output.String())
	}
}

// TestResolveActor_UsesWorktreeLocalGitConfig confirms that resolveActor uses
// cmd.Dir=worktree (not the cwd of the process running the test): a local
// user.name of the worktree must win, even though the test process runs in
// another directory (this very repository).
func TestResolveActor_UsesWorktreeLocalGitConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("isolating HOME/GIT_CONFIG_NOSYSTEM deterministically on Windows needs more than this helper")
	}
	worktree := t.TempDir()
	if out, err := exec.Command("git", "-C", worktree, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", worktree, "config", "user.name", "worktree-local-actor").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	if got := resolveActor(worktree); got != "worktree-local-actor" {
		t.Errorf("resolveActor(worktree) = %q, expected the worktree's LOCAL user.name, not the process cwd's", got)
	}
}

// TestResolveActor_WithoutGitFallsBackToUserThenUsername confirms the full
// fallback chain when git config cannot resolve any name (neither local nor
// global nor system): $USER first, $USERNAME if $USER is empty, and "unknown"
// if both are. HOME is redirected to an empty directory and
// GIT_CONFIG_NOSYSTEM=1 so the result does not depend on the real git
// configuration of the machine running the test.
func TestResolveActor_WithoutGitFallsBackToUserThenUsername(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("isolating HOME/GIT_CONFIG_NOSYSTEM deterministically on Windows needs more than this helper")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	worktreeWithoutGit := t.TempDir() // not a git repo: no local config possible

	t.Run("falls back to USER", func(t *testing.T) {
		t.Setenv("USER", "env-user")
		t.Setenv("USERNAME", "")
		if got := resolveActor(worktreeWithoutGit); got != "env-user" {
			t.Errorf("resolveActor = %q, expected %q ($USER)", got, "env-user")
		}
	})
	t.Run("without USER falls back to USERNAME", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("USERNAME", "windows-env-user")
		if got := resolveActor(worktreeWithoutGit); got != "windows-env-user" {
			t.Errorf("resolveActor = %q, expected %q ($USERNAME)", got, "windows-env-user")
		}
	})
	t.Run("without any variable uses the placeholder", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("USERNAME", "")
		if got := resolveActor(worktreeWithoutGit); got != "unknown" {
			t.Errorf("resolveActor = %q, expected %q", got, "unknown")
		}
	})
}

// TestRunPrCreateWith_ConfigLoadError_Exit1WithoutValidatingOrPublishing covers
// Fix 3 (orchestrator finding): pr create must use the STRICT config load
// (config.LoadStrictLocalConfig in production, see runPrCreate).
// With a broken yml, it must cut right here with exit 1 and the error
// visible, without reaching the validation or publishing anything.
func TestRunPrCreateWith_ConfigLoadError_Exit1WithoutValidatingOrPublishing(t *testing.T) {
	var validationCalled, publishCalled bool
	var output bytes.Buffer
	code := runPrCreateCon(&output, "worktree", nil, depsPrCreate{
		loadConfig: func(string) (config.Config, error) {
			return config.Config{}, errors.New("vassentinel.yml: line 2: field missing_key not found in type config.Config")
		},
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			validationCalled = true
			return nil, nil
		},
		publish: func(string, string, string) (string, bool, error) {
			publishCalled = true
			return "", false, nil
		},
	})
	if code != 1 {
		t.Fatalf("code = %d, expected 1", code)
	}
	if !strings.Contains(output.String(), "line") {
		t.Errorf("the broken yml error must stay visible in the output, got: %s", output.String())
	}
	if validationCalled {
		t.Fatal("with the broken yml it must not reach the validation")
	}
	if publishCalled {
		t.Fatal("with the broken yml it must not publish anything")
	}
}

func TestExecutePrCreateWith_StackAndNetAuthority(t *testing.T) {
	res := &review.BranchResult{
		Records: []review.Record{createRecordFixture("abc1234", review.VerdictOK)}, SHAs: []string{"abc1234"}, Decision: "single",
		Net: &review.NetReview{Audit: review.AuditResult{Verdict: review.VerdictBlock,
			Findings: []review.Finding{{Dimension: review.DimSecurity, Severity: review.SevCritical, Description: "secret logged"}}}},
		Inherited: []review.InheritedFinding{{SHA: "deadbeefcafe", Finding: review.Finding{Dimension: review.DimLogic, Severity: review.SevCritical}}},
	}
	var opts review.BranchOptions
	pubBase, body := "", ""
	output := &bytes.Buffer{}
	deps := depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gd", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		analyzeBranch: func(_ string, o review.BranchOptions) (*review.BranchResult, error) { opts = o; return res, nil },
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		publish: func(_, path, base string) (string, bool, error) {
			pubBase = base
			data, _ := os.ReadFile(path)
			body = string(data)
			return "https://x/pr/1", false, nil
		},
		recordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
	}
	res.Own = &review.OwnRange{Parent: "layer-a", PublicationBranch: "layer-a"}
	code := runPrCreateCon(output, "wt", []string{"--parent", "layer-a", "--chain-pr"}, deps)
	if code != 0 || pubBase != "layer-a" || *opts.OwnDiff != (review.OwnDiffOptions{Parent: "layer-a"}) ||
		opts.NetReview == nil || opts.NetReview.Intention != honestNetIntention {
		t.Errorf("stacked: exitCode=%d base=%q own=%v net=%v", code, pubBase, opts.OwnDiff, opts.NetReview)
	}
	for _, want := range []string{"Net audit verdict: block", "secret logged", "OWN (per-commit audit)", "INHERITED (non-blocking)", "deadbee"} {
		if !strings.Contains(body, want) {
			t.Errorf("published body lacks %q: %s", want, body)
		}
	}
	if strings.Contains(body, "No pending risks") {
		t.Errorf("a net BLOCK must not claim a risk-free body: %s", body)
	}
	pubBase, output = "", new(bytes.Buffer)
	code = runPrCreateCon(output, "wt", []string{"--chain-pr"}, deps)
	if code != 0 ||
		pubBase != "layer-a" ||
		opts.OwnDiff == nil ||
		*opts.OwnDiff != (review.OwnDiffOptions{ResolveParent: true}) {
		t.Errorf("chain-only: base=%q own=%v", pubBase, opts.OwnDiff)
	}
	res.Own = &review.OwnRange{Parent: "layer-a"}
	pubBase, output = "", new(bytes.Buffer)
	templateCreated, publishCalled := false, false
	deps.writeTemplate = func(string) (string, error) { templateCreated = true; return "/tmp/sentinel_pr_fake.md", nil }
	originalPublish := deps.publish
	deps.publish = func(wt, path, base string) (string, bool, error) {
		publishCalled = true
		return originalPublish(wt, path, base)
	}
	code = runPrCreateCon(output, "wt", []string{"--parent", "layer-a"}, deps)
	if code != 1 ||
		pubBase != "" ||
		opts.OwnDiff == nil ||
		*opts.OwnDiff != (review.OwnDiffOptions{Parent: "layer-a"}) ||
		templateCreated ||
		publishCalled {
		t.Errorf("missing publication branch: base=%q own=%v templateCreated=%v publishCalled=%v output=%q", pubBase, opts.OwnDiff, templateCreated, publishCalled, output.String())
	}
	deps.writeTemplate = nil
	deps.publish = originalPublish
	res.Net.Audit.Verdict = review.VerdictOK
	res.Net.Audit.Findings, res.Records, res.Own = nil, []review.Record{createRecordFixture("abc1234", review.VerdictBlock)}, nil
	pubBase, output = "", new(bytes.Buffer)
	code = runPrCreateCon(output, "wt", nil, deps)
	if code != 0 || strings.Contains(output.String(), "NOTICE") || !strings.Contains(body, "Net audit verdict: ok") {
		t.Errorf("net OK over historical BLOCK must not warn: %s / %s", output.String(), body)
	}
	res.Own, res.Net, res.Inherited = nil, nil, nil
	pubBase = ""
	code = runPrCreateCon(output, "wt", nil, deps)
	if code != 0 || pubBase != "main" {
		t.Errorf("legacy: exitCode=%d base=%q, want 0/main", code, pubBase)
	}
}

// TestResolveBlobStoreResolvesTheCommonDir covers the F8 criterion 2 wiring at its
// only nil-able point: blob reuse in AnalyzeBranch is gated on Store != nil, so
// a helper that silently returned nil inside a real repository would make the
// criterion unreachable from the PR commands without any test noticing.
func TestResolveBlobStoreResolvesTheCommonDir(t *testing.T) {
	repo := tempGitRepo(t)
	mainStore, err := resolveBlobStore(repo)
	if err != nil {
		t.Fatalf("resolveBlobStore inside a real repository: %v", err)
	}
	if mainStore == nil {
		t.Fatal("resolveBlobStore returned a nil store inside a real repository: blob reuse would never fire and a base rebase would re-audit everything")
	}

	// The contract the doc comment declares mandatory: linked worktrees share
	// ONE store. A plain repository cannot prove it, because there the
	// per-worktree git dir and the common dir are the same path; only a linked
	// worktree distinguishes GetGitCommonDir from GetGitDir.
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "linked", linked).CombinedOutput(); err != nil {
		t.Skipf("git worktree add is unavailable: %v\n%s", err, out)
	}
	fromLinked, err := resolveBlobStore(linked)
	if err != nil {
		t.Fatalf("resolveBlobStore inside a linked worktree: %v", err)
	}
	if !reflect.DeepEqual(mainStore, fromLinked) {
		t.Errorf("the linked worktree resolved a different store (%v) than the main worktree (%v): reviews would not be shared across worktrees", fromLinked, mainStore)
	}

	// Outside a repository the reuse optimization must report the failure and
	// return no store, so the caller degrades instead of publishing with a
	// half-built one.
	if st, err := resolveBlobStore(t.TempDir()); err == nil || st != nil {
		t.Errorf("resolveBlobStore outside a repository = (%v, %v), expected (nil, error)", st, err)
	}
}

// TestExecutePrCreateWiresTheBlobStore is the pr create half of F8 criterion 2:
// the command must hand AnalyzeBranch the shared blob store, otherwise rebasing
// the stack base re-audits every commit.
func TestExecutePrCreateWiresTheBlobStore(t *testing.T) {
	var opts review.BranchOptions
	deps := depsPrCreate{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		getGitDir:  func() (string, error) { return "gd", nil },
		runValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return nil, nil
		},
		blobStore: func(string) (review.StoreBlobs, error) {
			return store.NewStore(t.TempDir()), nil
		},
		analyzeBranch: func(_ string, o review.BranchOptions) (*review.BranchResult, error) {
			opts = o
			return nil, errors.New("cut the flow right after AnalyzeBranch: this test only observes its options")
		},
		verify: func(string, string, config.Config, *modelprobe.Verifier) review.TemplateVerification {
			return review.TemplateVerification{Mode: "omitido"}
		},
		recordEvent:  func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		resolveActor: func(string) string { return "actor" },
	}
	runPrCreateCon(&bytes.Buffer{}, "wt", nil, deps)
	if opts.Store == nil {
		t.Error("BranchOptions.Store is nil: pr create never reaches blob reuse, so a base rebase re-audits the whole stack")
	}
}

// tempGitRepo creates a throwaway repository with one commit. The
// blob-store wiring tests need a real one because resolveBlobStore shells out
// to git rev-parse for the common dir.
func tempGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"add", "base.txt"},
		{"commit", "-q", "-m", "feat(base): base"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

// TestBranchReviewOptionsWiresTheBlobStore covers the pr review half of F8
// criterion 2. runPrReview calls os.Exit and cannot be driven from a test,
// so the store assignment lives in this extracted assembler; without this test,
// deleting it would fail nothing.
func TestBranchReviewOptionsWiresTheBlobStore(t *testing.T) {
	// Assert everything that needs no repository FIRST, on the failure path
	// itself: outside a repository the caller only warns, so the options must
	// still come back fully usable. Ordering it this way also keeps these
	// assertions out of reach of tempGitRepo's skip when git is absent.
	outside, storeWarning := branchReviewOptions(config.Config{}, nil, t.TempDir(), flagsPrReview{parent: "layer-a"}, nil, os.Stdout)
	if storeWarning == nil {
		t.Error("expected an error outside a repository so the caller can warn")
	}
	if outside.Store != nil {
		t.Errorf("Store = %v outside a repository, expected nil", outside.Store)
	}
	if outside.Base != "main" {
		t.Errorf("Base = %q with an empty --base, expected \"main\"", outside.Base)
	}
	if outside.OwnDiff == nil || outside.OwnDiff.Parent != "layer-a" {
		t.Errorf("OwnDiff = %+v, expected the explicit --parent to reach the stacked own-diff", outside.OwnDiff)
	}
	if outside.NetReview == nil {
		t.Error("NetReview is nil: the PR would be reviewed as the sum of its commits")
	}
	if outside.RefuterFactory == nil {
		t.Error("RefuterFactory is nil: the options no longer travel through branchOptionsWithRefuter")
	}

	repo := tempGitRepo(t)
	options, storeWarning := branchReviewOptions(config.Config{}, nil, repo, flagsPrReview{parent: "layer-a"}, nil, os.Stdout)
	if storeWarning != nil {
		t.Fatalf("branchReviewOptions inside a real repository: %v", storeWarning)
	}
	expected, err := resolveBlobStore(repo)
	if err != nil {
		t.Fatalf("resolveBlobStore: %v", err)
	}
	if !reflect.DeepEqual(options.Store, expected) {
		t.Errorf("BranchOptions.Store = %v, expected the resolved blob store %v: pr review would never reach blob reuse, so a base rebase re-audits the whole stack", options.Store, expected)
	}
}

// TestRealPrCreateDepsWiresTheBlobStore covers the production wiring of the
// pr create seam. TestExecutePrCreateWiresTheBlobStore substitutes the seam, so
// on its own it cannot notice the production assignment disappearing.
func TestRealPrCreateDepsWiresTheBlobStore(t *testing.T) {
	deps := realPrCreateDeps()
	// Sweep every seam, not just the blob store: realPrCreateDeps exists so
	// that a production wiring silently disappearing fails a test, and that
	// promise is only worth what the sweep covers.
	seams := map[string]bool{
		"loadConfig": deps.loadConfig == nil, "getGitDir": deps.getGitDir == nil,
		"getHeadSHA": deps.getHeadSHA == nil, "runValidation": deps.runValidation == nil,
		"analyzeBranch": deps.analyzeBranch == nil, "verify": deps.verify == nil,
		"publish": deps.publish == nil, "recordEvent": deps.recordEvent == nil,
		"getGitCommonDir": deps.getGitCommonDir == nil, "recordDecision": deps.recordDecision == nil,
		"resolveActor": deps.resolveActor == nil, "writeTemplate": deps.writeTemplate == nil,
		"blobStore": deps.blobStore == nil,
	}
	for name, missing := range seams {
		if missing {
			t.Errorf("depsPrCreate.%s is nil in production: pr create would panic or silently lose that step", name)
		}
	}
	if deps.blobStore == nil {
		t.Fatal("depsPrCreate.blobStore is nil in production: pr create never reaches blob reuse")
	}
	repo := tempGitRepo(t)
	fromDeps, err := deps.blobStore(repo)
	if err != nil {
		t.Fatalf("deps.blobStore inside a real repository: %v", err)
	}
	direct, err := resolveBlobStore(repo)
	if err != nil {
		t.Fatalf("resolveBlobStore: %v", err)
	}
	if !reflect.DeepEqual(fromDeps, direct) {
		t.Errorf("deps.blobStore resolved %v while resolveBlobStore resolved %v: the production seam is not wired to resolveBlobStore", fromDeps, direct)
	}
}

// captureStreams redirects os.Stdout and os.Stderr during f and returns what
// each stream received. The restorations go in t.Cleanup: a t.Fatalf/panic
// inside f must not leave the process writing into closed pipes.
func captureStreams(t *testing.T, f func()) (stdout, stderr string) {
	t.Helper()
	originalOut, originalErr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout, os.Stderr = outW, errW
	t.Cleanup(func() { os.Stdout, os.Stderr = originalOut, originalErr })
	f()
	outW.Close()
	errW.Close()
	outBytes, err := io.ReadAll(outR)
	if err != nil {
		t.Fatalf("reading the stdout pipe: %v", err)
	}
	errBytes, err := io.ReadAll(errR)
	if err != nil {
		t.Fatalf("reading the stderr pipe: %v", err)
	}
	return string(outBytes), string(errBytes)
}

// TestBranchPrReviewOptionsProgressFollowsInjectedWriter covers the injected
// progress channel of the pr review options: the ⏳ spinner callbacks write to
// the writer the caller passes, never to a process-global snapshot taken when
// the options were built. The JSON-safe routing itself is decided at the
// injection site (RunPrReviewWith passes its payload writer normally and
// stderr in --json mode) and is pinned by TestPrReviewJSONStdoutStartsAtJSON.
func TestBranchPrReviewOptionsProgressFollowsInjectedWriter(t *testing.T) {
	wiring := pr.Wiring{
		BranchOptionsWithRefuter: func(_ config.Config, _ *modelprobe.Verifier, opts review.BranchOptions) review.BranchOptions {
			return opts
		},
		TransportFactory: func(config.Config, string) func(string, []string) review.ReviewTransport {
			return func(string, []string) review.ReviewTransport { return nil }
		},
		ShortSHA: func(sha string) string { return sha },
	}
	var progress bytes.Buffer
	options, _ := pr.BranchPrReviewOptions(config.Config{}, modelprobe.NewVerifier(nil), t.TempDir(),
		pr.FlagsPrReview{JsonOut: true}, nil, wiring, &progress)
	if options.OnCommit == nil || options.OnDimension == nil {
		t.Fatal("BranchPrReviewOptions must wire the progress callbacks")
	}
	options.OnCommit(0, 1, "abc1234abcd")
	options.OnDimension("logic")
	if got := progress.String(); !strings.Contains(got, "⏳ [1/1] Auditing abc1234abcd") || !strings.Contains(got, "  ⏳ logic …") {
		t.Errorf("injected progress writer misses the spinner lines:\n%q", got)
	}
}

// TestPrReviewJSONStdoutStartsAtJSON is the byte-0 regression of the pr
// review --json contract: the whole flow runs through RunPrReviewWith with a
// stubbed fast path (the branch analysis fires the same progress callbacks
// the real one does, then returns a fixture result; no agent ever runs), and
// the stdout the command produces must START at the JSON document — the ⏳
// progress lines ride stderr. A regression that lets one progress byte
// precede the document fails here before any automation chokes on it.
func TestPrReviewJSONStdoutStartsAtJSON(t *testing.T) {
	worktree := tempGitRepo(t)
	writeTestGateYml(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), cutoverValidationYml)
	wiring := pr.Wiring{
		NewModelVerifier:   func(string) *modelprobe.Verifier { return modelprobe.NewVerifier(nil) },
		SharedReviewLedger: func(string) (*review.Ledger, error) { return review.NewLedger(t.TempDir()), nil },
		LoadDispositions:   func(string) ([]review.FindingDisposition, error) { return nil, nil },
		TransportFactory: func(config.Config, string) func(string, []string) review.ReviewTransport {
			return func(string, []string) review.ReviewTransport { return nil }
		},
		BranchOptionsWithRefuter: func(_ config.Config, _ *modelprobe.Verifier, opts review.BranchOptions) review.BranchOptions {
			return opts
		},
		ShortSHA: func(sha string) string { return sha },
		Version:  "test",
	}
	deps := pr.DepsPrReview{
		AnalyzeBranch: func(_ *review.Ledger, opts review.BranchOptions) (*review.BranchResult, error) {
			if opts.OnCommit != nil {
				opts.OnCommit(0, 1, "abc1234abcd")
			}
			if opts.OnDimension != nil {
				opts.OnDimension("logic")
			}
			return &review.BranchResult{
				Branch:   "feat/json-contract",
				SHAs:     []string{"abc1234abcd"},
				Decision: "single",
				Records:  []review.Record{},
			}, nil
		},
		RecordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
	}
	stdout, stderr := captureStreams(t, func() {
		if code := pr.RunPrReviewWith(os.Stdout, worktree, pr.FlagsPrReview{Base: "main", JsonOut: true}, wiring, deps); code != 0 {
			t.Errorf("RunPrReviewWith exit = %d, want 0", code)
		}
	})
	if !strings.HasPrefix(stdout, "{") {
		t.Fatalf("byte 0 of --json stdout = %q, want '{'; stdout:\n%s", firstBytes(stdout), stdout)
	}
	if strings.Contains(stdout, "⏳") {
		t.Errorf("stdout received spinner bytes before the JSON document:\n%s", stdout)
	}
	if !strings.Contains(stderr, "⏳ [1/1] Auditing abc1234abcd") || !strings.Contains(stderr, "  ⏳ logic …") {
		t.Errorf("stderr misses the progress lines in --json mode:\n%s", stderr)
	}
}

// firstBytes reports the first bytes of s for failure messages, tolerating
// empty output.
func firstBytes(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
