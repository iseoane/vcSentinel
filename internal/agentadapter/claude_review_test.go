package agentadapter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
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
// Cached from cache_read_input_tokens only (cache_creation_input_tokens is
// observed but unmapped, like cost), Reasoning from
// output_tokens_details.thinking_tokens. TotalTokens stays nil: the wire
// carries no total_tokens member and computing one would fabricate evidence.
var claudeUsage = &acpadapter.Usage{
	InputTokens:       new(int64(2)),
	OutputTokens:      new(int64(4)),
	CachedInputTokens: new(int64(6465)),
	ReasoningTokens:   new(int64(0)),
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
			wantErr: "vacia",
		},
		{
			name:    "whitespace-only stream fails closed",
			stream:  "  \n ",
			wantErr: "vacia",
		},
		{
			name:    "malformed json fails closed",
			stream:  `{"type":"result"`,
			wantErr: "salida JSON invalida",
		},
		{
			name:    "error result fails closed",
			stream:  `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`,
			wantErr: "resultado de error",
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
