package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ISeoane-Quental/vcSentinel/internal/change"
)

// Agent adapter families an agent entry may declare through its kind key.
// The empty value is the historical CLI family: absence of kind keeps every
// existing vassentinel.yml byte-identical in behavior.
const (
	AgentKindCLI  = ""
	AgentKindACPX = "acpx"
)

// Enforcement values an agent entry may declare. They mirror the admission
// vocabulary of internal/acpadapter (C6); config only carries the string,
// the adapter family validates it at construction time.
const (
	EnforcementNone          = "none"
	EnforcementClaudeSandbox = "claude-sandbox"
)

// AgentConfig defines the model and default effort of an agent (binary)
// and the nested profiles that agent offers (schema v2).
type AgentConfig struct {
	Model           string
	ReasoningEffort string
	Profiles        map[string]ProfileConfig
	// Kind selects the adapter family: "" (CLI, default) or "acpx". Any
	// other value fails construction with an explicit error.
	Kind string
	// ACPAgent is the acpx agent token (claude|codex|opencode|custom) when
	// Kind is "acpx"; it is required in that case and ignored otherwise.
	ACPAgent string
	// Enforcement declares the restriction backend for acpx agents
	// ("none" default; "claude-sandbox"). Validated at construction.
	Enforcement string
}

// ProfileConfig is a named recipe: agent + model + effort. The agent is
// optional: if empty, the active binary (active_agent) is used.
type ProfileConfig struct {
	Agent           string
	Model           string
	ReasoningEffort string
}

// ReviewConfig groups the audit engine configuration.
type ReviewConfig struct {
	Timeout          time.Duration
	Parallel         int
	CodeGraphContext bool
	// EvidenceAdmission enables evidence admission over durable transport
	// output (ticket 07): snapshot binding and output-hash verification run
	// before a completion may influence verdicts, and admission failures are
	// surfaced as first-class evidence. It defaults to true (cutover
	// default-on); setting it false restores the pre-R6 observe-but-admit
	// lenient behavior while runs stay inspectable via `sentinel runs`.
	EvidenceAdmission bool
	// CancellationEscalation enables bounded whole-tree escalation after the
	// cooperative grace budget expires when a routed review owns its provider
	// process tree (ticket 08). It defaults to true; setting it false keeps
	// cooperative cancellation and orphan detection while never issuing a
	// kill signal beyond the direct child.
	CancellationEscalation bool
}

// Possible values of CapabilityConfig.FailsWhen: when a validation
// capability is considered failed. exit_code is the historical default (the
// same criterion already used by lint_commands/test_commands/build_commands).
const (
	FailsWhenExitCode       = "exit_code"
	FailsWhenOutputNotEmpty = "output_not_empty"
)

// Possible values of ValidationConfig.Mode: where validation
// capabilities run.
const (
	ModeWorktree = "worktree"
	ModeInplace  = "inplace"
)

// packagesMarker is the literal marker every scoped_command must
// contain: the executor (outside the scope of this task) substitutes it
// with the packages the validation is scoped to.
const packagesMarker = "{packages}"

// CapabilityConfig describes a user-configurable validation check (T1.2):
// command to run, failure criterion and, optionally, a variant scoped to a
// subset of packages.
type CapabilityConfig struct {
	Command   string
	FailsWhen string
	// SupportsScope and ScopedCommand enable a variant of the command
	// scoped to the affected packages (e.g. after a partial slice).
	SupportsScope bool
	ScopedCommand string
	// Timeout in seconds; 0 means "no explicit timeout of its own", the
	// executor decides its default.
	Timeout int
}

// ValidationConfig groups the capabilities configurable by the user, the
// profiles that combine them by name and the execution mode (T1.2).
type ValidationConfig struct {
	Capabilities map[string]CapabilityConfig
	// Profiles assigns a validation profile name to the ordered list of
	// capabilities it groups (they must exist in Capabilities).
	Profiles map[string][]string
	Mode     string
}

