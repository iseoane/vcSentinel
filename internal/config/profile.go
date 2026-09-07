package config

import "strings"

// ResolveAgentProfile resolves the nested profile of ONE concrete agent:
// it reads cfg.Agents[agent].Profiles[profile] and falls back to the
// agent's base model and effort when the profile does not define them.
// Returns model and effort.
func ResolveAgentProfile(cfg Config, agent, profile string) (model, effort string) {
	a := cfg.Agents[agent]
	p := a.Profiles[profile]
	model = p.Model
	if model == "" {
		model = a.Model
	}
	effort = p.ReasoningEffort
	if effort == "" {
		effort = a.ReasoningEffort
	}
	return model, effort
}

// ResolvedProfile is the result of resolving which agent, model and effort
// a concrete audit dimension uses. Empty fields mean "inherit from the
// level below": Binary "" = active_agent (auto = the agents available in
// the yml configuration order, with chain fallback); Model/Effort "" =
// those of the chosen agent.
type ResolvedProfile struct {
	Name   string
	Binary string
	Model  string
	Effort string
}

// ResolveProfile resolves a provider profile. The caller supplies the
// provider-neutral default selected by the authoritative review contract;
// configuration owns provider selection, model, and effort only. The name
// accepts two syntaxes:
//
//   - "agent.profile" (v2): looks up agents.<agent>.profiles.<profile>; the
//     profile's model/effort, when present, override the agent's.
//   - "profile" (v1/compat): looks up profiles.<profile>; if it does not
//     exist, the active agent's nested profile with that name.
//
// The resulting profile may be undefined (an empty recipe), which is
// interpreted as inheriting everything from the chosen agent.
func ResolveProfile(cfg Config, defaultProfile, override string) ResolvedProfile {
	name := override
	if name == "" {
		name = defaultProfile
	}

	// v2: "agent.profile". The prefix only counts as an agent when it is
	// configured: a profile name that carries a dot by itself (e.g.
	// "gpt-4.1") would be split into a nonexistent binary and lose the
	// model/effort inheritance. When the prefix is not an agent, fall
	// through to the v1 path.
	if agent, profile, ok := strings.Cut(name, "."); ok {
		if _, exists := cfg.Agents[agent]; exists {
			model, effort := ResolveAgentProfile(cfg, agent, profile)
			return ResolvedProfile{Name: name, Binary: agent, Model: model, Effort: effort}
		}
	}

	// v1/compat: a global profile with its own agent, or the active agent's nested profile.
	p := cfg.Profiles[name]
	if p.Agent != "" {
		return ResolvedProfile{Name: name, Binary: p.Agent, Model: p.Model, Effort: p.ReasoningEffort}
	}
	agent := cfg.ActiveAgent
	if agent == "auto" || agent == "" {
		return ResolvedProfile{Name: name, Binary: "", Model: p.Model, Effort: p.ReasoningEffort}
	}
	a := cfg.Agents[agent]
	nested := a.Profiles[name]
	model := nested.Model
	if model == "" {
		model = a.Model
	}
	effort := nested.ReasoningEffort
	if effort == "" {
		effort = a.ReasoningEffort
	}
	return ResolvedProfile{Name: name, Binary: agent, Model: model, Effort: effort}
}
