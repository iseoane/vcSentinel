package agentadapter

import (
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

func TestNuevoAdaptadorConPerfilExplicito(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "opencode",
		Agents: map[string]config.AgentConfig{
			"opencode": {Model: "base", ReasoningEffort: "medium"},
		},
		Profiles: map[string]config.ProfileConfig{
			"deep": {Agent: "opencode", Model: "claude-sonnet", ReasoningEffort: "max"},
		},
		Review: config.ReviewConfig{Timeout: 30 * time.Second, Parallel: 2, Dims: map[string]string{"security": "deep"}},
	}

	perfil := config.ResolverPerfil(cfg, "security", "")
	adapter, err := NuevoAdaptadorConPerfil(cfg, perfil)
	if err != nil {
		t.Fatalf("NuevoAdaptadorConPerfil devolvió error: %v", err)
	}
	if adapter.nombreBase() != "opencode" {
		t.Errorf("binario = %q, esperado opencode", adapter.BinaryName)
	}
	if adapter.Config.Model != "claude-sonnet" || adapter.Config.ReasoningEffort != "max" {
		t.Errorf("config = %+v, esperado claude-sonnet/max del perfil", adapter.Config)
	}
	if adapter.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, esperado 30s de review.timeout", adapter.Timeout)
	}
}

func TestNuevoAdaptadorConPerfilHeredaDelAgente(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "claude",
		Agents: map[string]config.AgentConfig{
			"claude": {Model: "claude-3-5-sonnet", ReasoningEffort: "high"},
		},
		Profiles: map[string]config.ProfileConfig{
			"normal": {},
		},
		Review: config.ReviewConfig{Dims: map[string]string{}},
	}

	perfil := config.ResolverPerfil(cfg, "logic", "")
	adapter, err := NuevoAdaptadorConPerfil(cfg, perfil)
	if err != nil {
		t.Fatalf("NuevoAdaptadorConPerfil devolvió error: %v", err)
	}
	if adapter.nombreBase() != "claude" {
		t.Errorf("binario = %q, esperado claude (active_agent)", adapter.BinaryName)
	}
	if adapter.Config.Model != "claude-3-5-sonnet" {
		t.Errorf("modelo = %q, esperado heredar del agente", adapter.Config.Model)
	}
}

func TestNuevoAdaptadorSinAgentesEnPATH(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "auto",
		Agents: map[string]config.AgentConfig{
			"agente-inexistente-xyz": {Model: "m", ReasoningEffort: "low"},
		},
		Profiles: map[string]config.ProfileConfig{"normal": {}},
		Review:   config.ReviewConfig{Dims: map[string]string{}},
	}

	perfil := config.ResolverPerfil(cfg, "logic", "")
	if _, err := NuevoAdaptadorConPerfil(cfg, perfil); err == nil {
		t.Error("sin agentes en el PATH se esperaba un error explícito")
	}
}
