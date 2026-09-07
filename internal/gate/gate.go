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

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// Terminal gate states: closed vocabulary from the T1.7 ticket.
const (
	StatePass                      = "PASS"
	StateValidationFailed          = "VALIDATION_FAILED"
	StateCodeReviewFailed          = "CODE_REVIEW_FAILED"
	StateNeedsUserReview           = "NEEDS_USER_REVIEW"
	StateReviewInfrastructureError = "REVIEW_INFRASTRUCTURE_ERROR"
)

// ExitCode maps Result.State to the exact exit code the ticket asks for:
// 0 PASS, 1 VALIDATION_FAILED, 2 NEEDS_USER_REVIEW, 4
// REVIEW_INFRASTRUCTURE_ERROR. An unknown state is NEVER treated as PASS: a
// gate whose purpose is to block cannot fail open on a value this very
// package does not recognize (e.g. a new state added in RunGate and forgotten
// here), so it maps to the same code as REVIEW_INFRASTRUCTURE_ERROR (4): an
// unrecognized state is a problem of the gate's own infrastructure, not a
// passed validation.
func ExitCode(state string) int {
	switch state {
	case StatePass:
		return 0
	case StateValidationFailed:
		return 1
	case StateCodeReviewFailed:
		return 1
	case StateNeedsUserReview:
		return 2
	case StateReviewInfrastructureError:
		return 4
	default:
		return 4
	}
}

// Result is the output of RunGate: the final state and the messages already
// worded so the command prints them as-is, without adding presentation logic
// in cmd/.
type ReviewerFailure struct {
	Bundle    string
	Dimension string
	Reason    string
}

type Result struct {
	State    string
	Messages []string
	// ContextSkipReason is observational context-provider evidence. It never
	// changes the semantic verdict but remains available to operator events.
	ContextSkipReason string
	// ReviewerFailures carries compact causes for unavailable dimensions to
	// operator events without replacing the detailed terminal messages.
	ReviewerFailures []ReviewerFailure
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

	// ReviewerFactory builds the agent of each semantic review dimension
	// (same contract as review.AuditCommit). gate does not build real agents
	// itself: that is cmd/ plumbing, exactly what runReview does today.
	ReviewerFactory review.ReviewerFactory
	RefuterFactory  review.RefuterFactory
	Parallel        int
	ReviewOptions   review.AuditOptions

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
	// DurableReviewTransportFactory constructs the review-side transport used
	// by the durable orchestration's review phase. It receives the gate's
	// ROOT run ID so production wiring can thread the parent linkage into the
	// review-side durable runs (review runs are constructed at a different
	// site — inside the factory — and cannot be stamped by the gate
	// orchestrator itself). Production wiring must be the same construction
	// path `sentinel review` uses today (durableReviewTransport in
	// cmd/sentinel), so reviewer invocations keep inheriting admission, owned
	// process trees, and cancellation; tests inject substitutes and
	// invocation counters here. When nil, the review phase falls back to
	// ReviewOptions.ReviewTransport (engine-level injection seam): there
	// is no second review execution path.
	DurableReviewTransportFactory func(rootRunID agentrun.Identity) review.ReviewTransport
	// DurableReviewTransportFactoryWithEvidence is the preferred factory for
	// semantic finalization. It preserves parent linkage while returning the
	// owner callback for the exact physical run.
	DurableReviewTransportFactoryWithEvidence func(rootRunID agentrun.Identity) (review.ReviewTransportWithEvidence, review.MetricsFinalizer)
	// DurableReviewChildren reports the review-side child run identities that
	// were ACTUALLY admitted during the review phase (ticket 11 slice 3).
	// Review candidate identities are process-salted inside the shared
	// durable transport, so the orchestrator cannot derive them from the
	// plan: production wiring records every admission through a
	// concurrency-safe observer sink and hands the drain function here. The
	// orchestrator consumes it when composing the root settlement's
	// "|children=" enumeration so the enumerated set equals the persisted
	// ParentRunID scan even though neither side derives the IDs
	// deterministically. When nil, only planned validation jobs (and any
	// resolvable planned review job) are enumerated.
	DurableReviewChildren func() []agentrun.Identity
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
	messages := []string{"❌ Validation FAILED: semantic review not executed."}
	for _, h := range findings {
		messages = append(messages,
			fmt.Sprintf("  ✖ %s (%s):\n%s", h.Capability, h.Command, strings.TrimSpace(h.Evidence)))
	}
	return messages
}

