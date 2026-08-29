package acpadapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// --- golden parse tests -----------------------------------------------------

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

// TestParseStreamGoldenEndTurn mirrors the A1 exec-stream fixture shape: an
// initialize result exposing the model config option, thought chunks that
// must not reach the output, message chunks that do, and a terminal end_turn
// carrying usage.
func TestParseStreamGoldenEndTurn(t *testing.T) {
	got := ParseStream(strings.NewReader(readFixture(t, "end-turn.jsonl")), DefaultLineCapBytes)
	if got.Output != "PROBE_OK" {
		t.Errorf("Output = %q, want %q", got.Output, "PROBE_OK")
	}
	if got.ObservedModel != "opencode/big-pickle" {
		t.Errorf("ObservedModel = %q, want %q", got.ObservedModel, "opencode/big-pickle")
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", got.StopReason, "end_turn")
	}
	wantUsage := `{"inputTokens":42,"outputTokens":3,"totalTokens":45}`
	if got.UsageJSON != wantUsage {
		t.Errorf("UsageJSON = %q, want %q", got.UsageJSON, wantUsage)
	}
	if got.Violations != 0 {
		t.Errorf("Violations = %d, want 0", got.Violations)
	}
	if class := Classify(got.StopReason); class != agentrun.OutcomeSuccess {
		t.Errorf("Classify(end_turn) = %q, want %q", class, agentrun.OutcomeSuccess)
	}
}

// TestParseStreamGoldenCancelled mirrors the A1 cancel2-stream shape: a
// cooperative session/cancel followed by a cancelled terminal with a
// zero-usage object.
func TestParseStreamGoldenCancelled(t *testing.T) {
	got := ParseStream(strings.NewReader(readFixture(t, "cancelled.jsonl")), DefaultLineCapBytes)
	if want := "1\n2\n3\n"; got.Output != want {
		t.Errorf("Output = %q, want %q", got.Output, want)
	}
	if got.StopReason != "cancelled" {
		t.Errorf("StopReason = %q, want %q", got.StopReason, "cancelled")
	}
	wantUsage := `{"inputTokens":0,"outputTokens":0,"totalTokens":0}`
	if got.UsageJSON != wantUsage {
		t.Errorf("UsageJSON = %q, want %q", got.UsageJSON, wantUsage)
	}
	if got.Violations != 0 {
		t.Errorf("Violations = %d, want 0", got.Violations)
	}
	if class := Classify(got.StopReason); class != agentrun.OutcomeCancellation {
		t.Errorf("Classify(cancelled) = %q, want %q", class, agentrun.OutcomeCancellation)
	}
}

// TestParseStreamFramingViolations pins framing tolerance (C2): one non-JSON
// line and one line over the configured cap are skipped and counted while the
// remaining stream still yields the correct output.
func TestParseStreamFramingViolations(t *testing.T) {
	const cap = 256 // every fixture line except the padded one fits under it
	got := ParseStream(strings.NewReader(readFixture(t, "framing-violations.jsonl")), cap)
	if got.Output != "STILL_OK" {
		t.Errorf("Output = %q, want %q (oversize chunk text must be dropped)", got.Output, "STILL_OK")
	}
	if got.Violations != 2 {
		t.Errorf("Violations = %d, want 2 (one invalid JSON, one over-cap)", got.Violations)
	}
	if got.ObservedModel != "kimi-k2" {
		t.Errorf("ObservedModel = %q, want %q", got.ObservedModel, "kimi-k2")
	}
	if got.StopReason != "end_turn" || Classify(got.StopReason) != agentrun.OutcomeSuccess {
		t.Errorf("StopReason/Class = %q/%q, want end_turn/success", got.StopReason, Classify(got.StopReason))
	}
}

// TestParseStreamClaudeOmitsModel pins that an initialize result without
// configOptions (the claude-agent-acp 0.60.0 shape verified in A1) leaves the
// observed model empty instead of inventing one.
func TestParseStreamClaudeOmitsModel(t *testing.T) {
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true},"authMethods":[],"agentInfo":{"name":"claude-agent-acp","version":"0.60.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`,
	}, "\n") + "\n"
	got := ParseStream(strings.NewReader(stream), DefaultLineCapBytes)
	if got.ObservedModel != "" {
		t.Errorf("ObservedModel = %q, want empty (backend omits configOptions)", got.ObservedModel)
	}
	if got.UsageJSON != "" {
		t.Errorf("UsageJSON = %q, want empty (terminal carries no usage member)", got.UsageJSON)
	}
	if got.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want %q", got.StopReason, "end_turn")
	}
}

