package main

import (
	"bytes"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/metrics"
)

// TestRenderMetricsJSONReportsCacheWriteAsNullWhenUnobserved pins the
// absent-stays-absent rule at the metrics surface: a usage aggregate with no
// observed cache-write count must render its JSON value as null — never 0 —
// through sentinel metrics --json.
func TestRenderMetricsJSONReportsCacheWriteAsNullWhenUnobserved(t *testing.T) {
	report := metrics.Report{Executions: metrics.ExecutionAggregate{
		LogicalRuns:           1,
		MeasuredRuns:          1,
		CacheWriteInputTokens: metrics.Measurement{Total: 1, Coverage: metrics.Coverage{Total: 1}},
	}}
	var output bytes.Buffer
	if err := renderMetricsJSON(&output, report); err != nil {
		t.Fatalf("renderMetricsJSON failed: %v", err)
	}
	decoded := decodeMetricsJSON(t, output.Bytes())
	executions, ok := decoded["executions"].(map[string]any)
	if !ok {
		t.Fatalf("executions object missing: %#v", decoded)
	}
	cacheWrite, ok := executions["cache_write_input_tokens"].(map[string]any)
	if !ok {
		t.Fatalf("cache_write_input_tokens object missing: %#v", executions)
	}
	assertJSONNull(t, cacheWrite, "value")
	if cacheWrite["observed"] != float64(0) || cacheWrite["total"] != float64(1) {
		t.Errorf("cache-write coverage fields are not explicit: %#v", cacheWrite)
	}
}
