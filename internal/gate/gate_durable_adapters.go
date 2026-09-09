// Execution adapters for the gate's durable orchestration (R9 slice 2), plus
// the small constants and helpers that exist purely to serve them. See
// gate_durable.go for the orchestration flow and the layer-separation rules
// this wiring pins.
//
// Scope of this file:
//
//   - rootRunAdapter blocks the root run's worker until the orchestrator
//     sends its final settlement, keeping StateRunning alive across both
//     phases without executing anything itself.
//   - settledValidationAdapter settles one already-executed validation
//     command to its deterministic outcome, always carrying the
//     RecordValidationEvidence serialization as adapter output.
//   - executedValidationJob stamps the RESOLVED command onto the planned job
//     capability attributes when it differs from the planned base command.
//   - The failing-layer detail constants and withChildren
//     build and resolve the machine-parseable settlement detail texts.
package gate

import (
	"context"
	"errors"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// Failing-layer detail texts carried verbatim in the root run's settled
// outcome error, so durable state alone can explain WHICH layer failed even
// though success/failure/unavailable classes are shared vocabulary.
const (
	layerValidationDetail     = "gate: failing layer: validation"
	layerInfrastructureDetail = "gate: failing layer: infrastructure"
)

// withChildren appends the machine-parseable child enumeration to a failing
// layer's settlement detail ("|children=<id1,id2,...>") so post-settlement
// reconstruction from the root record alone is possible. The human facade
// text is untouched: this lives only in the durable settlement detail.
func withChildren(detail string, children []agentrun.Identity) string {
	if len(children) == 0 {
		return detail
	}
	ids := make([]string, 0, len(children))
	for _, child := range children {
		ids = append(ids, string(child))
	}
	return detail + "|children=" + strings.Join(ids, ",")
}

// resolvedCommandAttribute records, inside the same gate.validation.command
// capability, the RESOLVED command (the resolverComando output) that actually
// executed. The planned base command stays in "command": base (plan) versus
// scoped (executed) are therefore both readable from one capability whenever
// graph scope authorization selected the scoped variant. The attribute is
// additive and present ONLY when the two differ — full-scope runs execute the
// base verbatim and keep exactly their planned identities.
const resolvedCommandAttribute = "resolved_command"

// executedValidationJob rebuilds one planned validation logical job for
// admission with its resolverComando outcome recorded. When the resolved
// command equals the planned base command (or is unknown), the planned job is
// returned untouched.
func executedValidationJob(planned GateJobPlan, run validation.ValidationRun) agentrun.LogicalJob {
	if run.Command == "" || run.Command == planned.Command {
		return planned.Job
	}
	request := planned.Job.Request()
	stamped := make([]agentrun.Capability, 0, len(request.Capabilities()))
	for _, capability := range request.Capabilities() {
		if capability.Name() != capabilityNameCommand {
			stamped = append(stamped, capability)
			continue
		}
		attributes := capability.Attributes()
		attributes[resolvedCommandAttribute] = run.Command
		stamped = append(stamped, agentrun.NewCapability(capability.Name(), attributes))
	}
	return agentrun.NewLogicalJob(agentrun.NewRunRequest(request.Candidate(), request.Prompt(), stamped))
}

// evidenceOutput serializes the deterministic evidence entry of one settled
// validation command. The settled adapter returns exactly this text, so the
// controller hash-binds the whole tuple into the durable AttemptOutcome
// without persisting raw output bytes.
func evidenceOutput(index int, evidence []ValidationEvidence) string {
	if index >= len(evidence) {
		return ""
	}
	return evidence[index].String()
}

// rootRunAdapter blocks the root run's worker until the orchestration sends
// its final settlement, keeping StateRunning alive across both phases. It
// executes nothing itself: the phases run outside the worker, and every
// durable lifecycle mutation still happens through controller APIs.
type rootRunAdapter struct {
	settle <-chan rootSettlement
}

func (a rootRunAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	settlement := <-a.settle
	if settlement.class == agentrun.OutcomeSuccess {
		return execution.AdapterResult{}, nil
	}
	return execution.AdapterResult{}, execution.NewAdapterError(settlement.class, errors.New(settlement.detail))
}

// settledValidationAdapter settles one already-executed validation command to
// its deterministic outcome. The adapter output ALWAYS carries the
// RecordValidationEvidence serialization — including on failure: the
// controller hash-binds AdapterResult.Output into AttemptOutcome.OutputHash
// even when the returned error classifies the attempt, so a failed command
// keeps its command/exit/duration evidence digest (class=failure with a
// non-empty OutputHash) instead of losing it exactly when it matters most.
type settledValidationAdapter struct {
	class  agentrun.OutcomeClass
	detail string
	output string
}

func (a settledValidationAdapter) Execute(_ context.Context, _ agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	if a.class == agentrun.OutcomeSuccess {
		return execution.AdapterResult{Output: a.output}, nil
	}
	return execution.AdapterResult{Output: a.output}, execution.NewAdapterError(a.class, errors.New(a.detail))
}