// Config is the complete configuration of VAS Sentinel with precedence
// defaults -> global -> per-project.
// CIConfig controls the optional GitHub Actions evidence collected by
// `sentinel pr create`. An empty Workflow deliberately disables CI; the
// command never guesses a workflow from detected CI files.
type CIConfig struct {
	Workflow    string
	WaitSeconds int
	PollSeconds int
}

type Config struct {
	ActiveAgent string
	Agents      map[string]AgentConfig
	// AgentOrder preserves the declaration order of the agents in the yaml
	// (the most specific file wins); it feeds automatic resolution.
	AgentOrder []string
	Profiles   map[string]ProfileConfig
	Review     ReviewConfig
	CI         CIConfig
	Validation ValidationConfig
	// Change holds the change.classes rules (T3.1) consumed by
	// change.ClassifyByPath: the order IS the precedence. No wrapper
	// struct because it groups nothing else (T3.1 review).
	Change []change.Rule
	// CommitLanguage fixes the language of the commit messages the agent
	// generates (T0.13). Defaults to English.
	CommitLanguage string
	// RequestExternalAgentDiff lets the repository request external
	// semantic generation. It does not represent personal consent.
	RequestExternalAgentDiff bool
	LintCommands             []string
	TestCommands             []string
	BuildCommands            []string
}

func defaultConfig() Config {
	return Config{
		ActiveAgent: "auto",
		// "en" and not agentadapter.DefaultLanguage: agentadapter already
		// imports config, so referencing it here would create a cycle. The
		// agentadapter tests pin that both values match.
		CommitLanguage:           "en",
		RequestExternalAgentDiff: false,
		Agents: map[string]AgentConfig{
			"claude": {
				Model:           "claude-5-sonnet",
				ReasoningEffort: "high",
				Profiles: map[string]ProfileConfig{
					"cheap":  {Model: "claude-5-sonnet", ReasoningEffort: "low"},
					"normal": {Model: "claude-5-sonnet", ReasoningEffort: "high"},
					"deep":   {Model: "claude-opus", ReasoningEffort: "high"},
				},
			},
			"opencode": {
				Model:           "deepseek-v4-flash-free",
				ReasoningEffort: "max",
				Profiles: map[string]ProfileConfig{
					"cheap":  {Model: "deepseek-v4-flash-free", ReasoningEffort: "default"},
					"normal": {Model: "deepseek-v4-flash-free", ReasoningEffort: "high"},
					"deep":   {Model: "deepseek-v4-flash-free", ReasoningEffort: "max"},
				},
			},
		},
		AgentOrder: []string{"claude", "opencode"},
		Profiles:   map[string]ProfileConfig{}, // compat v1: global profiles
		Review: ReviewConfig{
			Timeout:  900 * time.Second,
			Parallel: 2,
			// Evidence admission is default-on at cutover (ticket 07): the
			// rollback seam is setting it to false, never leaving it unset.
			EvidenceAdmission: true,
			// Bounded cancellation escalation is default-on (ticket 08): the
			// rollback seam is setting it to false, which restricts every
			// kill to the direct child.
			CancellationEscalation: true,
		},
		CI: CIConfig{
			WaitSeconds: 900,
			PollSeconds: 15,
		},
		Validation: ValidationConfig{
			Capabilities: map[string]CapabilityConfig{},
			Profiles:     map[string][]string{},
			Mode:         ModeWorktree,
		},
		LintCommands:  []string{},
		TestCommands:  []string{},
		BuildCommands: []string{},
		Change:        change.DefaultRules(),
	}
}

// globalConfigPath returns the path of the global configuration file,
// located next to the VAS Sentinel base directory in the user's home.
func globalConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".vas_sentinel", "vassentinel.yml"), nil
}

// perProjectConfigPath returns the path of the per-project configuration
// file, located in the .vas_sentinel folder of the worktree root,
// keeping coherence with the user's global directory.
func perProjectConfigPath(worktreePath string) string {
	return filepath.Join(worktreePath, ".vas_sentinel", "vassentinel.yml")
}

