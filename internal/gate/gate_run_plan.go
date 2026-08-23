// Gate run-plan machinery for the durable-runs roadmap (R9 slice 1): it
// decomposes ONE gate execution into ONE root run request plus logical job
// descriptors, without executing anything. This file is pure construction
// logic, fully unit-testable without processes or agents, and it routes
// nothing by itself: activation stays behind the construction-time switch in
// EjecutarGate (see gate.go), whose default keeps the legacy orchestration as
// the only active path.
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

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// GateJobKind distinguishes the two logical-job layers under one gate root
// run: deterministic validation commands and the semantic review phase.
// Terminal classes remain distinct per layer downstream; this vocabulary only
// names which layer a logical job belongs to.
type GateJobKind string

const (
	GateJobValidation GateJobKind = "validation"
	GateJobReview     GateJobKind = "review"
)

// Canonical prompt domains and capability names feeding agentrun identity
// derivation. They are stable contract strings: changing any of them changes
// every derived plan identity.
const (
	promptDomainRoot       = "gate.root"
	promptDomainValidation = "gate.validation"
	promptDomainReview     = "gate.review"

	capabilityNameCommands = "gate.validation.commands"
	capabilityNameCommand  = "gate.validation.command"
	capabilityNameReview   = "gate.review.phase"
)

// GateJobPlan is one logical job descriptor under the gate root run. The
// empty Command belongs to the review job; every validation job carries its
// exact command string in deterministic profile order.
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

// ReviewJob returns the single semantic-review logical-job descriptor.
func (p GateRunPlan) ReviewJob() GateJobPlan {
	for _, job := range p.Jobs {
		if job.Kind == GateJobReview {
			return job
		}
	}
	return GateJobPlan{}
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
		[]agentrun.Capability{agentrun.NewCapability(capabilityNameCommands, commandAttributes(commands))},
	))

	jobs := make([]GateJobPlan, 0, len(commands)+1)
	for index, command := range commands {
		position := strconv.Itoa(index)
		jobs = append(jobs, GateJobPlan{
			Kind:    GateJobValidation,
			Command: command,
			Job: agentrun.NewLogicalJob(agentrun.NewRunRequest(
				agentrun.Candidate(candidateSHA),
				agentrun.Prompt(canonicalPrompt(promptDomainValidation, stage, profile, position)),
				[]agentrun.Capability{agentrun.NewCapability(capabilityNameCommand, map[string]string{
					"profile":  profile,
					"position": position,
					"command":  command,
				})},
			)),
		})
	}
	jobs = append(jobs, GateJobPlan{
		Kind: GateJobReview,
		Job: agentrun.NewLogicalJob(agentrun.NewRunRequest(
			agentrun.Candidate(candidateSHA),
			agentrun.Prompt(canonicalPrompt(promptDomainReview, stage, profile)),
			[]agentrun.Capability{agentrun.NewCapability(capabilityNameReview, map[string]string{
				"profile": profile,
			})},
		)),
	})

	return GateRunPlan{
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
// resolution stays a runtime concern of the legacy executor; planning pins
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

func commandAttributes(commands []string) map[string]string {
	attributes := make(map[string]string, len(commands))
	for index, command := range commands {
		attributes[strconv.Itoa(index)] = command
	}
	return attributes
}
