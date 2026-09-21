// Facade pins for the real durable gate orchestration. This file owns the
// byte-level facade contract: the exact State/Messages shapes per terminal
// class. The CLI contract (stdout text, exit codes) is produced from Result
// State/Messages by cmd/vcsentinel, so these tests pin the facade at its source.
//
// Piece 3 of docs/design/review-flow-ownership.md removed the semantic phase,
// and with it the review-block and refuted-CRITICAL rows this table used to
// pin. What remains is the whole vocabulary a deterministic gate can produce:
// pass, validation failure, and infrastructure.
package gate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/store"
	"github.com/ISeoane-Quental/vcSentinel/internal/validation"
)

// durableOptions overrides an already-wired base Options set with a fresh
// temp-dir store, so tests assert against fixtures built by baseOptions.
func durableOptions(t *testing.T, base Options) Options {
	t.Helper()
	durable := base
	durable.Stage = "pre-push"
	durable.CandidateSHA = "0123456789abcdef"
	durable.DurableStore = store.NewStore(filepath.Join(t.TempDir(), "gate-common"))
	return durable
}

// TestGateGreenStatesWhatItCovers pins the PASS shape. The notice is not
// decoration: piece 3 changed what a green gate asserts, from "validation and
// semantics" to "validation only", under the same policy and the same run
// identity family. Without it, a historical PASS and a current one read alike.
func TestGateGreenStatesWhatItCovers(t *testing.T) {
	opts := durableOptions(t, baseOptions(t, cfgWithProfile("lint", "echo ok"),
		func(string) (int, string, error) { return 0, "ok", nil }))
	opts.RunValidation = runProfileWithoutCandidate

	result := RunGate(opts)

	if result.State != StatePass {
		t.Fatalf("green validation must pass, got %q: %v", result.State, result.Messages)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, ValidationCoverageNotice) {
		t.Fatalf("a green gate must state what it covers, got:\n%s", joined)
	}
	if strings.Contains(strings.ToLower(joined), "semantic") {
		t.Fatalf("a deterministic gate must not claim a semantic verdict, got:\n%s", joined)
	}
}

// TestGateValidationFailureShowsRealEvidence pins that a red validation
// reports the command's own output rather than a summary: the gate must never
// invent a verdict it did not observe.
func TestGateValidationFailureShowsRealEvidence(t *testing.T) {
	opts := durableOptions(t, baseOptions(t, cfgWithProfile("lint", "echo boom"),
		func(string) (int, string, error) { return 1, "real output of the failed command", nil }))
	opts.RunValidation = runProfileWithoutCandidate

	result := RunGate(opts)

	if result.State != StateValidationFailed {
		t.Fatalf("red validation must fail the gate, got %q", result.State)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, "real output of the failed command") {
		t.Fatalf("validation failure must carry the command's own evidence, got:\n%s", joined)
	}
}

func TestGateDurableInfrastructurePinsTexts(t *testing.T) {
	t.Run("plan failure keeps the prefix verbatim", func(t *testing.T) {
		opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil)
		opts.Stage = ""
		opts.CandidateSHA = "abc123def456"
		opts.DurableStore = store.NewStore(filepath.Join(t.TempDir(), "gate-common"))

		result := RunGate(opts)

		want := "gate durable run plan failed before wiring: gate: invalid durable run plan (stage): must be a non-empty lifecycle stage"
		if result.State != StateInfrastructureError || result.Messages[0] != want {
			t.Fatalf("plan-failure facade drifted:\n got  %q / %q\n want %q", result.State, result.Messages[0], want)
		}
	})

	t.Run("missing store names the seam explicitly", func(t *testing.T) {
		opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil)
		opts.Stage = "pr"
		opts.CandidateSHA = "abc123def456"
		opts.DurableStore = nil

		result := RunGate(opts)

		want := "gate: durable runs require an injected store"
		if result.State != StateInfrastructureError || result.Messages[0] != want {
			t.Fatalf("missing-store facade drifted:\n got  %q / %q\n want %q", result.State, result.Messages[0], want)
		}
	})
}

var _ = validation.RunProfile