// LoadLocalConfig loads the configuration following the
// precedence: defaults -> global (~/.vas_sentinel/vassentinel.yml) ->
// per-project (<worktree>/.vas_sentinel/vassentinel.yml). Per-project
// wins and overwrites only the fields it defines.
func LoadLocalConfig(worktreePath string) Config {
	cfg := defaultConfig()

	// LoadLocalConfig keeps its error-less signature (same
	// contract as today for its callers); the strict error from
	// applyFromPath stays available for whoever invokes it directly
	// (see tests), with how to make it visible to the CLI operator to be
	// decided in another task.
	if path, err := globalConfigPath(); err == nil {
		_ = applyFromPath(&cfg, path)
	}

	_ = applyFromPath(&cfg, perProjectConfigPath(worktreePath))

	translateLegacyCommandsToCapabilities(&cfg)

	return cfg
}

// LoadStrictLocalConfig loads the configuration with the same
// precedence as LoadLocalConfig (defaults -> global ->
// per-project) but WITHOUT silently discarding the applyFromPath error:
// an unknown key in the yml (global or per-project) propagates with
// file and line (T1.1), instead of being ignored.
//
// Requirement added by the orchestrator for the F1 phase exit criterion #4
// ("an unknown key in vassentinel.yml produces an explicit error with the
// line, not silence"): 'gate' (T1.7) is the first entry point where this
// must be visible, being the new consolidated command.
// LoadLocalConfig does NOT change (same contract without error to
// avoid breaking its current callers); this function is the strict variant
// for whoever can propagate the error to the operator.
func LoadStrictLocalConfig(worktreePath string) (Config, error) {
	cfg := defaultConfig()

	if path, err := globalConfigPath(); err == nil {
		if err := applyFromPath(&cfg, path); err != nil {
			return Config{}, err
		}
	}

	if err := applyFromPath(&cfg, perProjectConfigPath(worktreePath)); err != nil {
		return Config{}, err
	}

	translateLegacyCommandsToCapabilities(&cfg)

	return cfg, nil
}

// agentProfileYAML is a profile nested inside an agent
// (agents.<agent>.profiles.<profile>, schema v2): only
// model/reasoning_effort, the agent comes from the outer key.
type agentProfileYAML struct {
	Model           *string `yaml:"model"`
	ReasoningEffort *string `yaml:"reasoning_effort"`
}

// globalProfileYAML is a top-level profile (profiles section, v1 compat):
// agent is optional; if missing, the active_agent is used.
type globalProfileYAML struct {
	Agent           *string `yaml:"agent"`
	Model           *string `yaml:"model"`
	ReasoningEffort *string `yaml:"reasoning_effort"`
}

// agentYAML is an agent entry in agents.<name>.
type agentYAML struct {
	Model           *string                     `yaml:"model"`
	ReasoningEffort *string                     `yaml:"reasoning_effort"`
	Profiles        map[string]agentProfileYAML `yaml:"profiles"`
	// Kind selects the adapter family ("" CLI by default, "acpx" for the
	// ACP/acpx adapter). Agent and Enforcement only apply to the acpx
	// family (ticket 16).
	Kind        *string `yaml:"kind"`
	Agent       *string `yaml:"agent"`
	Enforcement *string `yaml:"enforcement"`
}

// reviewYAML is the review section. Timeout/Parallel are decoded as
// yaml.Node (not int directly) to preserve the historical tolerance to
// non-numeric values (they are ignored and the default remains), the same
// way the handcrafted parser's strconv.Atoi worked.
type reviewYAML struct {
	Timeout                yaml.Node `yaml:"timeout"`
	Parallel               yaml.Node `yaml:"parallel"`
	CodeGraphContext       *bool     `yaml:"codegraph_context"`
	EvidenceAdmission      *bool     `yaml:"evidence_admission"`
	CancellationEscalation *bool     `yaml:"cancellation_escalation"`
}

type ciYAML struct {
	Workflow    *string   `yaml:"workflow"`
	WaitSeconds yaml.Node `yaml:"wait_seconds"`
	PollSeconds yaml.Node `yaml:"poll_seconds"`
}

