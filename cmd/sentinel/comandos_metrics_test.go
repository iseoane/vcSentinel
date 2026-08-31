package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/metrics"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func TestRenderMetricsJSONUsesStableUnitsAndNullForUnknown(t *testing.T) {
	report := metrics.Report{Executions: metrics.ExecutionAggregate{
		LogicalRuns:  1,
		MeasuredRuns: 1,
		Duration:     metrics.Measurement{Total: 1, Coverage: metrics.Coverage{Total: 1}},
	}}
	var output bytes.Buffer
	if code := renderMetricsJSON(&output, report); code != 0 {
		t.Fatalf("renderMetricsJSON exit code = %d", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("metrics JSON is invalid: %v\n%s", err, output.String())
	}
	executions, ok := decoded["executions"].(map[string]any)
	if !ok {
		t.Fatalf("executions object missing: %#v", decoded)
	}
	duration, ok := executions["duration_nanos"].(map[string]any)
	if !ok {
		t.Fatalf("duration_nanos object missing: %#v", executions)
	}
	if duration["value"] != nil {
		t.Errorf("unknown duration must be JSON null, got %#v", duration["value"])
	}
	if duration["observed"] != float64(0) || duration["total"] != float64(1) {
		t.Errorf("duration coverage fields are not explicit: %#v", duration)
	}
	if _, ok := executions["duration"]; ok {
		t.Error("duration must use a unit-bearing JSON key")
	}
}

func TestRenderMetricsJSONIsDeterministic(t *testing.T) {
	report := metrics.Report{Costs: []metrics.CostAggregate{{Currency: "USD"}, {Currency: "EUR"}}}
	var first, second bytes.Buffer
	if renderMetricsJSON(&first, report) != 0 || renderMetricsJSON(&second, report) != 0 {
		t.Fatal("renderMetricsJSON failed")
	}
	if first.String() != second.String() {
		t.Fatal("equivalent metrics reports produced different JSON")
	}
}

func TestMetricsArgumentsAndHelp(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"argument"}, {"--json", "--unknown"}} {
		if message := validarArgumentos("metrics", args); message == "" {
			t.Errorf("metrics accepted undeclared arguments %v", args)
		}
	}
	if message := validarArgumentos("metrics", []string{"--json"}); message != "" {
		t.Fatalf("metrics rejected --json: %s", message)
	}
	var output bytes.Buffer
	if code := executeMetrics(&output, t.TempDir(), []string{"--unknown"}); code != 1 || !strings.Contains(output.String(), "accepts only --json") {
		t.Errorf("invalid metrics arguments were not rejected before store access: %d/%q", code, output.String())
	}
	if !strings.Contains(construirAyuda(), "metrics") || !escribirAyudaComando(&bytes.Buffer{}, "metrics") {
		t.Error("metrics help is not registered")
	}
	for _, flag := range []string{"--help", "-h"} {
		var stdout, stderr bytes.Buffer
		if !gestionarAyuda(&stdout, &stderr, "metrics", []string{flag}) || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Errorf("metrics %s help streams = %q/%q", flag, stdout.String(), stderr.String())
		}
	}
}

func TestMetricsStoreScenarios(t *testing.T) {
	cases := []struct {
		name, text string
		args       []string
		setup      func(*testing.T, string)
		want       int
	}{
		{name: "empty", want: 0, text: "WARNING: insufficient samples"},
		{name: "empty-json", args: []string{"--json"}, want: 0, text: "\"executions\""},
		{name: "historical", setup: writeHistoricalMetrics, want: 0, text: "confirmed=1"},
		{name: "mixed", setup: writeMixedMetrics, want: 0, text: "Remediation: attempts=1"},
		{name: "unreadable", setup: writeCorruptMetrics, want: 1, text: "cannot aggregate store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, common := newMetricsRepository(t)
			if tc.setup != nil {
				tc.setup(t, common)
			}
			var output bytes.Buffer
			if code := executeMetrics(&output, repo, tc.args); code != tc.want {
				t.Fatalf("metrics exit code = %d, want %d: %s", code, tc.want, output.String())
			}
			if !strings.Contains(output.String(), tc.text) {
				t.Errorf("metrics output lacks %q: %s", tc.text, output.String())
			}
		})
	}
}

func newMetricsRepository(t *testing.T) (string, string) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	common, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("git common directory: %v", err)
	}
	return repo, common
}

func writeHistoricalMetrics(t *testing.T, common string) {
	t.Helper()
	err := review.NuevoLedger(common).GuardarRevision("historical", "message", "bucket", "model", review.Revision{
		AggregatedFindings: []review.Hallazgo{{Fingerprint: "finding", Dimension: review.DimLogic, Status: review.StatusConfirmed}},
	})
	if err != nil {
		t.Fatalf("write historical finding: %v", err)
	}
}

func writeMixedMetrics(t *testing.T, common string) {
	t.Helper()
	writeHistoricalMetrics(t, common)
	if err := ops.RegistrarEvento(common, "repair", 0, nil, ops.EventDetail{"kind": "remediation", "target": "finding", "dimension": "logic"}, ""); err != nil {
		t.Fatalf("write remediation event: %v", err)
	}
}

func writeCorruptMetrics(t *testing.T, common string) {
	t.Helper()
	path := filepath.Join(common, "vas-sentinel", "metrics", "v1", "corrupt.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0600); err != nil {
		t.Fatal(err)
	}
}
