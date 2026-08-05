package config

import (
	"testing"
)

func TestResolverPerfilDesdeDims(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "logic", "")
	if perfil.Nombre != "normal" {
		t.Errorf("logic -> perfil %q, esperado normal", perfil.Nombre)
	}

	perfil = ResolverPerfil(cfg, "security", "")
	if perfil.Nombre != "deep" {
		t.Errorf("security -> perfil %q, esperado deep", perfil.Nombre)
	}

	perfil = ResolverPerfil(cfg, "spec", "")
	if perfil.Nombre != "cheap" {
		t.Errorf("spec -> %+v, esperado cheap", perfil)
	}
}

func TestResolverPerfilV2AgentePuntoPerfil(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "logic", "opencode.cheap")
	if perfil.Binario != "opencode" || perfil.Modelo != "deepseek-v4-flash-free" || perfil.Esfuerzo != "default" {
		t.Errorf("opencode.cheap = %+v, esperado opencode/deepseek-v4-flash-free/default", perfil)
	}

	perfil = ResolverPerfil(cfg, "logic", "claude.deep")
	if perfil.Binario != "claude" || perfil.Modelo != "claude-opus" {
		t.Errorf("claude.deep = %+v, esperado claude/claude-opus", perfil)
	}
}

func TestResolverPerfilV2HeredaDelAgente(t *testing.T) {
	cfg := configuracionPorDefecto()
	// Perfil anidado sin modelo propio: hereda el del agente.
	cfg.Agents["opencode"].Profiles["extra"] = ProfileConfig{ReasoningEffort: "medium"}

	perfil := ResolverPerfil(cfg, "logic", "opencode.extra")
	if perfil.Modelo != "deepseek-v4-flash-free" || perfil.Esfuerzo != "medium" {
		t.Errorf("opencode.extra = %+v, esperado heredar modelo del agente + medium", perfil)
	}
}

func TestResolverPerfilConOverride(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "spec", "deep")
	if perfil.Nombre != "deep" {
		t.Errorf("override deep sobre spec -> %q, esperado deep", perfil.Nombre)
	}
}

func TestResolverPerfilDimsConfiguradas(t *testing.T) {
	cfg := configuracionPorDefecto()
	cfg.Review.Dims["logic"] = "opencode.cheap"
	cfg.Agents["opencode"].Profiles["cheap"] = ProfileConfig{Model: "mini"}

	perfil := ResolverPerfil(cfg, "logic", "")
	if perfil.Nombre != "opencode.cheap" || perfil.Binario != "opencode" || perfil.Modelo != "mini" {
		t.Errorf("perfil = %+v, esperado opencode.cheap con opencode/mini", perfil)
	}
}

func TestResolverPerfilDimensionDesconocida(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "perf", "")
	if perfil.Nombre != "normal" {
		t.Errorf("dimensión sin mapear -> %q, esperado normal", perfil.Nombre)
	}
}

// TestResolverPerfilAgente verifica el helper de perfil anidado de un agente
// concreto: perfil definido, perfil sin modelo (hereda del agente) y perfil
// inexistente (hereda todo del agente).
func TestResolverPerfilAgente(t *testing.T) {
	cfg := configuracionPorDefecto()

	t.Run("perfil anidado definido", func(t *testing.T) {
		modelo, esfuerzo := ResolverPerfilAgente(cfg, "opencode", "cheap")
		if modelo != "deepseek-v4-flash-free" || esfuerzo != "default" {
			t.Errorf("opencode.cheap = %s/%s, esperado deepseek-v4-flash-free/default", modelo, esfuerzo)
		}
	})

	t.Run("perfil sin modelo hereda del agente", func(t *testing.T) {
		cfg.Agents["opencode"].Profiles["extra"] = ProfileConfig{ReasoningEffort: "medium"}
		modelo, esfuerzo := ResolverPerfilAgente(cfg, "opencode", "extra")
		if modelo != "deepseek-v4-flash-free" || esfuerzo != "medium" {
			t.Errorf("opencode.extra = %s/%s, esperado heredar modelo del agente + medium", modelo, esfuerzo)
		}
	})

	t.Run("perfil inexistente hereda todo del agente", func(t *testing.T) {
		modelo, esfuerzo := ResolverPerfilAgente(cfg, "claude", "no-existe")
		if modelo != "claude-5-sonnet" || esfuerzo != "high" {
			t.Errorf("claude.no-existe = %s/%s, esperado heredar todo del agente", modelo, esfuerzo)
		}
	})
}