// translateVerdict keeps validation and semantic blockers distinct. A refuted
// semantic critical finding remains visible and requires human review.
func translateVerdict(result review.AuditResult) Result {
	contextSkipReason := result.ContextSkipReason
	reviewerFailures := compactReviewerFailures(result)
	switch result.Verdict {
	case review.VerdictUnavailable:
		return Result{
			State:             StateReviewInfrastructureError,
			Messages:          unavailableReviewMessages(result),
			ContextSkipReason: contextSkipReason,
			ReviewerFailures:  reviewerFailures,
		}
	case review.VerdictQuestion:
		messages := []string{"❓ Semantic review requires explicit human attention:"}
		for _, question := range result.Questions {
			messages = append(messages, fmt.Sprintf("  ? %s", question.Text))
		}
		messages = append(messages, unavailableDimensionMessages(result)...)
		return Result{
			State:             StateNeedsUserReview,
			Messages:          messages,
			ContextSkipReason: contextSkipReason,
			ReviewerFailures:  reviewerFailures,
		}
	case review.VerdictBlock:
		messages := []string{
			"❌ Semantic review confirmed CRITICAL findings.",
			result.String(),
		}
		messages = append(messages, criticalFindingMessages(result)...)
		messages = append(messages, unavailableDimensionMessages(result)...)
		return Result{
			State:             StateCodeReviewFailed,
			Messages:          messages,
			ContextSkipReason: contextSkipReason,
			ReviewerFailures:  reviewerFailures,
		}
	default:
		if hasRefutedCriticalFinding(result) {
			return Result{
				State:             StateNeedsUserReview,
				Messages:          []string{"❓ Semantic review refuted a CRITICAL finding and requires human attention.", result.String()},
				ContextSkipReason: contextSkipReason,
				ReviewerFailures:  reviewerFailures,
			}
		}
		return Result{
			State:             StatePass,
			Messages:          []string{"✅ Validation and semantic review green.", result.String()},
			ContextSkipReason: contextSkipReason,
			ReviewerFailures:  reviewerFailures,
		}
	}
}

func hasRefutedCriticalFinding(result review.AuditResult) bool {
	for _, dimension := range result.Dims {
		if dimension.Result != nil && dimension.Result.RefutedCritical {
			return true
		}
	}
	return false
}

func unavailableReviewMessages(auditResult review.AuditResult) []string {
	messages := []string{"❌ Semantic review could not run (agent unavailable or infrastructure error)."}
	dimensionMessages := unavailableDimensionMessages(auditResult)
	if len(dimensionMessages) == 0 {
		return append(messages, "  No unavailable dimension evidence was retained.")
	}
	return append(messages, dimensionMessages...)
}

func unavailableDimensionMessages(auditResult review.AuditResult) []string {
	dimensions := unavailableDimensions(auditResult)
	if len(dimensions) == 0 {
		return nil
	}

	messages := []string{"Unavailable dimensions:"}
	for _, dimension := range dimensions {
		dimensionName := dimension.Dim
		reason := ""
		if dimension.Result != nil {
			if dimensionName == "" {
				dimensionName = dimension.Result.Dim
			}
			reason = dimension.Result.Reason
		}
		if reason == "" && dimension.Error != nil {
			reason = dimension.Error.Error()
		}
		// Ticket 07: admission failures surface with their own class label
		// instead of wearing generic infrastructure unavailability. Exit-code
		// contracts are untouched: this marker only classifies evidence.
		messages = append(messages, fmt.Sprintf("  - dimension=%q reason=%q class=%s", dimensionName, reason, failureClass(dimension)))
	}
	return messages
}

func compactReviewerFailures(auditResult review.AuditResult) []ReviewerFailure {
	dimensions := unavailableDimensions(auditResult)
	if len(dimensions) == 0 {
		return nil
	}
	failures := make([]ReviewerFailure, 0, len(dimensions))
	for _, dimension := range dimensions {
		dimensionName := dimension.Dim
		bundleName := dimension.Bundle
		reason := ""
		if dimension.Result != nil {
			if dimensionName == "" {
				dimensionName = dimension.Result.Dim
			}
			if bundleName == "" {
				bundleName = dimension.Result.Bundle
			}
			reason = dimension.Result.Reason
		}
		if reason == "" && dimension.Error != nil {
			reason = dimension.Error.Error()
		}
		reason = review.CompactProviderCause(reason)
		if strings.TrimSpace(reason) == "" {
			continue
		}
		failures = append(failures, ReviewerFailure{
			Bundle:    bundleName,
			Dimension: dimensionName,
			Reason:    reason,
		})
	}
	return failures
}

// failureClass labels an unavailable dimension as an admission failure or an
// infrastructure failure (ticket 07). The typed transport error wins when the
// engine retained it; once only the persisted reason survives, classification
// goes through the literal admission prefix both share.
func failureClass(dimension review.DimensionOutcome) string {
	if reviewexec.IsAdmissionError(dimension.Error) {
		return "admission"
	}
	if dimension.Result != nil && reviewexec.IsAdmissionReason(dimension.Result.Reason) {
		return "admission"
	}
	return "infrastructure"
}

func unavailableDimensions(auditResult review.AuditResult) []review.DimensionOutcome {
	var dimensions []review.DimensionOutcome
	for _, dimension := range auditResult.Dims {
		if dimension.Result != nil && dimension.Result.Verdict == review.VerdictUnavailable {
			dimensions = append(dimensions, dimension)
			continue
		}
		if dimension.Result == nil && dimension.Error != nil {
			dimensions = append(dimensions, dimension)
		}
	}
	return dimensions
}

