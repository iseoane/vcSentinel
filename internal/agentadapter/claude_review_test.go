package agentadapter

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

// The redacted probe fixture reproduces Claude Code 2.1.263's
// `--output-format json` review result for the production reviewer
// invocation: one single-line result object (NOT NDJSON) with the answer in
// `result`, the token report in `usage`, and the terminal outcome in
// `stop_reason`/`subtype`. session_id and uuid were replaced with
// probe_redacted placeholders; the usage member is verbatim.

// claudeUsageJSON is the verbatim usage member of the probe fixture. It is
// retained raw so evidence consumers see exactly what crossed the wire.
const claudeUsageJSON = `{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":6465,"output_tokens":4,"output_tokens_details":{"thinking_tokens":0},"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},"inference_geo":"not_available","iterations":[{"input_tokens":2,"output_tokens":4,"cache_read_input_tokens":6465,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"}],"speed":"standard"}`

// claudeUsage is the probe fixture's token report mapped onto
// acpadapter.Usage: Input from input_tokens, Output from output_tokens,
// Cached from cache_read_input_tokens, CacheWrite from
// cache_creation_input_tokens, Reasoning from
// output_tokens_details.thinking_tokens. TotalTokens stays nil: the wire
// carries no total_tokens member and computing one would fabricate evidence.
var claudeUsage = &acpadapter.Usage{
	InputTokens:           new(int64(2)),
	OutputTokens:          new(int64(4)),
	CachedInputTokens:     new(int64(6465)),
	CacheWriteInputTokens: new(int64(0)),
	ReasoningTokens:       new(int64(0)),
}

func loadClaudeProbeFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "claude", "usage-probe.json"))
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	return string(raw)
}

// TestClaudeReviewUsage pins the review-result contract of the dedicated
// claude parser: the observable answer is the result member (trimmed once at
// the boundary by the caller, byte-identical to the plain-text output the
// probe captured), usage maps the four recognized members with TotalTokens
// always nil, and the terminal stop reason is the result's stop_reason
// verbatim — "end_turn" arrives already in acpadapter's success vocabulary —
// falling back to the success subtype only when the wire omits stop_reason.
// Malformed, empty or error results abort with an error instead of leaking
// raw JSON (or a failed run's text) as the review answer.
func TestClaudeReviewUsage(t *testing.T) {
	fixture := loadClaudeProbeFixture(t)
	cases := []struct {
		name          string
		stream        string
		wantText      string
		wantUsage     *acpadapter.Usage
		wantUsageJSON string
		wantStop      string
		wantErr       string
	}{
		{
			name:          "redacted probe fixture",
			stream:        fixture,
			wantText:      "OK",
			wantUsage:     claudeUsage,
			wantUsageJSON: claudeUsageJSON,
			wantStop:      "end_turn",
		},
		{
			name:     "stream without usage yields nil usage",
			stream:   `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"OK"}`,
			wantText: "OK",
			wantStop: "end_turn",
		},
		{
			name:     "observed zeros stay non-nil pointers and total stays nil",
			stream:   `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"none","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"output_tokens_details":{"thinking_tokens":0}}}`,
			wantText: "none",
			wantUsage: &acpadapter.Usage{
				InputTokens:       new(int64(0)),
				OutputTokens:      new(int64(0)),
				CachedInputTokens: new(int64(0)),
				ReasoningTokens:   new(int64(0)),
			},
			wantUsageJSON: `{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"output_tokens_details":{"thinking_tokens":0}}`,
			wantStop:      "end_turn",
		},
		{
			name:     "missing stop_reason falls back to success subtype",
			stream:   `{"type":"result","subtype":"success","result":"fallback"}`,
			wantText: "fallback",
			wantStop: "end_turn",
		},
		{
			name:     "unsuccessful stop reasons stay raw",
			stream:   `{"type":"result","subtype":"success","stop_reason":"tool_use","result":"partial"}`,
			wantText: "partial",
			wantStop: "tool_use",
		},
		{
			name:     "no stop reason and non-success subtype stays raw",
			stream:   `{"type":"result","subtype":"error_during_execution","result":""}`,
			wantStop: "error_during_execution",
		},
		{
			name:     "absent stop reason and subtype stays empty",
			stream:   `{"type":"result","result":"quiet"}`,
			wantText: "quiet",
			wantStop: "",
		},
		{
			name:    "empty stream fails closed",
			stream:  "",
			wantErr: "empty claude output",
		},
		{
			name:    "whitespace-only stream fails closed",
			stream:  "  \n ",
			wantErr: "empty claude output",
		},
		{
			name:    "malformed json fails closed",
			stream:  `{"type":"result"`,
			wantErr: "invalid claude JSON output",
		},
		{
			name:    "error result fails closed",
			stream:  `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`,
			wantErr: "error result",
		},
		{
			name:     "explicit null usage yields nil usage and empty raw member",
			stream:   `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"quiet","usage":null}`,
			wantText: "quiet",
			wantStop: "end_turn",
		},
		{
			name:    "malformed inner usage member fails closed",
			stream:  `{"type":"result","subtype":"success","result":"x","usage":5}`,
			wantErr: "usage",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scan, err := scanClaudeReview(strings.NewReader(tc.stream))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("scanClaudeReview() = %+v, expected an error mentioning %q", scan, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, expected it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("scanClaudeReview() error = %v", err)
			}
			if scan.Output != tc.wantText {
				t.Errorf("Output = %q, want %q", scan.Output, tc.wantText)
			}
			switch {
			case tc.wantUsage == nil && scan.Usage != nil:
				t.Errorf("Usage = %+v, want nil (absent usage must never become zeros)", scan.Usage)
			case tc.wantUsage != nil && scan.Usage == nil:
				t.Errorf("Usage = nil, want %+v", tc.wantUsage)
			case tc.wantUsage != nil:
				if scan.Usage == nil || !reflect.DeepEqual(scan.Usage, tc.wantUsage) {
					t.Errorf("Usage = %+v, want %+v", scan.Usage, tc.wantUsage)
				}
				if scan.Usage != nil && scan.Usage.TotalTokens != nil {
					t.Errorf("TotalTokens = %v, want nil (the wire carries no total and it is never computed)", scan.Usage.TotalTokens)
				}
			}
			if scan.UsageJSON != tc.wantUsageJSON {
				t.Errorf("UsageJSON = %q, want %q", scan.UsageJSON, tc.wantUsageJSON)
			}
			if scan.StopReason != tc.wantStop {
				t.Errorf("StopReason = %q, want %q", scan.StopReason, tc.wantStop)
			}
		})
	}
}

