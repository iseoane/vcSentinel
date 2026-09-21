package agentadapter

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

func NewAgentAdapter(worktreePath string) (AgentAdapter, error) {
	cfg := config.LoadLocalConfig(worktreePath)

	name := os.Getenv("MY_SUB_AGENT")
	if name == "" {
		name = cfg.ActiveAgent
	}

	if name != "auto" {
		if _, exists := cfg.Agents[name]; exists {
			return newAdapter(cfg, name, "")
		}
	}

	all := make([]string, 0, len(cfg.Agents))
	for key := range cfg.Agents {
		all = append(all, key)
	}
	sort.Strings(all)

	names := agentNamesInPATH(cfg)
	if len(names) == 0 {
		return nil, fmt.Errorf("no agent configured in vcsentinel.yml is available on the PATH: %s", strings.Join(all, ", "))
	}

	// Auto path: a chain with one adapter per available agent, in the yml's
	// configuration order (chained fallback per request).
	return buildChain(cfg, names, "")
}

// NewAgentAdapterForMessage builds the adapter to generate the commit
// messages of 'vcsentinel slice'. Same as NewAgentAdapter, but every agent
// resolves its model/effort with the nested "commit" profile (low reasoning)
// instead of the base model/effort: naming a commit does not need the same
// reasoning as the rest of the task. When the agent does not define the
// profile, ResolverProfileAgent falls back to the base model/effort and the
// config stays the same.
func NewAgentAdapterForMessage(worktreePath string) (AgentAdapter, error) {
	cfg := config.LoadLocalConfig(worktreePath)

	name := os.Getenv("MY_SUB_AGENT")
	if name == "" {
		name = cfg.ActiveAgent
	}

	if name != "auto" {
		if _, exists := cfg.Agents[name]; exists {
			return newAdapter(cfg, name, "commit")
		}
	}

	all := make([]string, 0, len(cfg.Agents))
	for key := range cfg.Agents {
		all = append(all, key)
	}
	sort.Strings(all)

	names := agentNamesInPATH(cfg)
	if len(names) == 0 {
		return nil, fmt.Errorf("no agent configured in vcsentinel.yml is available on the PATH: %s", strings.Join(all, ", "))
	}

	// Auto path: each adapter of the chain resolves ITS commit profile.
	return buildChain(cfg, names, "commit")
}

// NewAgentAdapterNamed builds a CLI adapter for an explicit agent name,
// without automatic resolution. It returns an error if the name is not
// configured in vcsentinel.yml.
func NewAgentAdapterNamed(worktreePath string, name string) (AgentAdapter, error) {
	cfg := config.LoadLocalConfig(worktreePath)
	return newAdapter(cfg, name, "")
}

// NewAgentAdapterNamedForMessage builds a CLI adapter for an explicit agent
// name, with the "commit" profile for the slice messages (applies low
// reasoning and falls back to the agent's base model if the profile does not
// exist). It returns an error if the name is not configured in
// vcsentinel.yml.
func NewAgentAdapterNamedForMessage(worktreePath string, name string) (AgentAdapter, error) {
	cfg := config.LoadLocalConfig(worktreePath)
	return newAdapter(cfg, name, "commit")
}

// newAdapter builds the adapter of a concrete agent, resolving the binary and
// the model/effort configuration. If profile is non-empty, the configuration
// is resolved with the agent's nested profile (with fallback to the base
// model/effort when the profile does not define them); if profile is empty,
// the agent's base configuration is used as is (compatibility with
// NewAgentAdapter/NewAgentAdapterNamed). The adapter family is decided by the
// kind declared by the entry (see buildAgentAdapter).
func newAdapter(cfg config.Config, name, profile string) (AgentAdapter, error) {
	return buildAgentAdapter(cfg, name, profile, 0)
}

// buildAgentAdapter builds the adapter matching the family declared by the
// agents.<name> entry: CLIAdapter for an empty kind (the historical path,
// byte-identical), AcpxBridge over acpadapter.AcpxAdapter for kind "acpx"
// (ticket 16), and an explicit construction error for any other value — fail
// fast before launching anything. timeout is the per-call budget (0 = each
// family's defaults).
func buildAgentAdapter(cfg config.Config, name, profile string, timeout time.Duration) (AgentAdapter, error) {
	if _, exists := cfg.Agents[name]; !exists {
		return nil, fmt.Errorf("agent %q is not configured in vcsentinel.yml", name)
	}
	return buildAdapterFamily(cfg, name, resolveAgentConfig(cfg, name, profile), timeout)
}

