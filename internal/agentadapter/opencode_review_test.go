package agentadapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// The redacted probe fixture reproduces OpenCode 1.18.29's `--format json`
// review stream for the exact production reviewer invocation (reviewer agent,
// opencode-go/glm-5.3-flash, effort unset): two model steps separated by a
// tool step, ending with one final answer. Session, message, call and part
// identifiers were replaced with probe_* placeholders and the tool outputs
// truncated; the text, token and reason members are verbatim.

// probeReviewText is the byte-exact answer the probe fixture carries in its
// type=text part: the observable review text the parser must recover.
const probeReviewText = "Findings:\n\n" +
	"| File:Line | Severity | Rationale |\n" +
	"|---|---|---|\n" +
	"| internal/calc/calc.go:7-8 | major | Off-by-one: doc (line 3) promises sum of first *n* positive integers (1..n = n(n+1)/2), but loop adds 0..n-1, yielding n(n-1)/2 (Suma(4)=6, contract says 10). |\n" +
	"| internal/calc/calc.go:15 | major | `a / b` panics on `b == 0`; no guard or documented error path for zero divisor despite being an exported API. |\n" +
	"| internal/calc/calc_test.go:6 | major | Test asserts `Suma(4) == 6`, enshrining the buggy off-by-one behavior instead of the documented contract (10), so the defect is invisible to CI. |\n" +
	"| internal/calc/calc_test.go:5-9 | minor | No tests for `Dividir` at all — the panic-prone function (b=0) has zero coverage. |\n" +
	"| internal/calc/calc.go:5-11 | minor | Behavior for `n < 0` is unspecified (silently returns 0); contract should state it or reject negative input. |"

// probeUsageJSON is the verbatim tokens member of each step_finish event in
// the probe fixture, one per line in stream order. The parser must retain
// these raw bytes so evidence hashing sees exactly what crossed the wire.
const probeUsageJSON = `{"total":5721,"input":5621,"output":49,"reasoning":51,"cache":{"write":0,"read":0}}` + "\n" +
	`{"total":7217,"input":466,"output":272,"reasoning":911,"cache":{"write":0,"read":5568}}`

// probeUsage is the sum over EVERY step_finish event of the probe fixture.
// Cached counts cache.read only; cache.write and the per-step cost member
// stay observed-but-unmapped because acpadapter.Usage has no destination for
// them.
var probeUsage = &acpadapter.Usage{
	InputTokens:       new(int64(6087)),  // 5621 + 466
	OutputTokens:      new(int64(321)),   // 49 + 272
	TotalTokens:       new(int64(12938)), // 5721 + 7217
	CachedInputTokens: new(int64(5568)),  // 0 + 5568
	ReasoningTokens:   new(int64(962)),   // 51 + 911
}

func loadOpenCodeProbeFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "opencode", "usage-probe.ndjson"))
	if err != nil {
		t.Fatalf("read probe fixture: %v", err)
	}
	return string(raw)
}

