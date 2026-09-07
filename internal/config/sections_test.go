package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestNewSections verifies the parsing of per-agent nested profiles (v2),
// review (timeout and parallel) and lint_commands.
func TestNewSections(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
agents:
  opencode:
    profiles:
      cheap:
        model: "flash-mini"
        reasoning_effort: "low"
      deep:
        model: "flash-max"
        reasoning_effort: "high"
review:
  timeout: 30
  parallel: 4
lint_commands:
  - "gofmt -l ."
  - "go vet ./..."
`)

	cfg := LoadLocalConfig(worktree)

	opencode := cfg.Agents["opencode"]
	if opencode.Profiles["cheap"].Model != "flash-mini" || opencode.Profiles["cheap"].ReasoningEffort != "low" {
		t.Errorf("profile opencode.cheap = %+v, expected flash-mini/low", opencode.Profiles["cheap"])
	}
	if opencode.Profiles["deep"].Model != "flash-max" || opencode.Profiles["deep"].ReasoningEffort != "high" {
		t.Errorf("profile opencode.deep = %+v, expected flash-max/high", opencode.Profiles["deep"])
	}
	if cfg.Review.Timeout != 30*time.Second {
		t.Errorf("Review.Timeout = %v, expected 30s", cfg.Review.Timeout)
	}
	if cfg.Review.Parallel != 4 {
		t.Errorf("Review.Parallel = %d, expected 4", cfg.Review.Parallel)
	}
	// The merge accumulates global + per-project commands: we check that the
	// per-project ones are present (there may be more if global config exists).
	found := 0
	for _, cmd := range cfg.LintCommands {
		if cmd == "gofmt -l ." || cmd == "go vet ./..." {
			found++
		}
	}
	if found != 2 {
		t.Errorf("LintCommands = %+v, the per-project commands are missing", cfg.LintCommands)
	}
}

// TestDefaultProfiles verifies that default provider profiles exist.
func TestDefaultProfiles(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := LoadLocalConfig(worktree)

	for _, agent := range []string{"claude", "opencode"} {
		for _, profile := range []string{"cheap", "normal", "deep"} {
			if _, ok := cfg.Agents[agent].Profiles[profile]; !ok {
				t.Errorf("missing default profile %q.%q", agent, profile)
			}
		}
	}
	if cfg.Review.Timeout != 900*time.Second || cfg.Review.Parallel != 2 {
		t.Errorf("review defaults = %v/%d, expected 900s/2", cfg.Review.Timeout, cfg.Review.Parallel)
	}
}

// TestConfigLegacyAgents verifies that the old agents syntax keeps working
// after the parser refactor.
func TestConfigLegacyAgents(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"active_agent: \"claude\"\nagents:\n  claude:\n    model: \"claude-legacy\"\n    reasoning_effort: \"high\"\n")

	cfg := LoadLocalConfig(worktree)
	if cfg.ActiveAgent != "claude" {
		t.Errorf("ActiveAgent = %q, expected claude", cfg.ActiveAgent)
	}
	if cfg.Agents["claude"].Model != "claude-legacy" {
		t.Errorf("Model = %q, expected claude-legacy", cfg.Agents["claude"].Model)
	}
}

// TestAgentOrderYml verifies that AgentOrder preserves the declaration order
// of the agents in the yml (claude before opencode).
func TestAgentOrderYml(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
agents:
  claude:
    model: "claude-x"
  opencode:
    model: "opencode-x"
`)

	cfg := LoadLocalConfig(worktree)
	expected := []string{"claude", "opencode"}
	if !reflect.DeepEqual(cfg.AgentOrder, expected) {
		t.Errorf("AgentOrder = %v, expected %v", cfg.AgentOrder, expected)
	}
}

// TestAgentOrderDefaults verifies that without configuration the default
// order is claude before opencode.
func TestAgentOrderDefaults(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := LoadLocalConfig(worktree)
	expected := []string{"claude", "opencode"}
	if !reflect.DeepEqual(cfg.AgentOrder, expected) {
		t.Errorf("AgentOrder = %v, expected %v", cfg.AgentOrder, expected)
	}
}

