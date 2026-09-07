package agentadapter

import "sync"

// EffectiveAgent identifies who actually served a request: the binary, the
// model and the effort it ran with.
//
// It exists for H4/T0.2. The audit record stored the profile name (or
// "default"), not the agent that answered, so with `active_agent: auto` a
// verdict had no verifiable author: verified in the record of 6c079a8, which
// logged "default" after claude answered.
type EffectiveAgent struct {
	Binary string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Empty states there is no author to record. Leaving the field empty is
// preferred over inventing one: misattributing is the very defect T0.2
// corrects.
func (a EffectiveAgent) Empty() bool {
	return a.Binary == "" && a.Model == "" && a.Effort == ""
}

// ReportsEffectiveAgent is implemented by the adapters able to say who served
// their last request. It is optional: an adapter that does not implement it
// simply contributes no authorship.
type ReportsEffectiveAgent interface {
	EffectiveAgent() (EffectiveAgent, bool)
}

// ConfiguredModel returns the model selected by this adapter. A chain only
// knows the answer after one child has responded successfully.
func (c *CLIAdapter) ConfiguredModel() (string, bool) {
	return c.Config.Model, c.Config.Model != ""
}

// EffectiveAgent of a CLIAdapter is its own binary with the configuration it
// was built with: it either answers itself or nobody does.
func (c *CLIAdapter) EffectiveAgent() (EffectiveAgent, bool) {
	return EffectiveAgent{
		Binary: c.baseName(),
		Model:  c.Config.Model,
		Effort: c.Config.ReasoningEffort,
	}, true
}

// effectiveRegistry keeps the last child that answered inside a chain. It
// carries a mutex because the audit engine audits several dimensions in
// parallel and each one may retry with a clarification round.
type effectiveRegistry struct {
	mu        sync.Mutex
	effective EffectiveAgent
	set       bool
}

func (r *effectiveRegistry) record(a completeAdapter) {
	reporter, ok := a.(ReportsEffectiveAgent)
	if !ok {
		return
	}
	effective, ok := reporter.EffectiveAgent()
	if !ok || effective.Empty() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.effective, r.set = effective, true
}

func (r *effectiveRegistry) read() (EffectiveAgent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.effective, r.set
}

// EffectiveAgent returns the child that served the last successful request.
// It returns false while none has answered: the fallback is per request and
// never cached, so the author tracks the latest call.
func (c *AdapterChain) EffectiveAgent() (EffectiveAgent, bool) {
	return c.registry.read()
}

func (c *AdapterChain) ConfiguredModel() (string, bool) {
	effective, ok := c.EffectiveAgent()
	return effective.Model, ok && effective.Model != ""
}