// TestClaudeReviewTextParityWithPlainTextBaseline grounds the text-parity
// claim in the probe's paired captures: the same review answer captured in
// plain-text mode ("OK\n") and in --output-format json mode (result "OK").
// The adapter's single boundary TrimSpace makes the two surfaces identical.
func TestClaudeReviewTextParityWithPlainTextBaseline(t *testing.T) {
	baseline, err := os.ReadFile(filepath.Join("testdata", "claude", "usage-probe.baseline.txt"))
	if err != nil {
		t.Fatalf("read plain-text baseline: %v", err)
	}
	scan, err := scanClaudeReview(strings.NewReader(loadClaudeProbeFixture(t)))
	if err != nil {
		t.Fatalf("scanClaudeReview() error = %v", err)
	}
	if got, want := strings.TrimSpace(scan.Output), strings.TrimSpace(string(baseline)); got != want {
		t.Errorf("text parity broken: json result = %q, plain-text output = %q", got, want)
	}
}

// TestClaudeReviewResultReportsWireObservations drives the rich
// ReviewWithContextResult surface end to end against a fake claude binary
// replaying the redacted probe fixture: the review invocation must request
// --output-format json, the child must not run in the audited repository
// (claude runs with the snapshot as its cwd; the positive cwd==snapshot
// assertion lives in reviewer_test.go's TestRunReviewClaudeUsesSnapshotDirAsCwd),
// the wire observations must land in
// acpadapter.Result with configured declarations in Requested* and empty
// Observed* (the result object exposes no top-level identity), and the
// legacy string contract must answer with the identical extracted text.
func TestClaudeReviewResultReportsWireObservations(t *testing.T) {
	repoCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get the current directory: %v", err)
	}
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", loadClaudeProbeFixture(t))
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "claude"),
		Config:     config.AgentConfig{Model: "claude-haiku-4-5"},
		Timeout:    10 * time.Second,
	}

	result, err := adapter.ReviewWithContextResult(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("ReviewWithContextResult() error = %v", err)
	}
	if result.Output != "OK" {
		t.Errorf("Output = %q, want the probe review text", result.Output)
	}
	if !reflect.DeepEqual(result.Usage, claudeUsage) {
		t.Errorf("Usage = %+v, want %+v", result.Usage, claudeUsage)
	}
	if result.UsageJSON != claudeUsageJSON {
		t.Errorf("UsageJSON = %q, want the verbatim usage member", result.UsageJSON)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", result.StopReason)
	}
	if result.RequestedModel != "claude-haiku-4-5" || result.RequestedEffort != "" {
		t.Errorf("declarations = %q/%q, want the configured model with the unset effort echoed", result.RequestedModel, result.RequestedEffort)
	}
	if result.ObservedModel != "" || result.ObservedEffort != "" {
		t.Errorf("observed identity = %q/%q, want empty (the result object exposes none)", result.ObservedModel, result.ObservedEffort)
	}

	capture := readAgentCapture(t, capturePath)
	if len(capture.Args) < 2 || !reflect.DeepEqual(capture.Args[len(capture.Args)-2:], []string{"--output-format", "json"}) {
		t.Errorf("args = %v, want the review invocation to end with --output-format json", capture.Args)
	}
	// Claude has no --dir flag; this asserts only that the child did NOT run
	// in the audited repository. The positive cwd==snapshot assertion lives
	// in reviewer_test.go's TestRunReviewClaudeUsesSnapshotDirAsCwd.
	if samePath(capture.Dir, repoCwd) {
		t.Errorf("cmd.Dir = %q, expected the isolated snapshot directory, not the repository", capture.Dir)
	}

	output, err := adapter.ReviewWithContext(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("ReviewWithContext() error = %v", err)
	}
	if output != "OK" {
		t.Errorf("ReviewWithContext() = %q, want the identical extracted review text", output)
	}
}