// capabilityYAML is an entry of validation.capabilities.<name> (T1.2).
// The capability name is a free label chosen by the user (not a closed
// enum in Go); the only fixed thing is this shape. Command is required in
// practice (without it the capability runs nothing), but this task only
// requires the three shape validations listed in the design: whoever
// declares a capability without command is left with the empty string,
// without failing the load.
type capabilityYAML struct {
	Command       *string `yaml:"command"`
	FailsWhen     *string `yaml:"fails_when"`
	SupportsScope *bool   `yaml:"supports_scope"`
	ScopedCommand *string `yaml:"scoped_command"`
	Timeout       *int    `yaml:"timeout"`
}

// validationYAML is the complete validation section (T1.2): capabilities
// configurable by the user, profiles grouping them by name and execution
// mode.
type validationYAML struct {
	Capabilities map[string]capabilityYAML `yaml:"capabilities"`
	Profiles     map[string][]string       `yaml:"profiles"`
	Mode         *string                   `yaml:"mode"`
}

// changeYAML is the change.classes section: each class mapped to its glob
// list. The map does not preserve textual order; applyClassOrder
// recovers it.
type changeYAML struct {
	Classes map[string][]string `yaml:"classes"`
}

// configYAML is the full schema exactly as yaml.v3 consumes it with
// KnownFields(true): a key outside this list (e.g. "comand" instead of
// "command") makes decoding fail with file and line, instead of being
// silently ignored as in the previous handcrafted parser.
//
// Version has no equivalent field in Config: it is not used in any
// computation today, but the real ymls of this repo declare it (see
// .vas_sentinel/vassentinel.yml), so it must be accepted to avoid breaking
// the strict decoding of existing configuration. Adding that section to
// Config is another task.
//
// Ticket 13 (R11): review.durable_runs and the whole gate section were
// removed together with their legacy execution paths. A yaml still declaring
// them is not ignored: KnownFields(true) rejects it right here with file and
// line, naming the unknown key ("durable_runs" / "gate").
type configYAML struct {
	Version                  *string                      `yaml:"version"`
	ActiveAgent              *string                      `yaml:"active_agent"`
	Agents                   map[string]agentYAML         `yaml:"agents"`
	Profiles                 map[string]globalProfileYAML `yaml:"profiles"`
	Review                   *reviewYAML                  `yaml:"review"`
	CI                       *ciYAML                      `yaml:"ci"`
	Validation               *validationYAML              `yaml:"validation"`
	CommitLanguage           *string                      `yaml:"commit_language"`
	RequestExternalAgentDiff *bool                        `yaml:"request_external_agent_diff"`
	LintCommands             []string                     `yaml:"lint_commands"`
	TestCommands             []string                     `yaml:"test_commands"`
	BuildCommands            []string                     `yaml:"build_commands"`
	Change                   *changeYAML                  `yaml:"change"`
}

// agentOrderYAML is decoded WITHOUT KnownFields, only to read the
// textual order of the "agents" keys through its yaml.Node: Go maps do
// not preserve declaration order, so this is the only point where it can
// be recovered. The strict validation of those same keys was already done
// by the configYAML decode before reaching here.
type agentOrderYAML struct {
	Agents yaml.Node `yaml:"agents"`
}

// applyFromPath decodes the file at path (if it exists) with strict
// key validation and applies the present fields over cfg, overwriting
// only those. It returns an error (with file and line) when the file
// exists but has a key outside the schema or is malformed.
func applyFromPath(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		// Missing file: global and per-project are optional.
		return nil
	}

	var raw configYAML
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&raw); err != nil {
		if err == io.EOF {
			return nil // empty file: nothing to apply.
		}
		return fmt.Errorf("%s: %w", path, err)
	}

	if err := applyYAMLValues(cfg, &raw); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	var order agentOrderYAML
	if err := yaml.Unmarshal(data, &order); err == nil {
		applyAgentOrder(cfg, keysInOrder(&order.Agents))
	}

	if raw.Change != nil {
		applyClassOrder(cfg, data, raw.Change.Classes)
	}

	return nil
}

