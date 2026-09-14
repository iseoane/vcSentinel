package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
)

const validPrReviewSHA = "abc1234abcd00000000000000000000000000000"

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
	if flags.base != "dev" || !flags.overview || !flags.jsonOut {
		t.Errorf("flags = %+v, expected base=dev overview json", flags)
	}

	if _, err := parsePrReviewFlags([]string{"--audit-pending"}); err == nil {
		t.Fatal("--audit-pending must be rejected: it was retired")
	} else if !strings.Contains(err.Error(), "sentinel review <sha>") {
		t.Errorf("--audit-pending error must name the per-commit replacement: %v", err)
	}

	// --only-unaudited is retired (docs/issues/actionable.md item 2): it now
	// describes the default (nothing is audited unless --audit-pending is
	// given), so keeping it as a silent no-op would mislead a caller into
	// believing it still restricts scope. It fails closed with a migration
	// hint instead, the same clean-break precedent as the removed
	// 'sentinel pr [gh arguments]' passthrough.
	if _, err := parsePrReviewFlags([]string{"--only-unaudited"}); err == nil {
		t.Error("--only-unaudited must be rejected: it was retired")
	} else if !strings.Contains(err.Error(), "sentinel review <sha>") {
		t.Errorf("--only-unaudited error should point at sentinel review: %v", err)
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
		Branch:    "feature/x",
		SHAs:      []string{"a1b2c3d4e5f6"},
		Pending:   []string{"a1b2c3d4e5f6"},
		Unaudited: []review.UnauditedCommit{{SHA: "a1b2c3d4e5f6", Subject: "feat: something"}},
		Records:   []review.Record{{SHA: "a1b2c3d4e5f6", Message: "feat: something"}},
		Volume:    420,
		Decision:  "chain",
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
		"unaudited": float64(1),
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
	if options.NetReview == nil {
		t.Fatal("options.NetReview = nil, want the requested net review input")
	}
	if len(options.Dispositions) != 1 {
		t.Fatalf("options.Dispositions = %+v, want the standing answers", options.Dispositions)
	}
	d := options.Dispositions[0]
	if d.Fingerprint != "fp-carry" || d.Status != review.StatusRefuted || d.Actor != review.RefutationActorHuman {
		t.Fatalf("carried disposition = %+v, want the recorded answer identity, not a count", d)
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

// A nil net review (no net audit requested) still receives the answers on the
// branch options and still reports a loader failure: the helper never invents
// a net input and never hides a corrupt log.
func TestApplyPrReviewDispositionsWithoutNet(t *testing.T) {
	answered := []review.FindingDisposition{{
		SHA: "abc1234", Fingerprint: "fp-no-net", Status: review.StatusRefuted,
		Reason: "verified safe", Actor: review.RefutationActorHuman, Source: review.DispositionSourceHuman,
	}}
	options, err := applyPrReviewDispositions(review.BranchOptions{}, "worktree",
		func(string) ([]review.FindingDisposition, error) { return answered, nil })
	if err != nil || options.NetReview != nil {
		t.Fatalf("options = %+v, err = %v; want no invented net input", options, err)
	}
	if len(options.Dispositions) != 1 || options.Dispositions[0].Fingerprint != "fp-no-net" {
		t.Fatalf("options.Dispositions = %+v, want the answers the branch surfaces decide with without a net review", options.Dispositions)
	}
	if _, err := applyPrReviewDispositions(review.BranchOptions{}, "worktree",
		func(string) ([]review.FindingDisposition, error) { return nil, errors.New("corrupt dispositions") }); err == nil {
		t.Fatal("corrupt log was swallowed without a net review")
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
			return ops.VerificationResult{}, errors.New("tool failed\r\n## forged `code`")
		})
	if template.Mode != ops.ModeSkipped {
		t.Errorf("with an error the mode must be omitted, got %q", template.Mode)
	}
	if !strings.Contains(template.Reason, "verification_error") ||
		!strings.Contains(template.Reason, "tool failed") {
		t.Errorf("the reason must reflect the real error, got %q", template.Reason)
	}

	body := review.RenderPRTemplate(nil, nil, template, "test", nil)
	if strings.Contains(body, "\r") || strings.Contains(body, "\n## forged") || strings.Contains(body, "`code`") {
		t.Fatalf("production verification-error rendering retained hostile Markdown/control text:\n%s", body)
	}
	if !strings.Contains(body, "tool failed ## forged 'code'") {
		t.Fatalf("production verification-error rendering lost the sanitized reason:\n%s", body)
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
	url, fallback, err := publishPRStoredWith("worktree", "Stored review title", path, "", publishPROptions{
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
	_, fallback, err := publishPRStoredWith("worktree", "Stored review title", path, "", publishPROptions{
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
// with explicit title/body-file arguments and the worktree as cwd; the URL
// comes from gh's output, no clipboard. The title is supplied by the persisted
// review rather than derived with --fill-first.
func TestPublishPRWithUsesGhWithExactArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seenWorktree string
	var seenArgs []string
	url, fallback, err := publishPRStoredWith("the-worktree", "Stored review title", path, "", publishPROptions{
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
	expected := []string{"pr", "create", "--draft", "--title", "Stored review title", "--body-file", path}
	if !reflect.DeepEqual(seenArgs, expected) {
		t.Errorf("gh arguments = %v, expected %v", seenArgs, expected)
	}
}

// TestPublishPRWithGhFailurePropagates: a failed gh publication still uses the
// clipboard fallback so a pushed branch is not stranded without its body, and
// still reports the gh diagnostic because no PR was created.
func TestPublishPRWithGhFailurePropagates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.md")
	if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	var copied string
	_, fallback, err := publishPRStoredWith("the-worktree", "Stored review title", path, "", publishPROptions{
		ghAvailable: func(string) bool { return true },
		runGh:       func(string, ...string) ([]byte, error) { return nil, errors.New("gh: repo not configured") },
		copy:        func(body string) error { copied = body; return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "gh: repo not configured") {
		t.Fatalf("gh failure must surface its diagnostic, got %v", err)
	}
	if !fallback || copied != "body" {
		t.Fatalf("fallback = (%v, %q), want true and the body", fallback, copied)
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
	_, _, err := publishPRStoredWith("the-worktree", "Stored review title", path, "develop", publishPROptions{
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
	expected := []string{"pr", "create", "--draft", "--title", "Stored review title", "--base", "develop", "--body-file", path}
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
	_, _, err := publishPRStoredWith("the-worktree", "Stored review title", path, "", publishPROptions{
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
	expected := []string{"pr", "create", "--draft", "--title", "Stored review title", "--body-file", path}
	if !reflect.DeepEqual(seenArgs, expected) {
		t.Errorf("without --base the gh arguments = %v, expected %v", seenArgs, expected)
	}
}

func TestDetailPrCreateEvent(t *testing.T) {
	detail, err := detailPrCreateEvent("https://github.com/x/pr/1", false, false, false, "", 0)
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
	detail, err := detailPrCreateEvent("", true, true, false, "", 0)
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

// TestDetailPrCreateEventRecordsUnauditedCount covers docs/issues/actionable.md
// item 5 for the event stream: an operator reconstructing what happened from
// events must be able to tell an audited pass from a skipped one, not just
// the JSON --json report.
func TestDetailPrCreateEventRecordsUnauditedCount(t *testing.T) {
	detail, err := detailPrCreateEvent("https://github.com/x/pr/3", false, false, false, "", 2)
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
	if raw["unaudited"] != float64(2) {
		t.Errorf("unaudited = %v, want 2", raw["unaudited"])
	}
}

// TestDetailPrCreateEventForceWithReason: the --force exception is recorded in
// the event with the explicit reason (T1.8). The "motivo" key is the wire key
// internal/app/pr emits.
func TestDetailPrCreateEventForceWithReason(t *testing.T) {
	detail, err := detailPrCreateEvent("https://github.com/x/pr/2", false, false, true, "real reason", 0)
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

	if _, err := parsePrCreateFlags([]string{"--audit-pending"}); err == nil || !strings.Contains(err.Error(), "sentinel review <sha>") {
		t.Fatalf("--audit-pending must be explicitly retired: %v", err)
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

// TestBranchPrReviewOptionsDefaultsToNotAuditingPending covers
// docs/issues/actionable.md item 2: pr review must NOT audit commits without
// a review record by default any more — only --audit-pending restores that.
// The net audit is unconditional and unaffected either way.
func TestBranchPrReviewOptionsDefaultsToNotAuditingPending(t *testing.T) {
	wiring := pr.Wiring{
		BranchOptionsWithRefuter: func(_ config.Config, _ *modelprobe.Verifier, opts review.BranchOptions) review.BranchOptions {
			return opts
		},
		TransportFactory: func(config.Config, string) func(string, []string) review.ReviewTransport {
			return func(string, []string) review.ReviewTransport { return nil }
		},
		ShortSHA: func(sha string) string { return sha },
	}
	options, _ := pr.BranchPrReviewOptions(config.Config{}, modelprobe.NewVerifier(nil), t.TempDir(),
		pr.FlagsPrReview{}, nil, wiring, &bytes.Buffer{})
	if !options.OnlyPending {
		t.Errorf("default pr review must not audit pending commits: OnlyPending = %v, want true", options.OnlyPending)
	}

}

// TestBranchPrReviewOptionsProgressFollowsInjectedWriter covers the injected
// progress channel of the pr review options: the ⏳ spinner callbacks write to
// the writer the caller passes, never to a process-global snapshot taken when
// the options were built. The JSON-safe routing itself is decided at the
// injection site (the cmd caller passes its payload writer normally and
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
				SHAs:     []string{validPrReviewSHA},
				Decision: "single",
				Records:  []review.Record{},
			}, nil
		},
		CommitMessage: func(string) (string, error) { return "feat: test review", nil },
		RecordEvent:   func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		EventDetail:   pr.PrReviewEventDetail,
	}
	stdout, stderr := captureStreams(t, func() {
		if code := pr.RunPrReviewWith(os.Stdout, os.Stderr, worktree, pr.FlagsPrReview{Base: "main", JsonOut: true}, wiring, deps); code != 0 {
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

// TestPrReviewJSONRoutesWarningsOffStdout extends the byte-0 contract to the
// human warnings of the flow: in --json mode both the blob-store warning
// (storeWarning) and the event-record warning are motion, not payload, and
// must ride the same progress channel as the spinners (stderr), so the JSON
// document still opens stdout.
//
// The harness splits the two git probes the way a foreign worktree with an
// ambient GIT_DIR does: GetGitDirFrom honors the ambient GIT_DIR while the
// sanitized common-dir probe inside ResolveBlobStore fails, which is exactly
// the storeWarning path; the failing RecordEvent seam is the event-record
// warning path.
func TestPrReviewJSONRoutesWarningsOffStdout(t *testing.T) {
	repo := tempGitRepo(t)
	ambient, err := exec.Command("git", "-C", repo, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		t.Fatalf("rev-parse --absolute-git-dir: %v", err)
	}
	t.Setenv("GIT_DIR", strings.TrimSpace(string(ambient)))
	worktree := t.TempDir()
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
		AnalyzeBranch: func(_ *review.Ledger, _ review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{
				Branch:   "feat/warning-routing",
				SHAs:     []string{validPrReviewSHA},
				Decision: "single",
				Records:  []review.Record{},
			}, nil
		},
		CommitMessage: func(string) (string, error) { return "feat: test review", nil },
		WriteEvidence: func(string, string, []review.EvidenceLog) ([]string, error) {
			return nil, nil
		},
		SavePRReview: func(string, *store.PRReviewEntry) error { return nil },
		RecordEvent: func(string, string, int, []string, ops.EventDetail, string) error {
			return errors.New("event store locked")
		},
		EventDetail: pr.PrReviewEventDetail,
	}
	stdout, stderr := captureStreams(t, func() {
		if code := pr.RunPrReviewWith(os.Stdout, os.Stderr, worktree, pr.FlagsPrReview{Base: "main", JsonOut: true}, wiring, deps); code != 0 {
			t.Errorf("RunPrReviewWith exit = %d, want 0", code)
		}
	})
	if !strings.HasPrefix(stdout, "{") {
		t.Fatalf("byte 0 of --json stdout = %q, want '{'; stdout:\n%s", firstBytes(stdout), stdout)
	}
	if strings.Contains(stdout, "⚠️") || strings.Contains(stdout, "? Warning") {
		t.Errorf("stdout received warning bytes that precede the JSON document:\n%s", stdout)
	}
	if !strings.Contains(stderr, "⚠️  Warning: could not resolve the git-common-dir") {
		t.Errorf("stderr misses the blob-store warning:\n%s", stderr)
	}
	if !strings.Contains(stderr, "? Warning: could not record the event: event store locked") {
		t.Errorf("stderr misses the event-record warning:\n%s", stderr)
	}
}

// TestPrReviewJSONRoutesEventDetailWarningOffStdout completes the JSON-safe
// routing contract: the "could not build the event detail" warning is human
// motion too, so in --json mode it must ride the injected progress channel
// (stderr) instead of polluting the stdout the JSON document opens.
// PrReviewEventDetail never fails on a real branch result, so the failing
// EventDetail dep seam is the deterministic way to drive the branch — no real
// git state or agent is involved.
func TestPrReviewJSONRoutesEventDetailWarningOffStdout(t *testing.T) {
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
		AnalyzeBranch: func(_ *review.Ledger, _ review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{
				Branch:   "feat/event-detail-routing",
				SHAs:     []string{validPrReviewSHA},
				Decision: "single",
				Records:  []review.Record{},
			}, nil
		},
		CommitMessage: func(string) (string, error) { return "feat: test review", nil },
		RecordEvent:   func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		EventDetail: func(string, *review.BranchResult, bool) (ops.EventDetail, error) {
			return nil, errors.New("detail schema rejected")
		},
	}
	stdout, stderr := captureStreams(t, func() {
		if code := pr.RunPrReviewWith(os.Stdout, os.Stderr, worktree, pr.FlagsPrReview{Base: "main", JsonOut: true}, wiring, deps); code != 0 {
			t.Errorf("RunPrReviewWith exit = %d, want 0", code)
		}
	})
	if !strings.HasPrefix(stdout, "{") {
		t.Fatalf("byte 0 of --json stdout = %q, want '{'; stdout:\n%s", firstBytes(stdout), stdout)
	}
	if strings.Contains(stdout, "? Warning: could not build the event detail") {
		t.Errorf("stdout received the event-detail warning:\n%s", stdout)
	}
	if !strings.Contains(stderr, "? Warning: could not build the event detail: detail schema rejected") {
		t.Errorf("stderr misses the event-detail warning:\n%s", stderr)
	}
}

// TestPrReviewNonJSONRoutesProgressThroughPayloadWriter pins the non-JSON half
// of the routing contract that now lives at the cmd call site: outside --json
// the caller passes the payload writer itself as progress (progress == w), so
// the ⏳ spinner lines and the human warnings share the stream the report is
// printed on and stderr stays silent. The failing RecordEvent seam fires the
// warning deterministically, with no real agent in the loop.
func TestPrReviewNonJSONRoutesProgressThroughPayloadWriter(t *testing.T) {
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
				Branch:   "feat/non-json-routing",
				SHAs:     []string{validPrReviewSHA},
				Decision: "single",
				Records:  []review.Record{},
			}, nil
		},
		CommitMessage: func(string) (string, error) { return "feat: test review", nil },
		RecordEvent: func(string, string, int, []string, ops.EventDetail, string) error {
			return errors.New("event store locked")
		},
		EventDetail: pr.PrReviewEventDetail,
	}
	stdout, stderr := captureStreams(t, func() {
		// The cmd wiring outside --json: the payload writer carries the
		// human motion too (progress == w).
		progress := io.Writer(os.Stdout)
		if code := pr.RunPrReviewWith(os.Stdout, progress, worktree, pr.FlagsPrReview{Base: "main"}, wiring, deps); code != 0 {
			t.Errorf("RunPrReviewWith exit = %d, want 0", code)
		}
	})
	if !strings.Contains(stdout, "⏳ [1/1] Auditing abc1234abcd") {
		t.Errorf("non-JSON stdout misses the spinner progress:\n%s", stdout)
	}
	if !strings.Contains(stdout, "? Warning: could not record the event: event store locked") {
		t.Errorf("non-JSON stdout misses the event-record warning:\n%s", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr must stay silent outside --json, got:\n%s", stderr)
	}
}

// TestRunPrReviewReportsUnauditedCommitsWithoutBlocking covers
// docs/issues/actionable.md item 2 at the terminal-report boundary: when a
// branch has commits with no review record, the report must say so, name
// them, and point at 'sentinel review <sha>' — but it must never block, and
// the single/chain decision line must still render even with zero Records
// (the default now that pr review does not audit pending commits).
func TestRunPrReviewReportsUnauditedCommitsWithoutBlocking(t *testing.T) {
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
			return &review.BranchResult{
				Branch:    "feat/unaudited",
				SHAs:      []string{validPrReviewSHA},
				Decision:  "single",
				Records:   nil,
				Unaudited: []review.UnauditedCommit{{SHA: validPrReviewSHA, Subject: "feat(x): x"}},
			}, nil
		},
		CommitMessage: func(string) (string, error) { return "feat: test review", nil },
		RecordEvent:   func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		EventDetail:   pr.PrReviewEventDetail,
	}
	stdout, _ := captureStreams(t, func() {
		if code := pr.RunPrReviewWith(os.Stdout, os.Stdout, worktree, pr.FlagsPrReview{Base: "main"}, wiring, deps); code != 0 {
			t.Errorf("RunPrReviewWith exit = %d, want 0 (reporting must never block)", code)
		}
	})
	for _, want := range []string{"no review record", "abc1234a", "feat(x): x", "sentinel review abc1234abcd", "PR decision"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunPrReviewSuppliesTrailerIntentsToNetReview(t *testing.T) {
	worktree := tempGitRepo(t)
	writeTestGateYml(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), cutoverValidationYml)
	sha := strings.Repeat("a", 64)
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
	}
	var saved *store.PRReviewEntry
	deps := pr.DepsPrReview{
		AnalyzeBranch: func(_ *review.Ledger, opts review.BranchOptions) (*review.BranchResult, error) {
			if opts.NetReview == nil || opts.PrepareNetReview == nil {
				t.Fatal("pr review must prepare net review intentions")
			}
			if err := opts.PrepareNetReview([]string{sha}); err != nil {
				t.Fatalf("PrepareNetReview() error = %v", err)
			}
			if got, want := opts.NetReview.Intention, "Protect the release pipeline."; got != want {
				t.Fatalf("net intention = %q, want %q", got, want)
			}
			return &review.BranchResult{Branch: "feat/trailers", SHAs: []string{sha}, Decision: "single"}, nil
		},
		RecordEvent: func(string, string, int, []string, ops.EventDetail, string) error { return nil },
		EventDetail: pr.PrReviewEventDetail,
		CommitMessage: func(got string) (string, error) {
			if got != sha {
				t.Fatalf("title SHA = %s, want %s", got, sha)
			}
			return "feat: persist the review title", nil
		},
		ReadIntents: func(got []string) ([]review.IntentLine, error) {
			if !reflect.DeepEqual(got, []string{sha}) {
				t.Fatalf("trailer range = %v, want [%s]", got, sha)
			}
			return []review.IntentLine{{SHA: sha, Text: "Protect the release pipeline.", Source: "declared"}}, nil
		},
		WriteEvidence: func(string, string, []review.EvidenceLog) ([]string, error) {
			return []string{"evidence/pr-review.md"}, nil
		},
		SavePRReview: func(_ string, entry *store.PRReviewEntry) error {
			saved = entry
			return nil
		},
	}
	var output bytes.Buffer
	if code := pr.RunPrReviewWith(&output, &output, worktree, pr.FlagsPrReview{Base: "main"}, wiring, deps); code != 0 {
		t.Fatalf("RunPrReviewWith() = %d, output:\n%s", code, output.String())
	}
	if saved == nil || saved.HeadSHA != sha || !strings.Contains(saved.Body, "Protect the release pipeline.") {
		t.Fatalf("saved review entry omits the SHA-256 head or trailer intent: %+v", saved)
	}
	var persisted review.Attestation
	if err := json.Unmarshal(saved.Attestation, &persisted); err != nil {
		t.Fatalf("persisted attestation is not valid JSON: %v", err)
	}
	bodyAttestation, err := review.ParseAttestation(saved.Body)
	if err != nil {
		t.Fatalf("body attestation is not parseable: %v", err)
	}
	if !reflect.DeepEqual(persisted, bodyAttestation) {
		t.Fatalf("persisted and body attestations differ:\npersisted: %+v\nbody: %+v", persisted, bodyAttestation)
	}
	wantSteps := []review.AttestationStep{
		{Step: "slice", Status: "passed"},
		{Step: "review", Status: "not_observed"},
		{Step: "gate", Status: "not_run"},
		{Step: "lint", Status: "not_configured"},
		{Step: "test", Status: "not_configured"},
		{Step: "build", Status: "not_configured"},
		{Step: "pr review", Status: "authored"},
		{Step: "ci", Status: "not_observed"},
	}
	if !reflect.DeepEqual(persisted.Steps, wantSteps) {
		t.Fatalf("persisted attestation steps = %+v, want %+v", persisted.Steps, wantSteps)
	}
	seenSteps := make(map[string]bool, len(persisted.Steps))
	for _, step := range persisted.Steps {
		if seenSteps[step.Step] {
			t.Fatalf("persisted attestation contains duplicate step %q", step.Step)
		}
		seenSteps[step.Step] = true
	}
	if saved.At.IsZero() || saved.At.Location() != time.UTC {
		t.Fatalf("saved review timestamp = %v, want a non-zero UTC timestamp", saved.At)
	}
	if got, want := saved.Title, "feat: persist the review title"; got != want {
		t.Fatalf("saved review title = %q, want %q", got, want)
	}
	deps.ReadIntents = func([]string) ([]review.IntentLine, error) { return nil, nil }
	deps.AnalyzeBranch = func(_ *review.Ledger, opts review.BranchOptions) (*review.BranchResult, error) {
		if err := opts.PrepareNetReview([]string{sha}); err != nil {
			t.Fatalf("PrepareNetReview() error = %v", err)
		}
		if got, want := opts.NetReview.Intention, "No intent recorded for this PR range."; got != want {
			t.Fatalf("net intention = %q, want %q", got, want)
		}
		return &review.BranchResult{Branch: "feat/no-trailers", SHAs: []string{sha}, Decision: "single"}, nil
	}
	saved = nil
	output.Reset()
	if code := pr.RunPrReviewWith(&output, &output, worktree, pr.FlagsPrReview{Base: "main"}, wiring, deps); code != 0 {
		t.Fatalf("RunPrReviewWith() without trailers = %d, output:\n%s", code, output.String())
	}
	if saved == nil || !strings.Contains(saved.Body, "_No intent recorded.") {
		t.Fatalf("saved review body must report the missing trailer intent: %+v", saved)
	}
	deps.CommitMessage = func(string) (string, error) { return "", errors.New("first commit unavailable") }
	saved = nil
	output.Reset()
	if code := pr.RunPrReviewWith(&output, &output, worktree, pr.FlagsPrReview{Base: "main"}, wiring, deps); code != 1 {
		t.Fatalf("RunPrReviewWith() with missing title = %d, want 1", code)
	}
	if saved != nil {
		t.Fatalf("saved review despite title lookup failure: %+v", saved)
	}
}

func TestRunPrReviewRejectsMalformedHeadBeforeSideEffects(t *testing.T) {
	worktree := tempGitRepo(t)
	writeTestGateYml(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), cutoverValidationYml)

	calls := make(map[string]int)
	mark := func(name string) { calls[name]++ }
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
	}
	deps := pr.DepsPrReview{
		AnalyzeBranch: func(_ *review.Ledger, _ review.BranchOptions) (*review.BranchResult, error) {
			return &review.BranchResult{Branch: "feature/malformed-head", SHAs: []string{"not-a-git-object-id"}}, nil
		},
		CommitMessage: func(string) (string, error) {
			mark("title")
			return "feat: should not be looked up", nil
		},
		WriteEvidence: func(string, string, []review.EvidenceLog) ([]string, error) {
			mark("evidence")
			return []string{"evidence/pr-review.md"}, nil
		},
		SavePRReview: func(string, *store.PRReviewEntry) error {
			mark("persistence")
			return nil
		},
		EventDetail: func(string, *review.BranchResult, bool) (ops.EventDetail, error) {
			mark("event detail")
			return nil, nil
		},
		RecordEvent: func(string, string, int, []string, ops.EventDetail, string) error {
			mark("event")
			return nil
		},
	}

	var output bytes.Buffer
	if code := pr.RunPrReviewWith(&output, &output, worktree, pr.FlagsPrReview{Base: "main"}, wiring, deps); code != 1 {
		t.Fatalf("RunPrReviewWith() = %d, want 1; output:\n%s", code, output.String())
	}
	if !strings.Contains(output.String(), "invalid branch head") {
		t.Fatalf("output does not explain the malformed head:\n%s", output.String())
	}
	for _, name := range []string{"title", "evidence", "persistence", "event detail", "event"} {
		if calls[name] != 0 {
			t.Errorf("%s calls = %d, want 0", name, calls[name])
		}
	}
}

func TestRunPrReviewAcceptsSHA1AndSHA256Heads(t *testing.T) {
	for _, sha := range []string{strings.Repeat("a", 40), strings.Repeat("b", 64)} {
		t.Run(fmt.Sprintf("sha-%d", len(sha)), func(t *testing.T) {
			worktree := tempGitRepo(t)
			writeTestGateYml(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), cutoverValidationYml)

			calls := make(map[string]int)
			mark := func(name string) { calls[name]++ }
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
				ShortSHA: func(value string) string { return value },
			}
			deps := pr.DepsPrReview{
				AnalyzeBranch: func(_ *review.Ledger, _ review.BranchOptions) (*review.BranchResult, error) {
					return &review.BranchResult{Branch: "feature/valid-head", SHAs: []string{sha}}, nil
				},
				CommitMessage: func(value string) (string, error) {
					if value != sha {
						t.Fatalf("title SHA = %s, want %s", value, sha)
					}
					mark("title")
					return "feat: persist valid head", nil
				},
				WriteEvidence: func(string, string, []review.EvidenceLog) ([]string, error) {
					mark("evidence")
					return []string{"evidence/pr-review.md"}, nil
				},
				SavePRReview: func(string, *store.PRReviewEntry) error {
					mark("persistence")
					return nil
				},
				EventDetail: func(string, *review.BranchResult, bool) (ops.EventDetail, error) {
					mark("event detail")
					return nil, nil
				},
				RecordEvent: func(string, string, int, []string, ops.EventDetail, string) error {
					mark("event")
					return nil
				},
			}

			var output bytes.Buffer
			if code := pr.RunPrReviewWith(&output, &output, worktree, pr.FlagsPrReview{Base: "main"}, wiring, deps); code != 0 {
				t.Fatalf("RunPrReviewWith() = %d, want 0; output:\n%s", code, output.String())
			}
			for _, name := range []string{"title", "evidence", "persistence", "event detail", "event"} {
				if calls[name] != 1 {
					t.Errorf("%s calls = %d, want 1", name, calls[name])
				}
			}
		})
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
