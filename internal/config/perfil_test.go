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
	if perfil.Binario != "" || perfil.Modelo != "" || perfil.Esfuerzo != "" {
		t.Errorf("el perfil normal debería heredar todo (vació), obtenido %+v", perfil)
	}

	perfil = ResolverPerfil(cfg, "security", "")
	if perfil.Nombre != "deep" {
		t.Errorf("security -> perfil %q, esperado deep", perfil.Nombre)
	}
	if perfil.Modelo != "claude-3-5-sonnet" || perfil.Esfuerzo != "high" {
		t.Errorf("perfil deep = %+v, esperado claude-3-5-sonnet/high", perfil)
	}

	perfil = ResolverPerfil(cfg, "spec", "")
	if perfil.Nombre != "cheap" || perfil.Esfuerzo != "default" {
		t.Errorf("spec -> %+v, esperado cheap/default", perfil)
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
	cfg.Review.Dims["logic"] = "cheap"
	cfg.Profiles["cheap"] = ProfileConfig{Agent: "opencode", Model: "mini"}

	perfil := ResolverPerfil(cfg, "logic", "")
	if perfil.Nombre != "cheap" || perfil.Binario != "opencode" || perfil.Modelo != "mini" {
		t.Errorf("perfil = %+v, esperado cheap con opencode/mini", perfil)
	}
}

func TestResolverPerfilDimensionDesconocida(t *testing.T) {
	cfg := configuracionPorDefecto()

	perfil := ResolverPerfil(cfg, "perf", "")
	if perfil.Nombre != "normal" {
		t.Errorf("dimensión sin mapear -> %q, esperado normal", perfil.Nombre)
	}
}