// applyYAMLValues applies over cfg the fields present in raw, field by
// field (only overwrites what the file declares explicitly).
// It returns an error when validation.capabilities/profiles do not
// respect the required shape (see applyValidation): unlike the rest of
// the sections, here an invalid value cannot be silently ignored.
func applyYAMLValues(cfg *Config, raw *configYAML) error {
	if raw.ActiveAgent != nil {
		cfg.ActiveAgent = *raw.ActiveAgent
	}
	if raw.CommitLanguage != nil {
		cfg.CommitLanguage = *raw.CommitLanguage
	}
	if raw.RequestExternalAgentDiff != nil {
		cfg.RequestExternalAgentDiff = *raw.RequestExternalAgentDiff
	}
	for name, agentRaw := range raw.Agents {
		agent := cfg.Agents[name]
		if agentRaw.Model != nil {
			agent.Model = *agentRaw.Model
		}
		if agentRaw.ReasoningEffort != nil {
			agent.ReasoningEffort = *agentRaw.ReasoningEffort
		}
		if agentRaw.Kind != nil {
			agent.Kind = *agentRaw.Kind
		}
		if agentRaw.Agent != nil {
			agent.ACPAgent = *agentRaw.Agent
		}
		if agentRaw.Enforcement != nil {
			agent.Enforcement = *agentRaw.Enforcement
		}
		for profileName, profileRaw := range agentRaw.Profiles {
			if agent.Profiles == nil {
				agent.Profiles = map[string]ProfileConfig{}
			}
			profile := agent.Profiles[profileName]
			if profileRaw.Model != nil {
				profile.Model = *profileRaw.Model
			}
			if profileRaw.ReasoningEffort != nil {
				profile.ReasoningEffort = *profileRaw.ReasoningEffort
			}
			agent.Profiles[profileName] = profile
		}
		cfg.Agents[name] = agent
	}
	for name, profileRaw := range raw.Profiles {
		profile := cfg.Profiles[name]
		if profileRaw.Agent != nil {
			profile.Agent = *profileRaw.Agent
		}
		if profileRaw.Model != nil {
			profile.Model = *profileRaw.Model
		}
		if profileRaw.ReasoningEffort != nil {
			profile.ReasoningEffort = *profileRaw.ReasoningEffort
		}
		cfg.Profiles[name] = profile
	}
	if raw.Review != nil {
		if raw.Review.CodeGraphContext != nil {
			cfg.Review.CodeGraphContext = *raw.Review.CodeGraphContext
		}
		if n, ok := decodePositiveInt(&raw.Review.Timeout); ok {
			cfg.Review.Timeout = time.Duration(n) * time.Second
		}
		if n, ok := decodePositiveInt(&raw.Review.Parallel); ok {
			cfg.Review.Parallel = n
		}
		if raw.Review.EvidenceAdmission != nil {
			cfg.Review.EvidenceAdmission = *raw.Review.EvidenceAdmission
		}
		if raw.Review.CancellationEscalation != nil {
			cfg.Review.CancellationEscalation = *raw.Review.CancellationEscalation
		}
	}
	if raw.CI != nil {
		if raw.CI.Workflow != nil {
			cfg.CI.Workflow = strings.TrimSpace(*raw.CI.Workflow)
		}
		if raw.CI.WaitSeconds.Kind != 0 {
			seconds, err := decodeRequiredPositiveInt(&raw.CI.WaitSeconds, "ci.wait_seconds")
			if err != nil {
				return err
			}
			cfg.CI.WaitSeconds = seconds
		}
		if raw.CI.PollSeconds.Kind != 0 {
			seconds, err := decodeRequiredPositiveInt(&raw.CI.PollSeconds, "ci.poll_seconds")
			if err != nil {
				return err
			}
			cfg.CI.PollSeconds = seconds
		}
	}
	cfg.LintCommands = append(cfg.LintCommands, raw.LintCommands...)
	cfg.TestCommands = append(cfg.TestCommands, raw.TestCommands...)
	cfg.BuildCommands = append(cfg.BuildCommands, raw.BuildCommands...)

	if raw.Validation != nil {
		if err := applyValidation(cfg, raw.Validation); err != nil {
			return err
		}
	}
	return nil
}

