// Package gate orchestrates the "gate" subcommand (T1.7): a single
// consolidated entry point that requires green validation (T1.6) BEFORE
// invoking the semantic review (internal/review), instead of letting every
// lifecycle hook (pre-commit, pre-push, pr) reimplement its own ordering.
//
// The business logic lives here (never in cmd/, see the architecture note of
// the ticket): cmd/sentinel/commands_gate.go only parses flags, resolves the
// real seams (git, agentadapter), and prints Result.
package gate

import (
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// Terminal gate states: closed vocabulary from the T1.7 ticket.
const (
	StatePass                = "PASS"
	StateValidationFailed    = "VALIDATION_FAILED"
	StateInfrastructureError = "INFRASTRUCTURE_ERROR"
)

// ValidationCoverageNotice is printed alongside a green gate and states what
// that green now covers. Piece 3 changed the meaning of PASS: it used to
// assert validation AND a semantic audit, and it now asserts validation only.
// Both carry the same policy and the same run identity family, so a historical
// PASS and a current one are indistinguishable from the record alone. Saying
// it on every green run is the cheapest honest way to keep the two apart for a
// person reading output; the durable contract declares it separately.
const ValidationCoverageNotice = "   Coverage: deterministic validation only. Per-commit quality is `sentinel review`."

// ExitCode maps Result.State to its exit code: 0 PASS, 1 VALIDATION_FAILED,
// 4 INFRASTRUCTURE_ERROR. An unknown state is NEVER treated as PASS: a gate
// whose purpose is to block cannot fail open on a value this very package
// does not recognize (e.g. a new state added in RunGate and forgotten here),
// so it maps to the same code as INFRASTRUCTURE_ERROR (4): an unrecognized
// state is a problem of the gate's own infrastructure, not a passed
// validation.
//
// Exit code 2 is retired with the semantic phase (piece 3): it carried
// NEEDS_USER_REVIEW, the "a human must look at this" outcome, which only a
// semantic audit can produce. Nothing deterministic can reach it, so the gate
// no longer emits it rather than keeping a code that can never occur.
func ExitCode(state string) int {
	switch state {
	case StatePass:
		return 0
	case StateValidationFailed:
		return 1
	case StateInfrastructureError:
		return 4
	default:
		return 4
	}
}

// Result is the output of RunGate: the final state and the messages already
// worded so the command prints them as-is, without adding presentation logic
// in cmd/.

type Result struct {
	State    string
	Messages []string
	// ContextSkipReason is observational context-provider evidence. It never
	// changes the semantic verdict but remains available to operator events.
	ContextSkipReason string
	// ReviewerFailures carries compact causes for unavailable dimensions to
	// operator events without replacing the detailed terminal messages.
	// Err carries a typed error produced by the durable orchestration when a
	// plan, admission, or settlement seam fails (R9 slice 1). Facade text
	// travels in State/Messages; Err exists for callers that need the typed
	// cause.
	Err error
}

// Options configures a gate run. The RunValidation and ReviewerFactory seams
// are the ones T0.x/T1.6 and the review.AuditCommit engine already expose as
// injectable: gate adds no new indirection layer, it reuses the existing one
// so it can be tested without real processes or agents.
type Options struct {
	// Profile is the validation.profiles entry to run (the command's
	// --profile). Do not confuse it with Stage: Stage identifies the
	// lifecycle point (messages/record), Profile identifies WHAT is validated
	// — they are independent axes (T1.7 design decision, also documented in
	// cmd/sentinel/commands_gate.go).
	Profile      string
	ChangedPaths []string

	ValidationOptions validation.RunOptions
	// RunValidation is the T1.6 orchestration function
	// (validation.RunProfileOnCandidate); nil uses that same function.
	// Injectable to simulate infrastructure failures (stale candidate,
	// snapshot that could not be created...) without depending on real git.
	RunValidation func(profile string, scope []string, opts validation.RunOptions) ([]validation.ValidationRun, error)

	// Stage is the --stage lifecycle context embedded in the durable root
	// run request. cmd/sentinel's facade messages also read the stage value.
	Stage string
	// CandidateSHA is the candidate HEAD commit SHA embedded in the durable
	// root run request.
	CandidateSHA string
	// DurableStore backs the root gate run and every validation-job
	// settlement. A nil store fails honestly as infrastructure before any
	// phase executes; cmd/sentinel wires it through applyDurableCutover over
	// the repository common-dir store.
	DurableStore *store.Store
}

// validationNotRunMessage is the single facade text for a validation
// ORCHESTRATION failure (infrastructure, never a code finding). The durable
// orchestration renders it so equivalent inputs keep the historical facade
// text byte-identical.
func validationNotRunMessage(err error) string {
	return fmt.Sprintf("Could not run validation: %v", err)
}

// validationFailedMessages words the detail of which commands failed and
// their real output: the ticket demands showing real evidence, never
// inventing a PASS.
func validationFailedMessages(findings []validation.Finding) []string {
	messages := []string{"❌ Validation FAILED."}
	for _, h := range findings {
		messages = append(messages,
			fmt.Sprintf("  ✖ %s (%s):\n%s", h.Capability, h.Command, strings.TrimSpace(h.Evidence)))
	}
	return messages
}
