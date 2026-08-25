package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestSeccionesNuevas verifica el parseo de profiles anidados por agente (v2),
// review (timeout and parallel) and lint_commands.
func TestSeccionesNuevas(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
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

	cfg := CargarConfiguracionLocal(worktree)

	opencode := cfg.Agents["opencode"]
	if opencode.Profiles["cheap"].Model != "flash-mini" || opencode.Profiles["cheap"].ReasoningEffort != "low" {
		t.Errorf("perfil opencode.cheap = %+v, esperado flash-mini/low", opencode.Profiles["cheap"])
	}
	if opencode.Profiles["deep"].Model != "flash-max" || opencode.Profiles["deep"].ReasoningEffort != "high" {
		t.Errorf("perfil opencode.deep = %+v, esperado flash-max/high", opencode.Profiles["deep"])
	}
	if cfg.Review.Timeout != 30*time.Second {
		t.Errorf("Review.Timeout = %v, esperado 30s", cfg.Review.Timeout)
	}
	if cfg.Review.Parallel != 4 {
		t.Errorf("Review.Parallel = %d, esperado 4", cfg.Review.Parallel)
	}
	// El merge acumula comandos globales + per-proyecto: verificamos que los
	// per-proyecto estén presentes (puede haber más si existe config global).
	encontrados := 0
	for _, cmd := range cfg.LintCommands {
		if cmd == "gofmt -l ." || cmd == "go vet ./..." {
			encontrados++
		}
	}
	if encontrados != 2 {
		t.Errorf("LintCommands = %+v, faltan los comandos per-proyecto", cfg.LintCommands)
	}
}

// TestDefaultsPerfiles verifies that default provider profiles exist.
func TestDefaultsPerfiles(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := CargarConfiguracionLocal(worktree)

	for _, agente := range []string{"claude", "opencode"} {
		for _, perfil := range []string{"cheap", "normal", "deep"} {
			if _, existe := cfg.Agents[agente].Profiles[perfil]; !existe {
				t.Errorf("falta el perfil %q.%q por defecto", agente, perfil)
			}
		}
	}
	if cfg.Review.Timeout != 300*time.Second || cfg.Review.Parallel != 2 {
		t.Errorf("defaults review = %v/%d, esperado 300s/2", cfg.Review.Timeout, cfg.Review.Parallel)
	}
}

// TestConfigLegacyAgents verifica que la sintaxis antigua de agents sigue
// funcionando tras el refactor del parser.
func TestConfigLegacyAgents(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"active_agent: \"claude\"\nagents:\n  claude:\n    model: \"claude-legacy\"\n    reasoning_effort: \"high\"\n")

	cfg := CargarConfiguracionLocal(worktree)
	if cfg.ActiveAgent != "claude" {
		t.Errorf("ActiveAgent = %q, esperado claude", cfg.ActiveAgent)
	}
	if cfg.Agents["claude"].Model != "claude-legacy" {
		t.Errorf("Model = %q, esperado claude-legacy", cfg.Agents["claude"].Model)
	}
}

// TestAgentOrderYml verifica que AgentOrder preserva el orden de declaración
// de los agentes en el yml (claude antes que opencode).
func TestAgentOrderYml(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
agents:
  claude:
    model: "claude-x"
  opencode:
    model: "opencode-x"
`)

	cfg := CargarConfiguracionLocal(worktree)
	esperado := []string{"claude", "opencode"}
	if !reflect.DeepEqual(cfg.AgentOrder, esperado) {
		t.Errorf("AgentOrder = %v, esperado %v", cfg.AgentOrder, esperado)
	}
}

// TestAgentOrderDefaults verifica que sin configuración el orden por defecto
// es claude antes que opencode.
func TestAgentOrderDefaults(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := CargarConfiguracionLocal(worktree)
	esperado := []string{"claude", "opencode"}
	if !reflect.DeepEqual(cfg.AgentOrder, esperado) {
		t.Errorf("AgentOrder = %v, esperado %v", cfg.AgentOrder, esperado)
	}
}

// TestAgentOrderPerProyectoReordena verifica que el archivo más específico
// (per-proyecto) manda el orden de los agentes que declara y que los agentes
// no declarados conservan su posición relativa anterior.
func TestAgentOrderPerProyectoReordena(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"), `
agents:
  claude:
    model: "claude-g"
  opencode:
    model: "opencode-g"
`)
	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
agents:
  opencode:
    model: "opencode-l"
`)

	cfg := CargarConfiguracionLocal(worktree)
	esperado := []string{"opencode", "claude"}
	if !reflect.DeepEqual(cfg.AgentOrder, esperado) {
		t.Errorf("AgentOrder = %v, esperado %v", cfg.AgentOrder, esperado)
	}
}

// TestTimeoutInvalidoSeIgnora verifica que valores no numéricos de timeout y
// parallel no rompen el parseo y dejan el default.
func TestTimeoutInvalidoSeIgnora(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"review:\n  timeout: \"mucho\"\n  parallel: 0\n")

	cfg := CargarConfiguracionLocal(worktree)
	if cfg.Review.Timeout != 300*time.Second {
		t.Errorf("Timeout = %v, esperado default 300s", cfg.Review.Timeout)
	}
	if cfg.Review.Parallel != 2 {
		t.Errorf("Parallel = %d, esperado default 2", cfg.Review.Parallel)
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
			escribirConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"), tt.global)
			escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), tt.yaml)

			_, err := CargarConfiguracionLocalEstricta(worktree)
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
			escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), tt.yaml)
			cfg := CargarConfiguracionLocal(worktree)
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
			escribirConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), tt.yaml)
			cfg := CargarConfiguracionLocal(worktree)
			if cfg.Review.CancellationEscalation != tt.want {
				t.Fatalf("CancellationEscalation = %v, want %v", cfg.Review.CancellationEscalation, tt.want)
			}
		})
	}
}
