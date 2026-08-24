package acpadapter

import "path/filepath"

// EffectiveAgent identifies who actually served a request: the binary, the
// model, and the reasoning effort the run used. Its JSON shape mirrors
// internal/agentadapter.AgenteEfectivo (tags agent/model/effort) so upper
// layers can persist both adapter kinds identically, while keeping every ACP
// concept inside this package boundary.
//
// Honest-attribution rule (C1/C8): Model is only reported when observed on
// the wire or explicitly configured; it is never invented. Effort is the
// configured echo verbatim or empty.
type EffectiveAgent struct {
	Binary string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Empty reports that there is no attributable identity. Leaving fields empty
// is preferred over fabricating attribution.
func (a EffectiveAgent) Empty() bool {
	return a.Binary == "" && a.Model == "" && a.Effort == ""
}

// EffectiveIdentity composes the launcher-resolved base name with the agent
// token for Binary, prefers the model observed on the last run's initialize
// result over the configured echo, and echoes Effort verbatim or empty.
func (a *AcpxAdapter) EffectiveIdentity() EffectiveAgent {
	model := a.model
	if observed, ok := a.observedModel(); ok && observed != "" {
		model = observed
	}
	return EffectiveAgent{
		Binary: filepath.Base(a.launcher[0]) + ":" + a.agent,
		Model:  model,
		Effort: a.effort,
	}
}