func criticalFindingMessages(auditResult review.AuditResult) []string {
	messages := []string{"Confirmed CRITICAL finding evidence:"}
	findings := effectiveCriticalFindings(auditResult)
	if len(findings) == 0 {
		return append(messages, "  No structured finding evidence was retained.")
	}

	for _, finding := range findings {
		fingerprint := finding.Fingerprint
		if fingerprint == "" {
			fingerprint = review.Fingerprint(finding)
		}
		identity := finding.ID
		if identity == "" {
			identity = fingerprint
		}
		producer := finding.Producer
		primaryEvidence := finding.Evidence
		if finding.EvidenceSet != nil && len(finding.EvidenceSet.Values) > 0 {
			if primaryEvidence == "" {
				primaryEvidence = finding.EvidenceSet.Values[0].Evidence
			}
			if producer == (review.Producer{}) {
				producer = finding.EvidenceSet.Values[0].Producer
			}
		}

		messages = append(messages,
			fmt.Sprintf("  finding identity=%q", identity),
			fmt.Sprintf("    id: %q", finding.ID),
			fmt.Sprintf("    fingerprint: %q", fingerprint),
			fmt.Sprintf("    dimension: %q", finding.Dimension),
			fmt.Sprintf("    location: file=%q line_start=%d line_end=%d symbol=%q blob=%q", finding.Location.File, finding.Location.LineStart, finding.Location.LineEnd, finding.Location.Simbolo, finding.Location.Blob),
			fmt.Sprintf("    description: %q", finding.Description),
			fmt.Sprintf("    evidence: %q", primaryEvidence),
			fmt.Sprintf("    confidence: %g", finding.Confidence),
			fmt.Sprintf("    producer: %s", formatProducer(producer)),
		)
		if finding.EvidenceSet == nil {
			continue
		}
		for index, corroboratingEvidence := range finding.EvidenceSet.Values {
			if index == 0 && corroboratingEvidence.Evidence == primaryEvidence {
				continue
			}
			messages = append(messages, fmt.Sprintf("    corroborating evidence[%d]: dimension=%q confidence=%g producer=%s value=%q", index+1, corroboratingEvidence.Dimension, corroboratingEvidence.Confidence, formatProducer(corroboratingEvidence.Producer), corroboratingEvidence.Evidence))
		}
	}
	return messages
}

func effectiveCriticalFindings(auditResult review.AuditResult) []review.Finding {
	findings := append([]review.Finding(nil), auditResult.Findings...)
	hasAggregatedFindings := len(auditResult.Findings) > 0
	for _, dimension := range auditResult.Dims {
		if dimension.Result == nil {
			continue
		}
		dimensionResult := dimension.Result
		if !hasAggregatedFindings {
			for _, legacyFinding := range dimensionResult.Findings {
				findings = append(findings, findingFromLegacy(dimension.Dim, legacyFinding))
			}
		}
		for _, legacyFinding := range dimensionResult.Findings {
			if isLegacyFindingRepresented(legacyFinding, dimension.Dim, auditResult.Findings) {
				continue
			}
			findings = append(findings, findingFromLegacy(dimension.Dim, legacyFinding))
		}
	}

	var effective []review.Finding
	for _, finding := range findings {
		// The shared FU-6 blocking rule: engine, gate, and BranchBlockers
		// agree about the same record, so a human-refuted or fixed CRITICAL
		// finding stops being effective here exactly as elsewhere.
		if review.IsBlocking(finding.Severity, finding.Status) {
			effective = append(effective, finding)
		}
	}
	return effective
}

func isLegacyFindingRepresented(legacy review.ReviewFinding, dimension string, v2Findings []review.Finding) bool {
	for _, v2Finding := range v2Findings {
		if v2Finding.Dimension == dimension &&
			v2Finding.Severity == legacy.Severity &&
			v2Finding.Description == legacy.Description &&
			v2Finding.Location.File == legacy.File &&
			v2Finding.Location.LineStart == int(legacy.Line) {
			return true
		}
	}
	return false
}

func findingFromLegacy(dimension string, finding review.ReviewFinding) review.Finding {
	status := finding.Status
	if status == "" {
		status = review.StatusConfirmed
	}
	convertedFinding := review.Finding{
		Dimension:   dimension,
		Severity:    finding.Severity,
		Status:      status,
		Description: finding.Description,
		Location: review.Location{
			File:      finding.File,
			LineStart: int(finding.Line),
		},
	}
	convertedFinding.Fingerprint = review.Fingerprint(convertedFinding)
	return convertedFinding
}

func formatProducer(producer review.Producer) string {
	return fmt.Sprintf("agent=%q binary=%q model=%q reasoning_effort=%q model_verified=%t", producer.Agent, producer.Binary, producer.Model, producer.Effort, producer.ModelVerified)
}
