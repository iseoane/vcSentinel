package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestAgentEntryParsesACPXFields verifies that an agent entry may declare
// kind: acpx with its agent token and an optional enforcement declaration,
// and that every declared field lands in AgentConfig (ticket 16 slice 3).
func TestAgentEntryParsesACPXFields(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)
	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
active_agent: claude-acpx
agents:
  claude-acpx:
    kind: acpx
    agent: claude
    model: claude-sonnet-x
    reasoning_effort: high
    enforcement: claude-sandbox
`)
	cfg := LoadLocalConfig(worktree)
	got := cfg.Agents["claude-acpx"]
	want := AgentConfig{
		Kind:            "acpx",
		ACPAgent:        "claude",
		Model:           "claude-sonnet-x",
		ReasoningEffort: "high",
		Enforcement:     EnforcementClaudeSandbox,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("agents.claude-acpx = %+v, want %+v", got, want)
	}
}

// TestAgentEntryEnforcementDefaultsToNone verifies that omitting enforcement
// leaves the field empty, which every consumer treats as grant-by-design.
func TestAgentEntryEnforcementDefaultsToNone(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)
	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
active_agent: opencode-acpx
agents:
  opencode-acpx:
    kind: acpx
    agent: opencode
`)
	cfg := LoadLocalConfig(worktree)
	got := cfg.Agents["opencode-acpx"]
	if got.Kind != "acpx" || got.ACPAgent != "opencode" || got.Enforcement != "" {
		t.Fatalf("agents.opencode-acpx = %+v, want kind/agent set and empty enforcement", got)
	}
}

// TestLegacyAgentEntriesStayUnchanged pins the compatibility contract: a
// legacy-style configuration without any of the new keys produces agent
// entries whose new fields stay zero-valued, so no existing vassentinel.yml
// changes behavior.
func TestLegacyAgentEntriesStayUnchanged(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)
	writeConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"), `
version: 1
active_agent: claude
agents:
  claude:
    model: claude-legacy
    reasoning_effort: high
    profiles:
      commit:
        reasoning_effort: low
`)
	cfg := LoadLocalConfig(worktree)
	for name, agent := range cfg.Agents {
		if agent.Kind != "" || agent.ACPAgent != "" || agent.Enforcement != "" {
			t.Errorf("agent %q = %+v, want zero-valued kind/agent/enforcement for a legacy entry", name, agent)
		}
	}
	if got := cfg.Agents["claude"]; got.Model != "claude-legacy" || got.ReasoningEffort != "high" {
		t.Fatalf("agents.claude = %+v, legacy model/effort must parse unchanged", got)
	}
	if got := cfg.Agents["claude"].Profiles["commit"].ReasoningEffort; got != "low" {
		t.Fatalf("commit profile effort = %q, legacy nested profiles must parse unchanged", got)
	}
}
