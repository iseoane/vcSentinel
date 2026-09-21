package agentadapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

func TestNewAdapterWithProfileExplicit(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "opencode",
		Agents: map[string]config.AgentConfig{
			"opencode": {Model: "base", ReasoningEffort: "medium"},
		},
		Profiles: map[string]config.ProfileConfig{
			"deep": {Agent: "opencode", Model: "claude-sonnet", ReasoningEffort: "max"},
		},
		Review: config.ReviewConfig{Timeout: 30 * time.Second, Parallel: 2},
	}

	profile := config.ResolveProfile(cfg, "deep", "")
	adapter, err := NewAdapterWithProfile(cfg, profile)
	if err != nil {
		t.Fatalf("NewAdapterWithProfile returned an error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("concrete path: want *CLIAdapter, got %T", adapter)
	}
	if cli.baseName() != "opencode" {
		t.Errorf("binary = %q, want opencode", cli.BinaryName)
	}
	if cli.Config.Model != "claude-sonnet" || cli.Config.ReasoningEffort != "max" {
		t.Errorf("config = %+v, want the profile's claude-sonnet/max", cli.Config)
	}
	if cli.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, want 30s from review.timeout", cli.Timeout)
	}
}

func TestNewAdapterWithProfileInheritsFromAgent(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "claude",
		Agents: map[string]config.AgentConfig{
			"claude": {Model: "claude-3-5-sonnet", ReasoningEffort: "high"},
		},
		Profiles: map[string]config.ProfileConfig{
			"normal": {},
		},
		Review: config.ReviewConfig{},
	}

	profile := config.ResolveProfile(cfg, "normal", "")
	adapter, err := NewAdapterWithProfile(cfg, profile)
	if err != nil {
		t.Fatalf("NewAdapterWithProfile returned an error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("concrete path: want *CLIAdapter, got %T", adapter)
	}
	if cli.baseName() != "claude" {
		t.Errorf("binary = %q, want claude (active_agent)", cli.BinaryName)
	}
	if cli.Config.Model != "claude-3-5-sonnet" {
		t.Errorf("model = %q, want inherited from the agent", cli.Config.Model)
	}
}

func TestNewAdapterWithoutAgentsInPATH(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "auto",
		Agents: map[string]config.AgentConfig{
			"agente-inexistente-xyz": {Model: "m", ReasoningEffort: "low"},
		},
		Profiles: map[string]config.ProfileConfig{"normal": {}},
		Review:   config.ReviewConfig{},
	}

	profile := config.ResolveProfile(cfg, "normal", "")
	if adapter, err := NewAdapterWithProfile(cfg, profile); err == nil || adapter != nil {
		t.Errorf("without agents on the PATH an explicit error was expected, got %T/%v", adapter, err)
	}
}

// TestBuildChainProfile verifies the auto path builds a chain with one adapter
// per available agent, in the received order, and that each one carries the
// model/effort of ITS nested profile (not the resolved profile's).
func TestBuildChainProfile(t *testing.T) {
	cfg := config.Config{
		AgentOrder: []string{"claude", "opencode"},
		Agents: map[string]config.AgentConfig{
			"claude": {
				Model:           "claude-5-sonnet",
				ReasoningEffort: "high",
				Profiles: map[string]config.ProfileConfig{
					"normal": {ReasoningEffort: "low"},
				},
			},
			"opencode": {
				Model:           "deepseek-x",
				ReasoningEffort: "max",
				Profiles: map[string]config.ProfileConfig{
					"normal": {Model: "deepseek-mini"},
				},
			},
		},
		Review: config.ReviewConfig{Timeout: 45 * time.Second},
	}
	profile := config.ResolvedProfile{Name: "normal", Binary: "auto"}

	chain, err := buildChainProfile(cfg, []string{"claude", "opencode"}, profile)
	if err != nil {
		t.Fatalf("buildChainProfile returned an error: %v", err)
	}
	if len(chain.adapters) != 2 {
		t.Fatalf("the chain has %d adapters, want 2", len(chain.adapters))
	}

	claude, ok := chain.adapters[0].(*CLIAdapter)
	if !ok {
		t.Fatalf("adapter[0] = %T, want *CLIAdapter", chain.adapters[0])
	}
	if claude.baseName() != "claude" {
		t.Errorf("adapter[0] binary = %q, want claude", claude.BinaryName)
	}
	// claude's normal profile defines only the effort: the model is inherited
	// from the agent.
	if claude.Config.Model != "claude-5-sonnet" || claude.Config.ReasoningEffort != "low" {
		t.Errorf("claude config = %+v, want the agent's model + the profile's low", claude.Config)
	}
	if claude.Timeout != 45*time.Second {
		t.Errorf("claude timeout = %v, want 45s", claude.Timeout)
	}

	opencode, ok := chain.adapters[1].(*CLIAdapter)
	if !ok {
		t.Fatalf("adapter[1] = %T, want *CLIAdapter", chain.adapters[1])
	}
	if opencode.baseName() != "opencode" {
		t.Errorf("adapter[1] binary = %q, want opencode", opencode.BinaryName)
	}
	// opencode's normal profile defines only the model: the effort is
	// inherited from the agent.
	if opencode.Config.Model != "deepseek-mini" || opencode.Config.ReasoningEffort != "max" {
		t.Errorf("opencode config = %+v, want the profile's deepseek-mini + the agent's max", opencode.Config)
	}
	if opencode.Timeout != 45*time.Second {
		t.Errorf("opencode timeout = %v, want 45s", opencode.Timeout)
	}
}

