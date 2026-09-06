package agentadapter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
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

// TestOpenCodeReviewUsage pins the review-stream contract of the dedicated
// opencode event parser: the observable answer is the ordered concatenation
// of every type=text part text, usage is the sum over every step_finish
// event, and the terminal stop reason is the LAST step_finish reason
// translated onto the acpadapter vocabulary ("stop" -> "end_turn", everything
// else verbatim so Classify fails it by default). A stream that never reports
// tokens yields a nil Usage — absent must never become zeros — while observed
// zeros survive as non-nil pointers.
func TestOpenCodeReviewUsage(t *testing.T) {
	fixture := loadOpenCodeProbeFixture(t)
	cases := []struct {
		name          string
		stream        string
		wantText      string
		wantUsage     *acpadapter.Usage
		wantUsageJSON string
		wantStop      string
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scan, err := scanOpenCodeReview(strings.NewReader(tc.stream))
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
