package durableruns_e2e

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
)

// Scenario d — retry. A failed run is retried through the Recover/Retry
// relaunch path by a FRESH controller (a new operator process): the resumed
// attempt carries a fresh invocation identity while the failed attempt keeps
// its durable outcome record.

func TestRetryRelaunchesFailedRunUnderFreshInvocationIdentity(t *testing.T) {
	backing := newE2EStore(t)
	failing := execution.NewControllerWithClock(backing, &failingAdapter{detail: "provider rejected the prompt"}, fixedClock())

	first, err := failing.Start(context.Background(), e2eRequest("retry-failed"), e2ePolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := first.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateFailed {
		t.Fatalf("first attempt = %+v, %v; want failed", completion, err)
	}
	runID := first.RunID
	originalInvocation := completion.InvocationID

	// A brand-new controller over the same store performs the operator's
	// retry decision with its own serving adapter, exactly as
	// `sentinel runs retry` does after a restart.
	successController := execution.NewControllerWithClock(backing, successOutputAdapter("resumed output"), fixedClock())
	handle, err := successController.Retry(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("Retry(failed run): %v", err)
	}
	if handle.InvocationID == originalInvocation {
		t.Fatalf("retried invocation %s reuses the failed attempt's identity", handle.InvocationID)
	}
	retryCompletion, err := handle.Wait(context.Background())
	if err != nil || retryCompletion.State != agentrun.StateSucceeded {
		t.Fatalf("retry completion = %+v, %v; want succeeded", retryCompletion, err)
	}

	inspection := assertRunsInspection(t, backing, successController, string(runID),
		agentrun.StateSucceeded, agentrun.TerminalSuccess)

	var failureOutcome, successOutcome bool
	for _, outcome := range inspection.Outcomes {
		switch {
		case outcome.InvocationID == string(originalInvocation) &&
			outcome.Class == agentrun.OutcomeFailure &&
			outcome.Error == "provider rejected the prompt":
			failureOutcome = true
		case outcome.InvocationID == string(handle.InvocationID) &&
			outcome.Class == agentrun.OutcomeSuccess:
			successOutcome = true
		}
	}
	if !failureOutcome || !successOutcome {
		t.Fatalf("outcomes = %+v, want the preserved failure record AND the fresh success", inspection.Outcomes)
	}
	if verification, err := successController.Verify(context.Background(), runID); err != nil || !verification.Valid {
		t.Fatalf("post-retry verification = %+v, %v; want a valid hash chain across both attempts", verification, err)
	}
}

// Scenario e — respond. An awaiting-decision run is answered through the host
// envelope with an authenticated principal and settles with response evidence
// bound to the child invocation.

func TestRespondSettlesAwaitingDecisionRunWithResponseEvidence(t *testing.T) {
	backing := newE2EStore(t)
	adapter := &awaitingAdapter{}
	controller := execution.NewControllerWithClock(backing, adapter, fixedClock())
	host := execution.NewInProcessHost(controller)

	handle, err := host.Start(context.Background(), execution.StartRequest{
		Request:     e2eRequest("respond-clarification"),
		Policy:      e2ePolicy(),
		AuthContext: execution.AuthContext{Principal: "reviewer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootInvocation := handle.InvocationID
	waitForProjection(t, controller, handle.RunID, agentrun.StateAwaitingDecision)

	const answer = "focus the review on the error handling path"
	result, err := host.Apply(context.Background(), execution.ApplyRequest{
		RunID:       handle.RunID,
		Action:      execution.ControlAction{Kind: execution.ActionRespond, Response: answer},
		ActionID:    "respond-1",
		AuthContext: execution.AuthContext{Principal: "reviewer"},
	})
	if err != nil || !result.Accepted {
		t.Fatalf("host Apply(respond) = %+v, %v; want accepted", result, err)
	}
	if result.InvocationID == rootInvocation {
		t.Fatalf("response invocation %s reuses the waiting invocation identity", result.InvocationID)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("completion = %+v, %v; want succeeded after the response round", completion, err)
	}

	inspection := assertRunsInspection(t, backing, controller, string(handle.RunID),
		agentrun.StateSucceeded, agentrun.TerminalSuccess)

	if len(inspection.Responses) != 1 {
		t.Fatalf("responses = %+v, want exactly one recorded response", inspection.Responses)
	}
	response := inspection.Responses[0]
	if response.InvocationID != string(result.InvocationID) {
		t.Fatalf("response bound to %s, want the child invocation %s", response.InvocationID, result.InvocationID)
	}
	if response.ResponseHash != execution.HashAdapterOutput(answer) {
		t.Fatalf("response hash = %q, want the admitted hash of the principal's answer", response.ResponseHash)
	}
	if len(adapter.calls) != 2 || adapter.calls[0] != "" || adapter.calls[1] != answer {
		t.Fatalf("adapter calls = %q, want [empty, the exact answer]", adapter.calls)
	}
	if len(inspection.Outcomes) != 2 {
		t.Fatalf("outcomes = %+v, want exactly two ordered outcomes", inspection.Outcomes)
	}
	initial := inspection.Outcomes[0]
	if initial.Class != agentrun.OutcomeAwaitingDecision ||
		initial.InvocationID != string(rootInvocation) {
		t.Fatalf("initial outcome = %+v, want awaiting_decision authored by the parent invocation", initial)
	}
	if initial.Observation == nil || initial.DurationNanos == nil ||
		*initial.DurationNanos < 0 ||
		initial.Observation.DurationNanos == nil ||
		*initial.Observation.DurationNanos < 0 {
		t.Fatalf("initial outcome = %+v, want observation with non-negative duration", initial)
	}
	final := inspection.Outcomes[1]
	if final.Class != agentrun.OutcomeSuccess ||
		final.InvocationID != string(result.InvocationID) {
		t.Fatalf("final outcome = %+v, want success authored by the response child", final)
	}
}

// Scenario f — fallback. The production CadenaAdaptador is built from real
// configuration over fake provider binaries: the primary fails every call,
// the secondary serves, the run succeeds with its effective agent recorded.

func TestFallbackChainPrimaryFailsSecondaryServesWithEffectiveAgent(t *testing.T) {
	requireNonWindows(t)

	binDir := t.TempDir()
	writeFakeAgent(t, binDir, "vas-e2e-primary-agent", "exit 7\n")
	writeFakeAgent(t, binDir, "vas-e2e-secondary-agent", "echo 'secondary review output'\n")

	home := t.TempDir()
	t.Setenv("MY_SUB_AGENT", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	worktree := t.TempDir()
	configDir := filepath.Join(worktree, ".vas_sentinel")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configYAML := "version: \"2\"\n" +
		"active_agent: auto\n" +
		"agents:\n" +
		"  vas-e2e-primary-agent:\n" +
		"    model: fake-primary-model\n" +
		"    reasoning_effort: low\n" +
		"  vas-e2e-secondary-agent:\n" +
		"    model: fake-secondary-model\n" +
		"    reasoning_effort: high\n"
	if err := os.WriteFile(filepath.Join(configDir, "vassentinel.yml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	chain, err := agentadapter.NewAgentAdapter(worktree)
	if err != nil {
		t.Fatalf("NewAgentAdapter(auto): %v", err)
	}
	reporter, reportsEffective := chain.(agentadapter.ReportsEffectiveAgent)
	if !reportsEffective {
		t.Fatal("the built chain cannot report its effective agent")
	}
	promptChain, isPrompt := chain.(agentadapter.PromptAdapter)
	if !isPrompt {
		t.Fatal("the built chain cannot answer arbitrary prompts")
	}

	backing := newE2EStore(t)
	controller := execution.NewControllerWithClock(backing, chainPromptAdapter{chain: promptChain}, fixedClock())
	handle, err := controller.Start(context.Background(), e2eRequest("fallback-chain"), e2ePolicy())
	if err != nil {
		t.Fatal(err)
	}
	completion, err := handle.Wait(context.Background())
	if err != nil || completion.State != agentrun.StateSucceeded {
		t.Fatalf("completion = %+v, %v; want the secondary to serve a successful run", completion, err)
	}
	if completion.Output != "secondary review output" {
		t.Fatalf("output = %q, want the secondary provider's answer", completion.Output)
	}

	// Effective-agent recording names whoever actually answered.
	effective, ok := reporter.EffectiveAgent()
	if !ok {
		t.Fatal("the chain recorded no effective agent despite answering")
	}
	if filepath.Base(effective.Binary) != "vas-e2e-secondary-agent" ||
		effective.Model != "fake-secondary-model" || effective.Effort != "high" {
		t.Fatalf("effective agent = %+v, want the secondary's binary/model/effort", effective)
	}

	inspection := assertRunsInspection(t, backing, controller, string(handle.RunID),
		agentrun.StateSucceeded, agentrun.TerminalSuccess)
	if len(inspection.Outcomes) != 1 ||
		inspection.Outcomes[0].OutputHash != execution.HashAdapterOutput(completion.Output) {
		t.Fatalf("outcomes = %+v, want one success whose OutputHash binds the served output", inspection.Outcomes)
	}
}

func requireNonWindows(t *testing.T) {
	t.Helper()
	if strings.EqualFold(runtime.GOOS, "windows") {
		t.Skip("fake provider shell fixtures require a POSIX host")
	}
}

// successOutputAdapter settles every attempt with the given output.
type successOutputAdapter string

func (a successOutputAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
	return execution.AdapterResult{Output: string(a)}, nil
}