// TestClassifyTable pins the full stopReason vocabulary mapping.
func TestClassifyTable(t *testing.T) {
	cases := []struct {
		stopReason string
		want       agentrun.OutcomeClass
	}{
		{"end_turn", agentrun.OutcomeSuccess},
		{"cancelled", agentrun.OutcomeCancellation},
		{"", agentrun.OutcomeFailure},
		{"max_tokens", agentrun.OutcomeFailure},
		{"refusal", agentrun.OutcomeFailure},
	}
	for _, tc := range cases {
		if got := Classify(tc.stopReason); got != tc.want {
			t.Errorf("Classify(%q) = %q, want %q", tc.stopReason, got, tc.want)
		}
	}
}

// --- adapter construction and command building ------------------------------

func TestNewAcpxRequiresAgentToken(t *testing.T) {
	if _, err := NewAcpx(Config{}); err == nil {
		t.Fatal("NewAcpx without agent token must fail")
	}
	if _, err := NewAcpx(Config{Agent: "  "}); err == nil {
		t.Fatal("NewAcpx with blank agent token must fail")
	}
	if _, err := NewAcpx(Config{Agent: "claude"}); err != nil {
		t.Fatalf("NewAcpx(claude) unexpected error: %v", err)
	}
}

// TestEnforcementDeclarationRetention pins that the validated C6 admission
// verdict survives construction: an empty declaration normalizes to
// EnforcementNone, and the declared value is retained verbatim and exposed
// through EnforcementDeclaration for every later observer.
func TestEnforcementDeclarationRetention(t *testing.T) {
	a, err := NewAcpx(Config{Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.EnforcementDeclaration(); got != EnforcementNone {
		t.Errorf("EnforcementDeclaration() = %q, want normalized %q", got, EnforcementNone)
	}

	a, err = NewAcpx(Config{Agent: "claude", Enforcement: EnforcementNone})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.EnforcementDeclaration(); got != EnforcementNone {
		t.Errorf("EnforcementDeclaration() = %q, want %q", got, EnforcementNone)
	}

	if runtime.GOOS == "windows" {
		return // claude-sandbox fails construction there by design (C6)
	}
	a, err = NewAcpx(Config{Agent: "claude", Enforcement: EnforcementClaudeSandbox})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.EnforcementDeclaration(); got != EnforcementClaudeSandbox {
		t.Errorf("EnforcementDeclaration() = %q, want retained %q", got, EnforcementClaudeSandbox)
	}
}

func TestArgsGlobalFlagsBeforeAgentToken(t *testing.T) {
	a, err := NewAcpx(Config{Agent: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-y", "acpx@latest", "--format", "json", "--json-strict",
		"--timeout", "300", "claude", "exec", "review this diff"}
	got := a.Args("review this diff")
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Args = %v\nwant      %v", got, want)
	}
}

func TestArgsCustomLauncherAndTimeout(t *testing.T) {
	a, err := NewAcpx(Config{
		Launcher:          []string{"acpx"},
		Agent:             "opencode",
		MaxRuntimeSeconds: 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--format", "json", "--json-strict", "--timeout", "90",
		"opencode", "exec", "hi"}
	got := a.Args("hi") // custom launcher carries no base args
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Args = %v\nwant %v", got, want)
	}
}

// --- identity ---------------------------------------------------------------

func TestEffectiveIdentityTable(t *testing.T) {
	cases := []struct {
		name        string
		cfg         Config
		runObserved bool
		observed    string
		wantBinary  string
		wantModel   string
		wantEffort  string
	}{
		{
			name:        "observed model wins over configured",
			cfg:         Config{Agent: "claude", Model: "configured-model"},
			runObserved: true,
			observed:    "observed-model",
			wantBinary:  "npx:claude",
			wantModel:   "observed-model",
		},
		{
			name:       "configured fallback when nothing observed",
			cfg:        Config{Agent: "claude", Model: "configured-model"},
			wantBinary: "npx:claude",
			wantModel:  "configured-model",
		},
		{
			name:       "both absent leaves model empty",
			cfg:        Config{Agent: "codex"},
			wantBinary: "npx:codex",
			wantModel:  "",
		},
		{
			name:       "effort passthrough",
			cfg:        Config{Agent: "claude", Effort: "high"},
			wantBinary: "npx:claude",
			wantEffort: "high",
		},
		{
			name:       "effort empty when unconfigured",
			cfg:        Config{Agent: "claude"},
			wantBinary: "npx:claude",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewAcpx(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if tc.runObserved {
				a.recordObserved(tc.observed, "")
			}
			got := a.EffectiveIdentity()
			if got.Binary != tc.wantBinary {
				t.Errorf("Binario = %q, want %q", got.Binary, tc.wantBinary)
			}
			if got.Model != tc.wantModel {
				t.Errorf("Modelo = %q, want %q", got.Model, tc.wantModel)
			}
			if got.Effort != tc.wantEffort {
				t.Errorf("Esfuerzo = %q, want %q", got.Effort, tc.wantEffort)
			}
			empty := got.Binary == "" && got.Model == "" && got.Effort == ""
			if got.Empty() != empty {
				t.Errorf("Vacio() = %v, inconsistent with fields %+v", got.Empty(), got)
			}
		})
	}
}

// --- helper-process pipeline tests ------------------------------------------
//
// The fake acpx child (TestHelperProcess) and its spawn helpers live in
// helper_test.go; slice-2 spawn scenarios extend them there.

// TestRunPipelineEndTurn exercises the full spawn -> scan -> normalize path
// against the fake acpx child and asserts the retained evidence contract.
func TestRunPipelineEndTurn(t *testing.T) {
	a := spawnHelper(t, helperModeOK)
	res, runErr := a.Run(context.Background(), "say PROBE")
	if runErr != nil {
		t.Fatalf("Run returned error: %v", runErr)
	}
	if res.Output != "HELLO FROM FAKE ACPX" {
		t.Errorf("Output = %q, want assembled chunk text", res.Output)
	}
	if res.Class() != agentrun.OutcomeSuccess {
		t.Errorf("Class() = %q, want success", res.Class())
	}
	if res.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", res.StopReason)
	}
	if res.Violations != 0 {
		t.Errorf("Violations = %d, want 0", res.Violations)
	}
	if res.ObservedModel != "test-model" {
		t.Errorf("ObservedModel = %q, want test-model", res.ObservedModel)
	}
	wantUsage := `{"inputTokens":1,"outputTokens":2}`
	if res.UsageJSON != wantUsage {
		t.Errorf("UsageJSON = %q, want %q", res.UsageJSON, wantUsage)
	}
	if !strings.Contains(res.RawStream, `"agent_message_chunk"`) {
		t.Errorf("RawStream must retain verbatim stdout bytes; got %q", res.RawStream)
	}
	if res.Enforcement != EnforcementNone {
		t.Errorf("Enforcement = %q, want the normalized declaration on every result", res.Enforcement)
	}
	ident := a.EffectiveIdentity()
	if ident.Model != "test-model" {
		t.Errorf("post-run identity Modelo = %q, want observed test-model", ident.Model)
	}
	if !strings.HasSuffix(ident.Binary, ":claude") {
		t.Errorf("Binario = %q, want launcher base + :claude suffix", ident.Binary)
	}
	if ident.Effort != "high" {
		t.Errorf("Esfuerzo = %q, want configured high", ident.Effort)
	}
}

// TestRunExitZeroCancelledIsCancellation pins the both-exit-0 trap: exit code
// zero plus stopReason "cancelled" must classify as cancellation, never as
// success.
func TestRunExitZeroCancelledIsCancellation(t *testing.T) {
	a := spawnHelper(t, helperModeTrap)
	res, runErr := a.Run(context.Background(), "say PROBE")
	if runErr == nil {
		t.Fatal("Run must surface a classified error for a cancelled turn")
	}
	var outcome *OutcomeError
	if !errors.As(runErr, &outcome) {
		t.Fatalf("Run error type = %T, want *OutcomeError", runErr)
	}
	if outcome.Outcome() != agentrun.OutcomeCancellation {
		t.Errorf("Outcome() = %q, want %q (exit 0 must not upgrade the verdict)", outcome.Outcome(), agentrun.OutcomeCancellation)
	}
	if outcome.Class == agentrun.OutcomeSuccess {
		t.Error("classification fell back to success despite stopReason cancelled")
	}
	if res.Class() != agentrun.OutcomeCancellation {
		t.Errorf("Result.Class() = %q, want cancellation", res.Class())
	}
	if res.StopReason != "cancelled" {
		t.Errorf("StopReason = %q, want cancelled", res.StopReason)
	}
	if res.Output != "" {
		t.Errorf("Output = %q, want empty", res.Output)
	}
}

// TestParseStreamLastLineWithoutNewline pins that a final NDJSON line with no
// trailing newline is still parsed instead of being dropped at EOF.
func TestParseStreamLastLineWithoutNewline(t *testing.T) {
	raw := "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"stopReason\":\"end_turn\"}}" // no trailing \n
	stream := ParseStream(strings.NewReader(raw), DefaultLineCapBytes)
	if stream.Violations != 0 {
		t.Errorf("Violations = %d, want 0", stream.Violations)
	}
	if stream.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", stream.StopReason)
	}
}