// ProbeAdapterFor builds the prompt-capable adapter for one configured agent
// entry with the given per-call budget, selecting the family its kind
// declares through the single family switch. Doctor is its only reader: a
// short budget keeps the preflight fast while still sending a real prompt.
func ProbeAdapterFor(cfg config.Config, name string, timeout time.Duration) (PromptAdapter, error) {
	ad, err := buildAgentAdapter(cfg, name, "", timeout)
	if err != nil {
		return nil, err
	}
	prompt, ok := ad.(PromptAdapter)
	if !ok {
		return nil, fmt.Errorf("agent %q: adapter %T cannot answer prompts", name, ad)
	}
	return prompt, nil
}

// buildAdapterFamily is the ONLY place that selects the adapter family for one
// agent entry: every factory path (named profile, auto chain, resolved
// profile chain, review-profile resolution) converges here, so introducing a
// third kind means touching exactly one switch instead of hunting call sites.
// The follow-up "Consolidate adapter-family dispatch" landed precisely to
// enforce this invariant.
func buildAdapterFamily(cfg config.Config, name string, resolved config.AgentConfig, timeout time.Duration) (AgentAdapter, error) {
	switch cfg.Agents[name].Kind {
	case config.AgentKindCLI:
		return &CLIAdapter{
			BinaryName:     resolveRealBinary(name),
			Config:         resolved,
			CommitLanguage: cfg.CommitLanguage,
			Timeout:        timeout,
		}, nil
	case config.AgentKindACPX:
		bridge, err := buildACPXAdapter(cfg, name, resolved, timeout)
		if err != nil {
			return nil, err
		}
		return bridge, nil
	default:
		return nil, fmt.Errorf("agent %q declares unknown kind %q (valid values: empty for the CLI family, %q for ACP/acpx)", name, cfg.Agents[name].Kind, config.AgentKindACPX)
	}
}

// buildACPXAdapter builds the ACP-backed adapter from the agent entry:
// launcher default ["npx","-y","acpx@latest"], configured model/effort echo,
// childEnv passthrough unchanged, and the C6 enforcement declaration validated
// at construction time so an unsatisfiable or unknown value fails before any
// launch. The timeout maps to the acpx --timeout budget; zero keeps the
// adapter default.
func buildACPXAdapter(cfg config.Config, name string, resolved config.AgentConfig, timeout time.Duration) (*AcpxBridge, error) {
	agent := cfg.Agents[name]
	inner, err := acpadapter.NewAcpx(acpadapter.Config{
		Agent:             agent.ACPAgent,
		Model:             resolved.Model,
		Effort:            resolved.ReasoningEffort,
		Enforcement:       agent.Enforcement,
		MaxRuntimeSeconds: int(timeout / time.Second),
	})
	if err != nil {
		return nil, fmt.Errorf("agent %q (kind: acpx): %w", name, err)
	}
	return &AcpxBridge{AcpxAdapter: inner, commitLanguage: cfg.CommitLanguage}, nil
}

// buildChain creates the auto path's adapter chain: one adapter per available
// agent, in the received order, each with its own configuration (its commit
// profile if profile is non-empty) and ITS declared family. It returns an
// error if any entry cannot be built.
func buildChain(cfg config.Config, available []string, profile string) (*AdapterChain, error) {
	chain := &AdapterChain{}
	for _, agent := range available {
		ad, err := buildAgentAdapter(cfg, agent, profile, 0)
		if err != nil {
			return nil, err
		}
		complete, ok := ad.(completeAdapter)
		if !ok {
			return nil, fmt.Errorf("agent %q: adapter %T cannot join the fallback chain", agent, ad)
		}
		chain.adapters = append(chain.adapters, complete)
	}
	return chain, nil
}

// resolveAgentConfig resolves an agent's model/effort configuration. If
// profile is "" it returns the agent's base config; otherwise it applies
// config.ResolveAgentProfile (commit profile with fallback to the agent).
func resolveAgentConfig(cfg config.Config, name, profile string) config.AgentConfig {
	if profile == "" {
		return cfg.Agents[name]
	}
	model, effort := config.ResolveAgentProfile(cfg, name, profile)
	return config.AgentConfig{Model: model, ReasoningEffort: effort}
}

// AvailableAdapterNames returns the names of the agents configured in
// vcsentinel.yml whose binary is available on the PATH, in the order they
// appear in the yml (alphabetical when no order is declared).
func AvailableAdapterNames(worktreePath string) []string {
	cfg := config.LoadLocalConfig(worktreePath)
	return agentNamesInPATH(cfg)
}