// RepositoryRequestsExternalAgentDiff reads only the versioned
// per-project configuration: the global configuration can never enable
// this capability.
func RepositoryRequestsExternalAgentDiff(worktreePath string) bool {
	cfg := defaultConfig()
	if err := applyFromPath(&cfg, perProjectConfigPath(worktreePath)); err != nil {
		return false
	}
	return cfg.RequestExternalAgentDiff
}

// applyValidation applies over cfg.Validation the fields present in raw
// and validates their shape. Unlike the rest of the parser (where an
// invalid value is ignored and the default remains), here a malformed
// capability or profile produces an explicit error: a profile promising a
// capability that does not exist, or a scoped capability without
// scoped_command or without the {packages} marker, are configuration
// errors the operator must fix, not silent defaults hiding the problem.
func applyValidation(cfg *Config, raw *validationYAML) error {
	for name, capRaw := range raw.Capabilities {
		capability := cfg.Validation.Capabilities[name]
		if capRaw.Command != nil {
			capability.Command = *capRaw.Command
		}
		if capRaw.FailsWhen != nil {
			// fails_when has a closed domain (T1.1/T1.2 define it as
			// exit_code/output_not_empty): a typo like "exit-cede" cannot
			// be accepted silently, it would degrade the failure criterion
			// at runtime without the operator noticing (orchestrator
			// finding, outside the original text of the ticket).
			if *capRaw.FailsWhen != FailsWhenExitCode && *capRaw.FailsWhen != FailsWhenOutputNotEmpty {
				return fmt.Errorf("validation.capabilities.%s: invalid fails_when %q (valid values: %q, %q)",
					name, *capRaw.FailsWhen, FailsWhenExitCode, FailsWhenOutputNotEmpty)
			}
			capability.FailsWhen = *capRaw.FailsWhen
		} else if capability.FailsWhen == "" {
			capability.FailsWhen = FailsWhenExitCode
		}
		if capRaw.SupportsScope != nil {
			capability.SupportsScope = *capRaw.SupportsScope
		}
		if capRaw.ScopedCommand != nil {
			capability.ScopedCommand = *capRaw.ScopedCommand
		}
		if capRaw.Timeout != nil {
			capability.Timeout = *capRaw.Timeout
		}
		if capability.SupportsScope && capability.ScopedCommand == "" {
			return fmt.Errorf("validation.capabilities.%s: supports_scope=true requires scoped_command", name)
		}
		if capability.ScopedCommand != "" && !strings.Contains(capability.ScopedCommand, packagesMarker) {
			return fmt.Errorf("validation.capabilities.%s: scoped_command must contain the %s marker", name, packagesMarker)
		}
		cfg.Validation.Capabilities[name] = capability
	}
	for profile, capNames := range raw.Profiles {
		for _, capName := range capNames {
			if _, exists := cfg.Validation.Capabilities[capName]; !exists {
				return fmt.Errorf("validation.profiles.%s: capability %q is not declared in validation.capabilities", profile, capName)
			}
		}
		cfg.Validation.Profiles[profile] = capNames
	}
	if raw.Mode != nil {
		// mode also has a closed domain (worktree/inplace): same reason as
		// fails_when, above.
		if *raw.Mode != ModeWorktree && *raw.Mode != ModeInplace {
			return fmt.Errorf("validation.mode: %q is invalid (valid values: %q, %q)",
				*raw.Mode, ModeWorktree, ModeInplace)
		}
		cfg.Validation.Mode = *raw.Mode
	}
	return nil
}

