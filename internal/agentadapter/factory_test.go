package agentadapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("camino concreto: se esperaba *CLIAdapter, obtuve %T", adapter)
	}
	if cli.nombreBase() != "opencode" {
		t.Errorf("binario = %q, esperado opencode", cli.BinaryName)
	}
	if cli.Config.Model != "claude-sonnet" || cli.Config.ReasoningEffort != "max" {
		t.Errorf("config = %+v, esperado claude-sonnet/max del perfil", cli.Config)
	}
	if cli.Timeout != 30*time.Second {
		t.Errorf("timeout = %v, esperado 30s de review.timeout", cli.Timeout)
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
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("camino concreto: se esperaba *CLIAdapter, obtuve %T", adapter)
	}
	if cli.nombreBase() != "claude" {
		t.Errorf("binario = %q, esperado claude (active_agent)", cli.BinaryName)
	}
	if cli.Config.Model != "claude-3-5-sonnet" {
		t.Errorf("modelo = %q, esperado heredar del agente", cli.Config.Model)
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
	if adapter, err := NuevoAdaptadorConPerfil(cfg, perfil); err == nil || adapter != nil {
		t.Errorf("sin agentes en el PATH se esperaba un error explícito, obtuve %T/%v", adapter, err)
	}
}

// TestConstruirCadenaPerfil verifica que el camino auto construye una cadena
// con un adaptador por agente disponible, en el orden recibido, y que cada uno
// lleva el modelo/esfuerzo de SU perfil anidado (no el del perfil resuelto).
func TestConstruirCadenaPerfil(t *testing.T) {
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
	perfil := config.PerfilResuelto{Nombre: "normal", Binario: "auto"}

	cadena := construirCadenaPerfil(cfg, []string{"claude", "opencode"}, perfil)
	if len(cadena.adaptadores) != 2 {
		t.Fatalf("la cadena tiene %d adaptadores, esperado 2", len(cadena.adaptadores))
	}

	claude, ok := cadena.adaptadores[0].(*CLIAdapter)
	if !ok {
		t.Fatalf("adaptador[0] = %T, esperado *CLIAdapter", cadena.adaptadores[0])
	}
	if claude.nombreBase() != "claude" {
		t.Errorf("adaptador[0] binario = %q, esperado claude", claude.BinaryName)
	}
	// El perfil normal de claude define solo esfuerzo: hereda el modelo del agente.
	if claude.Config.Model != "claude-5-sonnet" || claude.Config.ReasoningEffort != "low" {
		t.Errorf("claude config = %+v, esperado modelo del agente + low del perfil", claude.Config)
	}
	if claude.Timeout != 45*time.Second {
		t.Errorf("claude timeout = %v, esperado 45s", claude.Timeout)
	}

	opencode, ok := cadena.adaptadores[1].(*CLIAdapter)
	if !ok {
		t.Fatalf("adaptador[1] = %T, esperado *CLIAdapter", cadena.adaptadores[1])
	}
	if opencode.nombreBase() != "opencode" {
		t.Errorf("adaptador[1] binario = %q, esperado opencode", opencode.BinaryName)
	}
	// El perfil normal de opencode define solo modelo: hereda el esfuerzo del agente.
	if opencode.Config.Model != "deepseek-mini" || opencode.Config.ReasoningEffort != "max" {
		t.Errorf("opencode config = %+v, esperado deepseek-mini del perfil + max del agente", opencode.Config)
	}
	if opencode.Timeout != 45*time.Second {
		t.Errorf("opencode timeout = %v, esperado 45s", opencode.Timeout)
	}
}