// NewAdapterWithProfile builds the adapter matching a resolved profile: it
// resolves the binary (profile -> active_agent -> auto), completes the model
// and effort with the agent's when the profile does not define them, and sets
// the audit timeout. On the auto path it returns an AdapterChain with one
// adapter per available agent, each with ITS agent's profile.
func NewAdapterWithProfile(cfg config.Config, profile config.ResolvedProfile) (PromptAdapter, error) {
	name := profile.Binary
	if name == "" {
		name = cfg.ActiveAgent
	}
	if name == "" || name == "auto" {
		available := agentNamesInPATH(cfg)
		if len(available) == 0 {
			return nil, fmt.Errorf("no agent configured in vcsentinel.yml is available on the PATH")
		}
		return buildChainProfile(cfg, available, profile)
	}
	agent := cfg.Agents[name]
	model := profile.Model
	if model == "" {
		model = agent.Model
	}
	effort := profile.Effort
	if effort == "" {
		effort = agent.ReasoningEffort
	}

	// The launcher (npx) does not take part in the binary's shim resolution;
	// the model/effort inherit the same profile->agent merge as the CLI path
	// and the audit timeout maps to the acpx --timeout budget. The family is
	// decided exclusively by buildAdapterFamily.
	ad, err := buildAdapterFamily(cfg, name, config.AgentConfig{Model: model, ReasoningEffort: effort}, cfg.Review.Timeout)
	if err != nil {
		return nil, err
	}
	prompt, ok := ad.(PromptAdapter)
	if !ok {
		return nil, fmt.Errorf("agent %q: adapter %T cannot serve arbitrary prompts", name, ad)
	}
	return prompt, nil
}

// buildChainProfile creates the auto path's adapter chain: one adapter per
// available agent (in the received order), each with the model/effort of ITS
// nested profile, not the resolved profile's, and ITS declared family. It
// returns an error if any entry cannot be built.
func buildChainProfile(cfg config.Config, available []string, profile config.ResolvedProfile) (*AdapterChain, error) {
	chain := &AdapterChain{}
	for _, agent := range available {
		model, effort := config.ResolveAgentProfile(cfg, agent, profile.Name)
		ad, err := buildAdapterFamily(cfg, agent, config.AgentConfig{Model: model, ReasoningEffort: effort}, cfg.Review.Timeout)
		if err != nil {
			return nil, err
		}
		complete, ok := ad.(completeAdapter)
		if !ok {
			return nil, fmt.Errorf("agent %q: adapter %T cannot join the fallback chain", agent, ad)
		}
		chain.adapters = append(chain.adapters, complete)
	}
	return chain, nil
}

// resolveRealBinary converts an npm shim (.cmd/.bat) into the path of the real
// binary it points to. Running .cmd shims with exec.Command uses cmd.exe and
// breaks the quoting of long prompts (line limit + quotes), so the target
// .exe is extracted from the shim's "node_modules\...\bin\X.exe" line. If
// there is no shim or it cannot be resolved, the name is returned as is.
func resolveRealBinary(name string) string {
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return name
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".cmd" && ext != ".bat" {
		return path
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return path
	}
	// npm shims use "%dp0%\node_modules\<package>\bin\<binary>.exe" (the
	// variable points at the shim's own directory); some use "%~dp0".
	pattern := regexp.MustCompile(`([A-Za-z]:\\[^"\r\n]*node_modules[^"\r\n]*\.exe|(?:%dp0%|%~dp0)\\[^"\r\n]*node_modules[^"\r\n]*\.exe)`)
	match := pattern.FindString(string(data))
	if match == "" {
		return path
	}
	// npm's .cmd uses %dp0% (the shim's directory): the extracted path is
	// relative to that directory, not to the process CWD.
	if strings.HasPrefix(match, "%dp0%") {
		return filepath.Join(filepath.Dir(path), match[len("%dp0%"):])
	}
	if strings.HasPrefix(match, "%~dp0") {
		return filepath.Join(filepath.Dir(path), match[len("%~dp0"):])
	}
	return match
}

// agentNamesInPATH filters the configured agents that exist on the PATH. If
// cfg.AgentOrder has entries, that order is kept; if it is empty (for
// example, a hand-built Config in tests) it falls back to alphabetical order.
// It is the logic shared by NewAgentAdapter's automatic resolution, by
// NewAdapterWithProfile and by AvailableAdapterNames.
func agentNamesInPATH(cfg config.Config) []string {
	order := cfg.AgentOrder
	if len(order) == 0 {
		order = make([]string, 0, len(cfg.Agents))
		for key := range cfg.Agents {
			order = append(order, key)
		}
		sort.Strings(order)
	}
	names := make([]string, 0, len(order))
	for _, key := range order {
		if _, err := exec.LookPath(key); err == nil {
			names = append(names, key)
		}
	}
	return names
}