// TestAgentNamesInPATHPreserveYmlOrder verifies the available-agent selection
// keeps the order declared in AgentOrder (instead of the previous alphabetical
// order). It compiles two binaries named after agents into a temporary
// directory to control the PATH.
func TestAgentNamesInPATHPreserveYmlOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("skips binary compilation in -short mode")
	}
	dir := t.TempDir()
	compileAgent := func(name string) {
		t.Helper()
		exe := filepath.Join(dir, name)
		if filepath.Ext(exe) == "" && os.PathSeparator == '\\' {
			exe += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", exe, ".")
		cmd.Dir = filepath.Join("testdata", "sleeper")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("could not compile agent %s: %v\n%s", name, err, output)
		}
	}
	compileAgent("claude")
	compileAgent("opencode")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config.Config{
		AgentOrder: []string{"opencode", "claude"},
		Agents: map[string]config.AgentConfig{
			"claude":   {},
			"opencode": {},
		},
	}
	got := agentNamesInPATH(cfg)
	want := []string{"opencode", "claude"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agentNamesInPATH = %v, want %v (yml order)", got, want)
	}
}

// TestNewAdapterForMessageUsesCommitProfile verifies the shared helper, in its
// "commit" variant, applies the agent's nested low-reasoning profile and
// inherits the base model when the profile only defines the effort.
func TestNewAdapterForMessageUsesCommitProfile(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "opencode",
		Agents: map[string]config.AgentConfig{
			"opencode": {
				Model:           "base",
				ReasoningEffort: "high",
				Profiles: map[string]config.ProfileConfig{
					"commit": {ReasoningEffort: "low"},
				},
			},
		},
	}

	adapter, err := newAdapter(cfg, "opencode", "commit")
	if err != nil {
		t.Fatalf("newAdapter returned an error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("want *CLIAdapter, got %T", adapter)
	}
	if cli.baseName() != "opencode" {
		t.Errorf("binary = %q, want opencode", cli.BinaryName)
	}
	// The commit profile defines only reasoning_effort: the model is inherited
	// from the agent.
	if cli.Config.Model != "base" {
		t.Errorf("model = %q, want inherited 'base' from the agent", cli.Config.Model)
	}
	if cli.Config.ReasoningEffort != "low" {
		t.Errorf("effort = %q, want 'low' from the commit profile", cli.Config.ReasoningEffort)
	}
}

// TestNewAdapterForMessageWithoutProfileFallsBackToBase verifies the
// compatibility: when the agent does not define the commit profile, the
// adapter keeps the same configuration NewAgentAdapterNamed would return
// (base model/effort).
func TestNewAdapterForMessageWithoutProfileFallsBackToBase(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "opencode",
		Agents: map[string]config.AgentConfig{
			"opencode": {Model: "deepseek-v4-flash-free", ReasoningEffort: "high"},
		},
	}

	adapter, err := newAdapter(cfg, "opencode", "commit")
	if err != nil {
		t.Fatalf("newAdapter returned an error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("want *CLIAdapter, got %T", adapter)
	}
	want := cfg.Agents["opencode"]
	if !reflect.DeepEqual(cli.Config, want) {
		t.Errorf("config = %+v, want %+v (same as the agent)", cli.Config, want)
	}
}

// TestNewAdapterForMessageUnknownAgent verifies the message variant keeps
// NewAgentAdapterNamed's error for unconfigured agents.
func TestNewAdapterForMessageUnknownAgent(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "opencode",
		Agents:      map[string]config.AgentConfig{"opencode": {Model: "base"}},
	}

	adapter, err := newAdapter(cfg, "agente-inexistente-xyz", "commit")
	if err == nil {
		t.Fatalf("an error was expected for an unconfigured agent, got %T/%v", adapter, err)
	}
	want := `agent "agente-inexistente-xyz" is not configured in vcsentinel.yml`
	if err.Error() != want {
		t.Errorf("error = %q, want %q (compatibility with NewAgentAdapterNamed)", err.Error(), want)
	}
}