// translateLegacyCommandsToCapabilities generates implicit capabilities from
// lint_commands/test_commands/build_commands when the yml declares no
// explicit validation.capabilities. Decision: an old config that only
// knows the pre-T1.2 command schema must keep producing a usable result
// for whoever asks for "the configured capabilities" (future task),
// without forcing the user to rewrite their yml.
// If the user already declared validation.capabilities, that declaration
// wins: two sources of truth for the same capabilities are not mixed. The
// commands of a single list are combined with "&&" into a single Command
// because CapabilityConfig models a command, not a list.
func translateLegacyCommandsToCapabilities(cfg *Config) {
	if len(cfg.Validation.Capabilities) > 0 {
		return
	}
	addImplicitCapability(cfg, "lint", cfg.LintCommands)
	addImplicitCapability(cfg, "unit_test", cfg.TestCommands)
	addImplicitCapability(cfg, "build", cfg.BuildCommands)
}

// addImplicitCapability adds a named entry built from the commands
// to cfg.Validation.Capabilities, if there is at least one.
func addImplicitCapability(cfg *Config, name string, comandos []string) {
	if len(comandos) == 0 {
		return
	}
	if cfg.Validation.Capabilities == nil {
		cfg.Validation.Capabilities = map[string]CapabilityConfig{}
	}
	cfg.Validation.Capabilities[name] = CapabilityConfig{
		Command:   strings.Join(comandos, " && "),
		FailsWhen: FailsWhenExitCode,
	}
}

// decodePositiveInt tries to read the node as a positive integer.
// It returns ok=false when the node is absent (zero Kind), not numeric or
// not positive, the same way the handcrafted parser's strconv.Atoi +
// "n > 0" worked: an invalid value is silently ignored and the default
// remains.
func decodePositiveInt(node *yaml.Node) (int, bool) {
	if node.Kind == 0 {
		return 0, false
	}
	var n int
	if err := node.Decode(&n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func decodeRequiredPositiveInt(node *yaml.Node, field string) (int, error) {
	var n int
	if err := node.Decode(&n); err != nil {
		return 0, fmt.Errorf("%s must be a positive integer: %w", field, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %d", field, n)
	}
	return n, nil
}

// keysInOrder returns the keys of a yaml.Node of mapping type in the
// textual order they appear in the file.
func keysInOrder(node *yaml.Node) []string {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys
}

// applyAgentOrder rebuilds cfg.AgentOrder with fileOrder (the
// declaration order in the processed file) at the head, followed by the
// already known agents that file does not declare, in their previous
// relative order.
func applyAgentOrder(cfg *Config, fileOrder []string) {
	if len(fileOrder) == 0 {
		return
	}
	inFile := make(map[string]bool, len(fileOrder))
	for _, name := range fileOrder {
		inFile[name] = true
	}
	newOrder := make([]string, 0, len(fileOrder)+len(cfg.AgentOrder))
	newOrder = append(newOrder, fileOrder...)
	for _, name := range cfg.AgentOrder {
		if !inFile[name] {
			newOrder = append(newOrder, name)
		}
	}
	cfg.AgentOrder = newOrder
}

// classOrderYAML reads without KnownFields the textual order of
// change.classes (same reason as agentOrderYAML).
type classOrderYAML struct {
	Change struct {
		Classes yaml.Node `yaml:"classes"`
	} `yaml:"change"`
}

// applyClassOrder rebuilds cfg.Change in the textual order of the file.
// By declaring change.classes the user replaces the defaults (same
// criterion as active_agent): two sources of rules are not merged.
//
// classes nil (the "classes" key does not appear under "change:") leaves
// the defaults intact; classes non-nil but empty ("classes: {}", declared
// on purpose) empties cfg.Change: they are two distinct user intents and
// they used to be confused (T3.1 review, both fell into the same
// "return").
func applyClassOrder(cfg *Config, data []byte, classes map[string][]string) {
	if classes == nil {
		return
	}
	var order classOrderYAML
	if err := yaml.Unmarshal(data, &order); err != nil {
		return
	}
	keys := keysInOrder(&order.Change.Classes)
	rules := make([]change.Rule, 0, len(keys))
	for _, key := range keys {
		rules = append(rules, change.Rule{Class: key, Patterns: classes[key]})
	}
	cfg.Change = rules
}
