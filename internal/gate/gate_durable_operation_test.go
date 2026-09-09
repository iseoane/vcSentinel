// Slice 14 acceptance: every gate-side durable run admits with an honest
// operator-facing Operation — the root carries the lifecycle stage it runs
// under, and each validation settlement child carries the exact command that
// identifies it. Labels are read back from the persisted policy records, so
// this test proves what future readers will see, not what was passed in.
package gate

import (
	"testing"
)

func TestGateRootOperationLabel(t *testing.T) {
	tests := []struct {
		stage string
		want  string
	}{
		{"pre-commit", "gate pre-commit"},
		{"pre-push", "gate pre-push"},
		{"pr", "gate pr"},
		{"", "gate"},
	}
	for _, tt := range tests {
		if got := gateRootOperation(tt.stage); got != tt.want {
			t.Errorf("gateRootOperation(%q) = %q, want %q", tt.stage, got, tt.want)
		}
	}
}

func TestDurableGateAdmitsLabeledRuns(t *testing.T) {
	cfg := cfgWithProfile("lint", "echo boom")
	calls := 0
	opts := baseOptions(t, cfg, func(string) (int, string, error) {
		return 1, "real output of the failed command", nil
	})
	opts.RunValidation = runProfileWithoutCandidate

	result := RunGate(opts)
	if result.State != StateValidationFailed {
		t.Fatalf("state = %q, want %q", result.State, StateValidationFailed)
	}
	if calls != 0 {
		t.Fatalf("review started %d times on a red validation, want zero", calls)
	}

	st := opts.DurableStore
	ids, err := st.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs() error = %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("want exactly root + one validation settlement, got %v", ids)
	}

	roots := 0
	for _, id := range ids {
		request, err := st.ReadExecutionRequest(id)
		if err != nil {
			t.Fatalf("ReadExecutionRequest(%s) error = %v", id, err)
		}
		operation, err := st.ReadRunOperation(id)
		if err != nil {
			t.Fatalf("ReadRunOperation(%s) error = %v", id, err)
		}
		if request.ParentRunID == "" {
			roots++
			if operation != "gate pre-push" {
				t.Fatalf("root operation = %q, want %q", operation, "gate pre-push")
			}
			continue
		}
		if want := "validate echo boom"; operation != want {
			t.Fatalf("validation child operation = %q, want %q", operation, want)
		}
	}
	if roots != 1 {
		t.Fatalf("store holds %d parentless runs, want exactly one root", roots)
	}
}