// Compile-time proof of the structural match cli_review_context.go claims:
// *CLIAdapter satisfies reviewexec's rich reviewer contracts. The signatures
// mirror reviewexec.ResultContextualReviewer and
// reviewexec.ResultPolicyContextualReviewer; they are restated here because
// importing reviewexec from a test would still cycle through the package
// under test's import graph.
var _ interface {
	ReviewWithContextResult(ctx context.Context, prompt, sha string, paths []string) (acpadapter.Result, error)
	ReviewWithContextAndPolicyResult(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (acpadapter.Result, error)
} = (*CLIAdapter)(nil)

// TestOpenCodeReviewUsage pins the review-stream contract of the dedicated
// opencode event parser: the observable answer is the ordered concatenation
// of every type=text part text, usage is the sum over every step_finish
// event, and the terminal stop reason is the LAST step_finish reason
// translated onto the acpadapter vocabulary ("stop" -> "end_turn", everything
// else verbatim so Classify fails it by default). A stream that never reports
// tokens yields a nil Usage — absent must never become zeros — while observed
// zeros survive as non-nil pointers.
// Broken framing — an empty stream, blank or oversized lines, malformed
// JSON, or an unparseable usage member — aborts the scan with an error
// instead of leaking raw NDJSON (or a silently truncated answer) as the
// review text.
func TestOpenCodeReviewUsage(t *testing.T) {
	fixture := loadOpenCodeProbeFixture(t)
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
			wantText:      probeReviewText,
			wantUsage:     probeUsage,
			wantUsageJSON: probeUsageJSON,
			wantStop:      "end_turn",
		},
		{
			name: "stream without usage yields nil usage",
			stream: "{\"type\":\"step_start\",\"part\":{\"type\":\"step-start\"}}\n" +
				"{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"Findings: none\"}}\n" +
				"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\"}}\n",
			wantText: "Findings: none",
			wantStop: "end_turn",
		},
		{
			name: "observed zeros stay non-nil pointers",
			stream: "{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"empty\"}}\n" +
				"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\",\"tokens\":{\"total\":0,\"input\":0,\"output\":0,\"reasoning\":0,\"cache\":{\"write\":0,\"read\":0}}}}\n",
			wantText: "empty",
			wantUsage: &acpadapter.Usage{
				InputTokens:       new(int64(0)),
				OutputTokens:      new(int64(0)),
				TotalTokens:       new(int64(0)),
				CachedInputTokens: new(int64(0)),
				ReasoningTokens:   new(int64(0)),
			},
			wantUsageJSON: `{"total":0,"input":0,"output":0,"reasoning":0,"cache":{"write":0,"read":0}}`,
			wantStop:      "end_turn",
		},
		{
			name: "partially reported fields keep absent ones nil",
			stream: "{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"tool-calls\",\"tokens\":{\"input\":100}}}\n" +
				"{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"partial\"}}\n",
			wantText:      "partial",
			wantUsage:     &acpadapter.Usage{InputTokens: new(int64(100))},
			wantUsageJSON: `{"input":100}`,
			wantStop:      "tool-calls",
		},
		{
			name: "multiple text parts concatenate in stream order",
			stream: "{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"Findings:\\n\\n\"}}\n" +
				"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"tool-calls\"}}\n" +
				"{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"more text\"}}\n" +
				"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\"}}\n",
			wantText: "Findings:\n\nmore text",
			wantStop: "end_turn",
		},
		{
			name: "last step_finish reason wins and unmapped reasons stay raw",
			stream: "{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\"}}\n" +
				"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"tool-calls\"}}\n",
			wantStop: "tool-calls",
		},
		{
			name:     "cancelled reason passes through verbatim",
			stream:   "{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"cancelled\"}}\n",
			wantStop: "cancelled",
		},
		{
			name:     "no step_finish yields empty stop reason",
			stream:   "{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"orphan answer\"}}\n",
			wantText: "orphan answer",
			wantStop: "",
		},
		{
			name:    "empty stream fails closed",
			stream:  "",
			wantErr: "empty opencode JSONL output",
		},
		{
			name:    "blank intermediate line fails closed",
			stream:  "{\"type\":\"step_start\"}\n\n{\"type\":\"text\",\"part\":{\"text\":\"x\"}}\n",
			wantErr: "empty line",
		},
		{
			name:    "malformed json line fails closed",
			stream:  "{\"type\":\"text\"\n",
			wantErr: "invalid opencode JSONL output",
		},
		{
			name:    "concatenated objects on one line fail closed",
			stream:  "{\"type\":\"step_start\"}{\"type\":\"text\",\"part\":{\"text\":\"x\"}}\n",
			wantErr: "invalid opencode JSONL output",
		},
		{
			name:    "oversized line fails closed",
			stream:  fmt.Sprintf("{\"type\":\"step_start\",\"padding\":%q}\n", strings.Repeat("x", 1024*1024)),
			wantErr: "invalid opencode JSONL output",
		},
		{
			name:    "usage member that is not an object fails closed",
			stream:  "{\"type\":\"step_finish\",\"part\":{\"reason\":\"stop\",\"tokens\":5}}\n",
			wantErr: "usage",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scan, err := scanOpenCodeReview(strings.NewReader(tc.stream))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("scanOpenCodeReview() = %+v, expected an error mentioning %q", scan, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, expected it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("scanOpenCodeReview() error = %v", err)
			}
			if scan.Output != tc.wantText {
				t.Errorf("Output = %q, want %q", scan.Output, tc.wantText)
			}
			switch {
			case tc.wantUsage == nil && scan.Usage != nil:
				t.Errorf("Usage = %+v, want nil (absent usage must never become zeros)", scan.Usage)
			case tc.wantUsage != nil && scan.Usage == nil:
				t.Errorf("Usage = nil, want %+v", tc.wantUsage)
			case tc.wantUsage != nil && !reflect.DeepEqual(scan.Usage, tc.wantUsage):
				t.Errorf("Usage = %+v, want %+v", scan.Usage, tc.wantUsage)
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

// TestOpenCodeReviewResultReportsWireObservations drives the rich
// ReviewWithContextResult surface end to end against a fake opencode binary
// replaying the redacted probe fixture: the review invocation must request
// --format json, the extracted review text must replace the raw NDJSON
// stream, the wire observations must land in acpadapter.Result, and the
// configured declarations must ride in Requested* while Observed* stays
// empty (the OpenCode event stream provides no wire identity). The legacy
// string contract must answer with the same observable text.
func TestOpenCodeReviewResultReportsWireObservations(t *testing.T) {
	capturaRuta := filepath.Join(t.TempDir(), "captura.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturaRuta)
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", loadOpenCodeProbeFixture(t))
	adapter := CLIAdapter{
		BinaryName: compilarAgenteConNombre(t, "opencode"),
		Config:     config.AgentConfig{Model: "opencode-go/glm-5.3-flash"},
		Timeout:    10 * time.Second,
	}

	result, err := adapter.ReviewWithContextResult(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("ReviewWithContextResult() error = %v", err)
	}
	if result.Output != probeReviewText {
		t.Errorf("Output = %q, want the probe review text", result.Output)
	}
	if !reflect.DeepEqual(result.Usage, probeUsage) {
		t.Errorf("Usage = %+v, want %+v", result.Usage, probeUsage)
	}
	if result.UsageJSON != probeUsageJSON {
		t.Errorf("UsageJSON = %q, want %q", result.UsageJSON, probeUsageJSON)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", result.StopReason)
	}
	if result.RequestedModel != "opencode-go/glm-5.3-flash" || result.RequestedEffort != "" {
		t.Errorf("declarations = %q/%q, want the configured model with the unset effort echoed", result.RequestedModel, result.RequestedEffort)
	}
	if result.ObservedModel != "" || result.ObservedEffort != "" {
		t.Errorf("observed identity = %q/%q, want empty (the event stream provides none)", result.ObservedModel, result.ObservedEffort)
	}
	captura := leerCapturaAgente(t, capturaRuta)
	args := captura.Args
	if len(args) < 2 || !reflect.DeepEqual(args[len(args)-2:], []string{"--format", "json"}) {
		t.Errorf("args = %v, want the review invocation to end with --format json", args)
	}
	dirIndex := -1
	for i, arg := range args {
		if arg == "--dir" {
			dirIndex = i
			break
		}
	}
	// The child's cwd stays the caller's: for OpenCode the immutable snapshot
	// travels as the --dir value, which must never be the repository itself.
	if dirIndex == -1 || dirIndex+1 >= len(args) || mismaRuta(args[dirIndex+1], captura.Dir) {
		t.Errorf("args = %v, want --dir bound to an isolated snapshot directory (child cwd %q)", args, captura.Dir)
	}
	output, err := adapter.ReviewWithContext(context.Background(), "review SNAPSHOT", headSha(t), []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("ReviewWithContext() error = %v", err)
	}
	if output != probeReviewText {
		t.Errorf("ReviewWithContext() = %q, want the identical extracted review text", output)
	}
}