// TestAgentOrderPerProjectReorders verifies that the most specific file
// (per-project) dictates the order of the agents it declares and that the
// undeclared agents keep their previous relative order.
func TestAgentOrderPerProjectReorders(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"), `
agents:
  claude:
    model: "claude-g"
  opencode:
    model: "opencode-g"
`)
	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
agents:
  opencode:
    model: "opencode-l"
`)

	cfg := LoadLocalConfig(worktree)
	expected := []string{"opencode", "claude"}
	if !reflect.DeepEqual(cfg.AgentOrder, expected) {
		t.Errorf("AgentOrder = %v, expected %v", cfg.AgentOrder, expected)
	}
}

// TestInvalidTimeoutIsIgnored verifies that non-numeric timeout and parallel
// values do not break parsing and leave the default.
func TestInvalidTimeoutIsIgnored(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"review:\n  timeout: \"much\"\n  parallel: 0\n")

	cfg := LoadLocalConfig(worktree)
	if cfg.Review.Timeout != 900*time.Second {
		t.Errorf("Timeout = %v, expected default 900s", cfg.Review.Timeout)
	}
	if cfg.Review.Parallel != 2 {
		t.Errorf("Parallel = %d, expected default 2", cfg.Review.Parallel)
	}
}

// TestRemovedDurableRunsKeysFailStrictly pins the ticket 13 (R11) removal:
// review.durable_runs and the whole gate section no longer exist in the
// schema, and a yaml that still carries them fails fast through the existing
// strict-load rules with an explicit unknown-key error naming the removed
// key — never silently ignored, in global or project scope alike.
func TestRemovedDurableRunsKeysFailStrictly(t *testing.T) {
	tests := []struct {
		name   string
		global string
		yaml   string
		wantKy string
	}{
		{name: "project review.durable_runs is rejected", yaml: "review:\n  durable_runs: true\n", wantKy: "durable_runs"},
		{name: "project gate.durable_runs is rejected", yaml: "gate:\n  durable_runs: false\n", wantKy: "gate"},
		{name: "global review.durable_runs is rejected", global: "review:\n  durable_runs: true\n", wantKy: "durable_runs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			worktree := t.TempDir()
			setHome(t, home)
			writeConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"), tt.global)
			writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), tt.yaml)

			_, err := LoadStrictLocalConfig(worktree)
			if err == nil {
				t.Fatalf("config with removed key %q loaded without error; expected a strict unknown-key failure", tt.wantKy)
			}
			if !strings.Contains(err.Error(), tt.wantKy) {
				t.Fatalf("error = %v, want it to name the removed key %q", err, tt.wantKy)
			}
		})
	}
}

func TestReviewEvidenceAdmissionFlag(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		// Cutover default-on (ticket 07): an absent key keeps admission
		// strict; only an explicit false restores lenient acceptance.
		{name: "default true when absent", yaml: "", want: true},
		{name: "explicit true keeps admission", yaml: "review:\n  evidence_admission: true\n", want: true},
		{name: "explicit false restores lenient mode", yaml: "review:\n  evidence_admission: false\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			worktree := t.TempDir()
			setHome(t, home)
			writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), tt.yaml)
			cfg := LoadLocalConfig(worktree)
			if cfg.Review.EvidenceAdmission != tt.want {
				t.Fatalf("EvidenceAdmission = %v, want %v", cfg.Review.EvidenceAdmission, tt.want)
			}
		})
	}
}

func TestReviewCancellationEscalationFlag(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		// Default-on (ticket 08): an absent key keeps bounded whole-tree
		// escalation; only an explicit false restricts every kill to the
		// direct child.
		{name: "default true when absent", yaml: "", want: true},
		{name: "explicit true keeps escalation", yaml: "review:\n  cancellation_escalation: true\n", want: true},
		{name: "explicit false disables escalation", yaml: "review:\n  cancellation_escalation: false\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			worktree := t.TempDir()
			setHome(t, home)
			writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), tt.yaml)
			cfg := LoadLocalConfig(worktree)
			if cfg.Review.CancellationEscalation != tt.want {
				t.Fatalf("CancellationEscalation = %v, want %v", cfg.Review.CancellationEscalation, tt.want)
			}
		})
	}
}