// TestNombresAgentesEnPATHConservaOrdenYml verifica que la selección de
// agentes disponibles conserva el orden declarado en AgentOrder (en lugar del
// orden alfabético anterior). Compila dos binarios con nombres de agente en un
// directorio temporal para controlar el PATH.
func TestNombresAgentesEnPATHConservaOrdenYml(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la compilación de binarios en modo -short")
	}
	dir := t.TempDir()
	compilarAgente := func(nombre string) {
		t.Helper()
		exe := filepath.Join(dir, nombre)
		if filepath.Ext(exe) == "" && os.PathSeparator == '\\' {
			exe += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", exe, ".")
		cmd.Dir = filepath.Join("testdata", "sleeper")
		if salida, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("no se pudo compilar el agente %s: %v\n%s", nombre, err, salida)
		}
	}
	compilarAgente("claude")
	compilarAgente("opencode")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := config.Config{
		AgentOrder: []string{"opencode", "claude"},
		Agents: map[string]config.AgentConfig{
			"claude":   {},
			"opencode": {},
		},
	}
	obtenido := nombresAgentesEnPATH(cfg)
	esperado := []string{"opencode", "claude"}
	if !reflect.DeepEqual(obtenido, esperado) {
		t.Errorf("nombresAgentesEnPATH = %v, esperado %v (orden del yml)", obtenido, esperado)
	}
}

// TestNuevoAdaptadorMensajeUsaPerfilCommit verifica que el helper compartido,
// en su variante "commit", aplica el perfil anidado de razonamiento bajo del
// agente y hereda el modelo base cuando el perfil solo define el esfuerzo.
func TestNuevoAdaptadorMensajeUsaPerfilCommit(t *testing.T) {
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

	adapter, err := nuevoAdaptador(cfg, "opencode", "commit")
	if err != nil {
		t.Fatalf("nuevoAdaptador devolvió error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("se esperaba *CLIAdapter, obtuve %T", adapter)
	}
	if cli.nombreBase() != "opencode" {
		t.Errorf("binario = %q, esperado opencode", cli.BinaryName)
	}
	// El perfil commit define solo reasoning_effort: el modelo se hereda del agente.
	if cli.Config.Model != "base" {
		t.Errorf("modelo = %q, esperado heredar 'base' del agente", cli.Config.Model)
	}
	if cli.Config.ReasoningEffort != "low" {
		t.Errorf("esfuerzo = %q, esperado 'low' del perfil commit", cli.Config.ReasoningEffort)
	}
}

// TestNuevoAdaptadorMensajeSinPerfilCaeAlBase verifica la compatibilidad:
// cuando el agente no define el perfil commit, el adaptador queda con la misma
// configuración que devolvería NewAgentAdapterNamed (modelo/esfuerzo base).
func TestNuevoAdaptadorMensajeSinPerfilCaeAlBase(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "opencode",
		Agents: map[string]config.AgentConfig{
			"opencode": {Model: "deepseek-v4-flash-free", ReasoningEffort: "high"},
		},
	}

	adapter, err := nuevoAdaptador(cfg, "opencode", "commit")
	if err != nil {
		t.Fatalf("nuevoAdaptador devolvió error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("se esperaba *CLIAdapter, obtuve %T", adapter)
	}
	esperado := cfg.Agents["opencode"]
	if !reflect.DeepEqual(cli.Config, esperado) {
		t.Errorf("config = %+v, esperado %+v (igual que el agente)", cli.Config, esperado)
	}
}

// TestNuevoAdaptadorMensajeAgenteInexistente verifica que la variante de
// mensaje mantiene el error de NewAgentAdapterNamed para agentes no
// configurados.
func TestNuevoAdaptadorMensajeAgenteInexistente(t *testing.T) {
	cfg := config.Config{
		ActiveAgent: "opencode",
		Agents:      map[string]config.AgentConfig{"opencode": {Model: "base"}},
	}

	adapter, err := nuevoAdaptador(cfg, "agente-inexistente-xyz", "commit")
	if err == nil {
		t.Fatalf("se esperaba un error para agente no configurado, obtuve %T/%v", adapter, err)
	}
	esperado := `el agente "agente-inexistente-xyz" no está configurado en vassentinel.yml`
	if err.Error() != esperado {
		t.Errorf("error = %q, esperado %q (compatibilidad con NewAgentAdapterNamed)", err.Error(), esperado)
	}
}
