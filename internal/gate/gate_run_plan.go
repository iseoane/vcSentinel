// Gate run-plan machinery for the durable-runs roadmap (R9 slice 1): it
// decomposes ONE gate execution into ONE root run request plus logical job
// descriptors, without executing anything. This file is pure construction
// logic, fully unit-testable without processes or agents. Since R11 removed
// the compatibility switch, this plan is the only gate orchestration path and
// RunGate consumes it unconditionally.
//
// Identity derivation deliberately reuses the agentrun helpers exclusively
// (Candidate, Prompt, NewCapability identities, NewRunRequest, NewLogicalJob):
// no ad-hoc hashing lives here, so gate plans stay comparable with any other
// durable run admitted by the shared transport.
package gate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

// GateJobKind names the layer a logical job belongs to under one gate root
// run. Since piece 3 removed the semantic phase there is exactly one layer,
// and the type is kept rather than collapsed because the plan, the settlement
// and the reconstruction all read it, and a future deterministic layer would
// reintroduce the distinction the type exists to carry.
type GateJobKind string

const (
	GateJobValidation GateJobKind = "validation"
)

// Canonical prompt domains and capability names feeding agentrun identity
// derivation. They are stable contract strings: changing any of them changes
// every derived plan identity.
const (
	promptDomainRoot       = "gate.root"
	promptDomainValidation = "gate.validation"

	capabilityNameCommands = "gate.validation.commands"
	capabilityNameCommand  = "gate.validation.command"
	// capabilityNameAttempt carries the gate execution ordinal and nothing
	// else, so the command layerbilities keep meaning exactly "these commands".
	capabilityNameAttempt = "gate.execution.attempt"
)

// GateJobPlan is one logical job descriptor under the gate root run. Every
// validation job carries its exact command string in deterministic profile
// order.
type GateJobPlan struct {
	Kind    GateJobKind
	Command string
	Job     agentrun.LogicalJob
}

// GateRunPlan decomposes one gate execution into the root run request plus
// its logical job descriptors. Jobs are ordered: validation jobs in exact
// profile order first, then exactly one review job, mirroring today's fixed
// validation-before-review ordering without executing either layer.
type GateRunPlan struct {
	// Attempt is the gate execution ordinal this plan derives its identities
	// from. Zero is the first execution over a candidate and keeps the
	// historical identities; a later attempt exists only because the previous
	// one already settled.
	Attempt      int
	Stage        string
	Profile      string
	CandidateSHA string
	// CommandsIdentity is the deterministic agentrun identity of the ordered
	// validation command list, so observation can explain which command set
	// the root run was planned against. Derived through capability
	// identities; no ad-hoc hashing.
	CommandsIdentity agentrun.Identity
	Root             agentrun.LogicalJob
	Jobs             []GateJobPlan
}

