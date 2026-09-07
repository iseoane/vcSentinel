// Production-cutover integration test for the gate durable-runs wiring
// (ticket 11 slice 3, unconditional since ticket 13 R11 removed the
// gate.durable_runs switch). It drives the EXACT cmd-level construction
// path — buildGateOptions plus applyDurableCutover over a strictly loaded
// project configuration — and proves that one gate execution produces
// root+children linkage in ONE store (the same instance backs the root run
// and every routed review invocation), that the settlement suffix enumerates
// validation jobs plus every learned review child, and that the persisted
// ParentRunID scan reproduces exactly that set.
package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

const cutoverValidationYml = "version: \"2.0\"\nvalidation:\n  capabilities:\n    lint:\n      command: \"echo ok\"\n      fails_when: \"exit_code\"\n  profiles:\n    standard: [\"lint\"]\n"

// fixedReviewAgent implements AgentReviewer AND RestrictedReviewer with a
// canned raw verdict, mirroring internal/gate's fake auditor but local to the
// cmd package.
type fixedReviewAgent struct{ output string }

func (a *fixedReviewAgent) RunPrompt(string) (string, error) { return a.output, nil }
func (a *fixedReviewAgent) RunReview(string, string, []string) (string, error) {
	return a.output, nil
}

func (a *fixedReviewAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

// cutoverRepository creates a real repository with one commit so HEAD
// resolution, change profiling, and the git common dir all resolve against a
// deterministic fixture.
func cutoverRepository(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	if err := os.WriteFile(filepath.Join(worktree, "fixture.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.name", "fixture"},
		{"config", "user.email", "fixture@example.com"},
		{"add", "."},
		{"commit", "-m", "feat: gate cutover fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return worktree
}

// buildCutoverOptions loads the strict project configuration for the
// fixture repo, resolves HEAD inputs through the real git seams, builds the
// production gate options, applies the durable cutover, and finally overrides
// ONLY the reviewer agent seams with canned verdicts (the review transport
// stays whatever production construction produced). t.Chdir pins every
// pathless git call to the fixture repository for the whole subtest.
func buildCutoverOptions(t *testing.T, worktree, stage, ymlExtra string, auditorOutput string) (gate.Options, config.Config, *reviewChildSink) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(worktree)
	writeTestGateYml(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), cutoverValidationYml+ymlExtra)

	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("config load failed: %v", err)
	}
	sha, err := git.ResolveSHA("HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	message, err := git.CommitMessage(sha)
	if err != nil {
		t.Fatalf("commit message: %v", err)
	}
	diff, err := git.DiffCommit(sha)
	if err != nil {
		t.Fatalf("commit diff: %v", err)
	}
	files, err := git.FilesOfCommit(sha)
	if err != nil {
		t.Fatalf("commit files: %v", err)
	}
	profile, err := change.ComputeCommitProfile(sha)
	if err != nil {
		t.Fatalf("change profile: %v", err)
	}

	verifier := newModelVerifier(worktree)
	options := buildGateOptions(cfg, verifier, worktree, defaultGateProfile, GateEvidence{
		SHA: sha, Message: message, Diff: diff, Profile: profile, Files: files,
	})
	sink := applyDurableCutover(&options, cfg, worktree, stage, sha, files)
	options.ReviewerFactory = func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
		return &fixedReviewAgent{output: auditorOutput}, "logic", nil
	}
	options.RefuterFactory = func() (review.AgentReviewer, string, error) {
		return &fixedReviewAgent{output: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
	}
	return options, cfg, sink
}

func TestGateCutoverLinksReviewChildrenInSharedStore(t *testing.T) {
	worktree := cutoverRepository(t)
	blockingJSON := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"confirmable risk","evidence":"risk()","confidence":"high"}]}`
	options, _, sink := buildCutoverOptions(t, worktree, "pre-push", "", blockingJSON)

	if sink == nil {
		t.Fatal("applyDurableCutover returned no sink, expected the durable wiring always on")
	}
	if options.DurableStore == nil || options.DurableReviewTransportFactory == nil {
		t.Fatalf("cutover left the durable seams unwired: store=%v factory=%v",
			options.DurableStore != nil, options.DurableReviewTransportFactory != nil)
	}

	result := gate.RunGate(options)
	if result.State != gate.StateCodeReviewFailed {
		t.Fatalf("state = %q (%v), expected %q", result.State, result.Messages, gate.StateCodeReviewFailed)
	}
	if len(sink.learned()) == 0 {
		t.Fatal("the production sink learned no review child identity")
	}

	st := options.DurableStore
	ids, err := st.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs() error = %v", err)
	}
	rootID := ""
	children := map[string]string{}
	for _, id := range ids {
		request, err := st.ReadExecutionRequest(id)
		if err != nil {
			t.Fatalf("ReadExecutionRequest(%s) error = %v", id, err)
		}
		if request.ParentRunID == "" {
			if rootID != "" {
				t.Fatalf("store holds multiple parentless runs (%s, %s)", rootID, id)
			}
			rootID = id
			continue
		}
		children[id] = request.ParentRunID
	}
	if rootID == "" {
		t.Fatal("store holds no parentless root run")
	}
	scanned := make([]string, 0, len(children))
	for id, parent := range children {
		if parent != rootID {
			t.Fatalf("run %s links to foreign parent %s", id, parent)
		}
		scanned = append(scanned, id)
	}

	// The learned review identities must be part of the persisted scan, and
	// together with the single validation job they must account for EVERY
	// scanned child.
	learned := sink.learned()
	if len(learned) < 1 {
		t.Fatal("sink drained empty after the audit")
	}
	if len(scanned) != 1+len(learned) {
		t.Fatalf("scanned children = %d (%v), want %d (1 validation job + %d review runs)",
			len(scanned), scanned, 1+len(learned), len(learned))
	}
	for _, id := range learned {
		if !slices.Contains(scanned, string(id)) {
			t.Fatalf("learned review run %s missing from the ParentRunID scan %v", id, scanned)
		}
	}

	// Root reconstruction from store contents alone: failed naming the review
	// layer, with a machine-parseable suffix enumerating exactly the scanned
	// set.
	inspection, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(rootID))
	if err != nil {
		t.Fatalf("root inspection failed: %v", err)
	}
	if inspection.Projection.State != agentrun.StateFailed {
		t.Fatalf("root state = %q, expected failed", inspection.Projection.State)
	}
	var detail string
	for _, outcome := range inspection.Outcomes {
		if strings.HasPrefix(outcome.Error, "gate: failing layer:") {
			detail = outcome.Error
		}
	}
	if !strings.Contains(detail, "gate: failing layer: review") {
		t.Fatalf("root detail %q does not name the review layer", detail)
	}
	marker := "|children="
	index := strings.Index(detail, marker)
	if index < 0 {
		t.Fatalf("root detail %q carries no children enumeration", detail)
	}
	enumerated := strings.Split(detail[index+len(marker):], ",")
	slices.Sort(enumerated)
	slices.Sort(scanned)
	if !slices.Equal(enumerated, scanned) {
		t.Fatalf("enumerated children %v != ParentRunID scan %v", enumerated, scanned)
	}

	// Sensible classification: only the ROOT carries the failure; every child
	// (validation job included) reached a terminal success attempt.
	for _, id := range scanned {
		childInspection, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(id))
		if err != nil {
			t.Fatalf("child %s inspection failed: %v", id, err)
		}
		if len(childInspection.Outcomes) != 1 {
			t.Fatalf("child %s recorded %d outcomes, expected exactly one terminal attempt", id, len(childInspection.Outcomes))
		}
		if childInspection.Outcomes[0].Class != agentrun.OutcomeSuccess {
			t.Fatalf("child %s settled class=%s, expected success (only the root names the failing layer)", id, childInspection.Outcomes[0].Class)
		}
	}
}

// TestGateStrictConfigRejectsRemovedDurableRunsKey proves at cmd level that a
// project yaml still carrying gate.durable_runs (removed in ticket 13 R11)
// fails fast as configuration infrastructure instead of being ignored.
func TestGateStrictConfigRejectsRemovedDurableRunsKey(t *testing.T) {
	worktree := cutoverRepository(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(worktree)
	writeTestGateYml(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		cutoverValidationYml+"gate:\n  durable_runs: false\n")

	var output bytes.Buffer
	exitCode := runGate(&output, worktree, []string{"--stage", "pre-push"})

	if exitCode != 4 {
		t.Fatalf("exit = %d (%q), want 4 for a strict configuration failure", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "gate") || !strings.Contains(output.String(), "not found") {
		t.Fatalf("output = %q, want the unknown-key error to name the removed gate section", output.String())
	}
}
