// Package planning builds deterministic execution plans without side effects.
package planning

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ISeoane-Quental/vcSentinel/internal/change"
	"github.com/ISeoane-Quental/vcSentinel/internal/risk"
)

// Stage identifies the lifecycle point for which a plan is built.
type Stage string

const (
	StagePreCommit Stage = "pre-commit"
	StagePrePush   Stage = "pre-push"
	StagePR        Stage = "pr"
)

// CodeModel is the serializable planning view of graph analysis.
type CodeModel struct {
	SnapshotID string   `json:"snapshot_id"`
	Paths      []string `json:"paths,omitempty"`
	Packages   []string `json:"packages,omitempty"`
	Tests      []string `json:"tests,omitempty"`
	Complete   bool     `json:"complete"`
}

// Policy contains stable policy inputs. Later tasks may interpret capabilities.
type Policy struct {
	Name         string            `json:"name"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Options      map[string]string `json:"options,omitempty"`
}

// ExecutionPlan is a deterministic, serializable record of a planning decision.
type ExecutionPlan struct {
	PlanID        string               `json:"plan_id"`
	ChangeProfile change.ChangeProfile `json:"change_profile"`
	RiskProfile   risk.Result          `json:"risk_profile"`
	CodeModel     CodeModel            `json:"code_model"`
	Policy        Policy               `json:"policy"`
	Stage         Stage                `json:"stage"`
	Explain       string               `json:"explain"`
}

type canonicalInputs struct {
	ChangeProfile change.ChangeProfile `json:"change_profile"`
	RiskProfile   risk.Result          `json:"risk_profile"`
	CodeModel     CodeModel            `json:"code_model"`
	Policy        Policy               `json:"policy"`
	Stage         Stage                `json:"stage"`
}

// Plan returns the same plan for semantically equivalent inputs.
func Plan(profile change.ChangeProfile, riskProfile risk.Result, model CodeModel, policy Policy, stage Stage) ExecutionPlan {
	inputs := canonicalInputs{
		ChangeProfile: canonicalChangeProfile(profile),
		RiskProfile:   riskProfile,
		CodeModel:     canonicalCodeModel(model),
		Policy:        canonicalPolicy(policy),
		Stage:         stage,
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		panic(fmt.Sprintf("planning inputs cannot be serialized: %v", err))
	}
	digest := sha256.Sum256(encoded)
	return ExecutionPlan{
		PlanID: hex.EncodeToString(digest[:]), ChangeProfile: inputs.ChangeProfile,
		RiskProfile: inputs.RiskProfile, CodeModel: inputs.CodeModel,
		Policy: inputs.Policy, Stage: inputs.Stage,
		Explain: fmt.Sprintf("Plan for stage %s uses policy %q at risk %s: %s.", stage, policy.Name, riskProfile.Level, riskProfile.Explanation),
	}
}

func canonicalChangeProfile(profile change.ChangeProfile) change.ChangeProfile {
	profile.Modules = sortedCopy(profile.Modules)
	if len(profile.FileClasses) > 0 {
		profile.FileClasses = cloneMap(profile.FileClasses)
	} else {
		profile.FileClasses = nil
	}
	return profile
}

func canonicalCodeModel(model CodeModel) CodeModel {
	model.Paths = sortedCopy(model.Paths)
	model.Packages = sortedCopy(model.Packages)
	model.Tests = sortedCopy(model.Tests)
	return model
}

func canonicalPolicy(policy Policy) Policy {
	policy.Capabilities = sortedCopy(policy.Capabilities)
	if len(policy.Options) > 0 {
		policy.Options = cloneMap(policy.Options)
	} else {
		policy.Options = nil
	}
	return policy
}

func sortedCopy(values []string) []string {
	copyOfValues := append([]string(nil), values...)
	sort.Strings(copyOfValues)
	return copyOfValues
}

func cloneMap[V any](values map[string]V) map[string]V {
	copyOfValues := make(map[string]V, len(values))
	for key, value := range values {
		copyOfValues[key] = value
	}
	return copyOfValues
}