// ValidationJobs returns the validation logical-job descriptors in exact
// profile order. The returned slice is a copy: callers cannot reorder the
// plan underneath itself.
func (p GateRunPlan) ValidationJobs() []GateJobPlan {
	jobs := make([]GateJobPlan, 0, len(p.Jobs))
	for _, job := range p.Jobs {
		if job.Kind == GateJobValidation {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// GatePlanError rejects inputs that would produce an unexplainable plan:
// missing stage/profile/candidate or blank commands. It surfaces at plan
// build time, before any routing decision exists.
type GatePlanError struct {
	Field  string
	Reason string
}

func (e GatePlanError) Error() string {
	return fmt.Sprintf("gate: invalid durable run plan (%s): %s", e.Field, e.Reason)
}

// BuildGateRunPlan decomposes one gate execution into ONE root run request
// plus logical job descriptors. It executes nothing and consults no agent:
// every input comes from resolved configuration and HEAD. The root request
// embeds --stage, the resolved validation profile name, the candidate HEAD
// SHA, and the ordered command-list identity; each validation command gets
// one logical job in exact profile order, followed by exactly one review
// job. The same inputs always derive the same identities.
func BuildGateRunPlan(stage, profile, candidateSHA string, commands []string) (GateRunPlan, error) {
	return BuildGateRunPlanAttempt(stage, profile, candidateSHA, commands, 0)
}

// BuildGateRunPlanAttempt is BuildGateRunPlan for a specific execution attempt.
// Attempt 0 is BuildGateRunPlan exactly, which is what every caller but the
// gate's admission probe wants; keeping the plain signature avoids a magic 0 at
// every call site.
func BuildGateRunPlanAttempt(stage, profile, candidateSHA string, commands []string, attempt int) (GateRunPlan, error) {
	if attempt < 0 {
		return GateRunPlan{}, GatePlanError{Field: "attempt", Reason: "must not be negative"}
	}
	if strings.TrimSpace(stage) == "" {
		return GateRunPlan{}, GatePlanError{Field: "stage", Reason: "must be a non-empty lifecycle stage"}
	}
	if strings.TrimSpace(profile) == "" {
		return GateRunPlan{}, GatePlanError{Field: "profile", Reason: "must be a non-empty validation profile name"}
	}
	if strings.TrimSpace(candidateSHA) == "" {
		return GateRunPlan{}, GatePlanError{Field: "candidate", Reason: "must be a non-empty HEAD SHA"}
	}
	for index, command := range commands {
		if strings.TrimSpace(command) == "" {
			return GateRunPlan{}, GatePlanError{
				Field:  "commands",
				Reason: fmt.Sprintf("command at position %d is empty", index),
			}
		}
	}

	root := agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate(candidateSHA),
		agentrun.Prompt(canonicalPrompt(promptDomainRoot, stage, profile)),
		withAttempt([]agentrun.Capability{agentrun.NewCapability(capabilityNameCommands, commandAttributes(commands))}, attempt),
	))

	jobs := make([]GateJobPlan, 0, len(commands))
	for index, command := range commands {
		position := strconv.Itoa(index)
		jobs = append(jobs, GateJobPlan{
			Kind:    GateJobValidation,
			Command: command,
			Job: agentrun.NewLogicalJob(agentrun.NewRunRequest(
				agentrun.Candidate(candidateSHA),
				agentrun.Prompt(canonicalPrompt(promptDomainValidation, stage, profile, position)),
				withAttempt([]agentrun.Capability{agentrun.NewCapability(capabilityNameCommand, map[string]string{
					"profile":  profile,
					"position": position,
					"command":  command,
				})}, attempt),
			)),
		})
	}
	return GateRunPlan{
		Attempt:          attempt,
		Stage:            stage,
		Profile:          profile,
		CandidateSHA:     candidateSHA,
		CommandsIdentity: ValidationCommandsIdentity(commands),
		Root:             root,
		Jobs:             jobs,
	}, nil
}

// ValidationCommandsIdentity derives the deterministic identity of an
// ordered validation command list through an agentrun capability identity:
// the single sanctioned hashing vocabulary for durable run requests.
func ValidationCommandsIdentity(commands []string) agentrun.Identity {
	return agentrun.NewCapability(capabilityNameCommands, commandAttributes(commands)).Identity()
}

// durableGateCommands resolves the configured base command of every
// capability referenced by the profile, in exact profile order. Scoped
// resolution stays a runtime concern of the validation executor; planning pins
// the deterministic configured command so plan identity never depends on
// graph authorization state.
func durableGateCommands(cfg config.Config, profile string) ([]string, error) {
	names := cfg.Validation.Profiles[profile]
	commands := make([]string, 0, len(names))
	for _, name := range names {
		capability, ok := cfg.Validation.Capabilities[name]
		if !ok {
			return nil, fmt.Errorf("gate: profile %q references capability %q, which is not configured", profile, name)
		}
		commands = append(commands, capability.Command)
	}
	return commands, nil
}

func canonicalPrompt(domain string, parts ...string) string {
	return strings.Join(append([]string{domain}, parts...), "\x00")
}

// withAttempt appends the execution attempt as its OWN capability, so every
// identity of the plan — root and jobs alike — is distinct per attempt. It is a
// separate capability on purpose: folding the ordinal into the command
// capability would give capabilityNameCommands two different derivations, one
// here and one in ValidationCommandsIdentity, and a capability name must mean
// one thing. It also returns a new slice rather than mutating its argument.
//
// Attempt 0 appends NOTHING: the identities a candidate has always derived stay
// byte-identical, so durable records written before this existed remain
// addressable and no migration is needed.
//
// The discriminator is the attempt, never a timestamp or a nonce: two gates
// launched concurrently on the same candidate still derive the same identity
// and one of them is correctly refused, which is the collision guard worth
// keeping. Only a SEQUENTIAL re-run, after the previous attempt settled,
// climbs to the next attempt.
func withAttempt(layerbilities []agentrun.Capability, attempt int) []agentrun.Capability {
	if attempt == 0 {
		return layerbilities
	}
	return append(append([]agentrun.Capability(nil), layerbilities...),
		agentrun.NewCapability(capabilityNameAttempt, map[string]string{"ordinal": strconv.Itoa(attempt)}))
}

func commandAttributes(commands []string) map[string]string {
	attributes := make(map[string]string, len(commands))
	for index, command := range commands {
		attributes[strconv.Itoa(index)] = command
	}
	return attributes
}
