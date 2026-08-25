package config

import (
	"testing"
)

func TestResolverPerfilUsesSuppliedContractDefault(t *testing.T) {
	cfg := configuracionPorDefecto()

	profile := ResolverPerfil(cfg, "normal", "")
	if profile.Nombre != "normal" {
		t.Errorf("logic -> profile %q, expected normal", profile.Nombre)
	}

	profile = ResolverPerfil(cfg, "deep", "")
	if profile.Nombre != "deep" {
		t.Errorf("security -> profile %q, expected deep", profile.Nombre)
	}

	profile = ResolverPerfil(cfg, "cheap", "")
	if profile.Nombre != "cheap" {
		t.Errorf("spec -> %+v, expected cheap", profile)
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

// TestResolverPerfilPuntoSinAgenteNoRompe verifica que un nombre de perfil con
// punto que NO corresponde a un agente configurado (p. ej. "gpt-4.1") no se
// parte como "agente.perfil": partirlo dejaría Binario en un binario
// inexistente y sin herencia de modelo/esfuerzo. Debe caer a la vía v1.
func TestResolverPerfilPuntoSinAgenteNoRompe(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "logic", "gpt-4.1")
	if perfil.Binario == "gpt-4" {
		t.Errorf("perfil = %+v: 'gpt-4' no es un agente configurado, no debe usarse como binario", perfil)
	}
	if perfil.Nombre != "gpt-4.1" {
		t.Errorf("Nombre = %q, esperado el nombre completo gpt-4.1", perfil.Nombre)
	}
}

// TestResolverPerfilPuntoSinAgenteHeredaDelActivo verifica que, además de no
// partirse, el perfil desconocido con punto hereda del agente activo como
// cualquier otro perfil v1: nunca debe quedarse sin modelo ni sin esfuerzo.
func TestResolverPerfilPuntoSinAgenteHeredaDelActivo(t *testing.T) {
	cfg := configuracionPorDefecto()
	cfg.ActiveAgent = "claude"

	perfil := ResolverPerfil(cfg, "logic", "gpt-4.1")
	if perfil.Binario != "claude" {
		t.Errorf("Binario = %q, esperado el agente activo claude", perfil.Binario)
	}
	if perfil.Modelo == "" || perfil.Esfuerzo == "" {
		t.Errorf("perfil = %+v, esperado heredar modelo y esfuerzo del agente activo", perfil)
	}
}

// TestResolverPerfilV2SigueFuncionandoConAgenteReal blinda la vía v2 frente al
// arreglo anterior: un nombre con punto cuyo prefijo SÍ es un agente
// configurado debe seguir partiéndose.
func TestResolverPerfilV2SigueFuncionandoConAgenteReal(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "logic", "claude.cheap")
	if perfil.Binario != "claude" {
		t.Errorf("Binario = %q, esperado claude", perfil.Binario)
	}
}

func TestResolverPerfilConOverride(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "spec", "deep")
	if perfil.Nombre != "deep" {
		t.Errorf("override deep sobre spec -> %q, esperado deep", perfil.Nombre)
	}
}

func TestResolverPerfilUsesExplicitProviderProfile(t *testing.T) {
	cfg := configuracionPorDefecto()
	cfg.Agents["opencode"].Profiles["cheap"] = ProfileConfig{Model: "mini"}

	profile := ResolverPerfil(cfg, "opencode.cheap", "")
	if profile.Nombre != "opencode.cheap" || profile.Binario != "opencode" || profile.Modelo != "mini" {
		t.Errorf("profile = %+v, expected opencode.cheap with opencode/mini", profile)
	}
}

func TestResolverPerfilUsesNormalWhenRequested(t *testing.T) {
	cfg := configuracionPorDefecto()

	profile := ResolverPerfil(cfg, "normal", "")
	if profile.Nombre != "normal" {
		t.Errorf("default profile -> %q, expected normal", profile.Nombre)
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
