package agentadapter

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func setHome(t *testing.T, home string) {
	t.Helper()
	// os.UserHomeDir() usa HOME en Unix y USERPROFILE en Windows.
	claves := []string{"HOME"}
	if runtime.GOOS == "windows" {
		claves = append(claves, "USERPROFILE")
	}
	originales := make(map[string]string, len(claves))
	for _, clave := range claves {
		originales[clave] = os.Getenv(clave)
	}
	for _, clave := range claves {
		if err := os.Setenv(clave, home); err != nil {
			t.Fatalf("no se pudo fijar %s: %v", clave, err)
		}
	}
	t.Cleanup(func() {
		for clave, valor := range originales {
			os.Setenv(clave, valor)
		}
	})
}

func crearFalsoBinario(t *testing.T, dir string, nombre string) string {
	t.Helper()
	ruta := filepath.Join(dir, nombre)
	var contenido string
	if runtime.GOOS == "windows" {
		ruta += ".cmd"
		contenido = "@echo off\r\nexit /b 0\r\n"
	} else {
		contenido = "#!/bin/sh\nexit 0\n"
	}
	if err := os.WriteFile(ruta, []byte(contenido), 0755); err != nil {
		t.Fatalf("no se pudo crear el binario falso %s: %v", ruta, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(ruta, 0755); err != nil {
			t.Fatalf("no se pudieron asignar permisos a %s: %v", ruta, err)
		}
	}
	return ruta
}

func fijarPATH(t *testing.T, dirs ...string) {
	t.Helper()
	// PATH controlado y determinista: solo los directorios indicados.
	// No se reusa el PATH real para no depender de agentes instalados.
	valor := strings.Join(dirs, string(os.PathListSeparator))
	t.Setenv("PATH", valor)
}

func escribirConfigPerProyecto(t *testing.T, worktree string, contenido string) {
	t.Helper()
	ruta := filepath.Join(worktree, "sentinel", "vassentinel.yml")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatalf("no se pudo crear el directorio de config: %v", err)
	}
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir la config: %v", err)
	}
}

func TestNewAgentAdapterResuelveAutoDesdePATH(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	dirBinarios := t.TempDir()
	crearFalsoBinario(t, dirBinarios, "opencode")
	fijarPATH(t, dirBinarios)
	t.Setenv("MY_SUB_AGENT", "")

	adapter, err := NewAgentAdapter(worktree)
	if err != nil {
		t.Fatalf("NewAgentAdapter devolvió error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("se esperaba *CLIAdapter, obtuve %T", adapter)
	}
	if cli.BinaryName != "opencode" {
		t.Errorf("BinaryName esperado 'opencode', obtuve %q", cli.BinaryName)
	}
}

func TestNewAgentAdapterUsaActiveAgentDeConfig(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	dirBinarios := t.TempDir()
	crearFalsoBinario(t, dirBinarios, "claude")
	fijarPATH(t, dirBinarios)

	escribirConfigPerProyecto(t, worktree, "active_agent: \"claude\"\n")
	t.Setenv("MY_SUB_AGENT", "")

	adapter, err := NewAgentAdapter(worktree)
	if err != nil {
		t.Fatalf("NewAgentAdapter devolvió error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("se esperaba *CLIAdapter, obtuve %T", adapter)
	}
	if cli.BinaryName != "claude" {
		t.Errorf("BinaryName esperado 'claude', obtuve %q", cli.BinaryName)
	}
}

func TestNewAgentAdapterMYSudAgentPredomina(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	dirBinarios := t.TempDir()
	crearFalsoBinario(t, dirBinarios, "claude")
	fijarPATH(t, dirBinarios)

	// La config per-proyecto dice "opencode" pero MY_SUB_AGENT manda.
	escribirConfigPerProyecto(t, worktree, "active_agent: \"opencode\"\n")
	t.Setenv("MY_SUB_AGENT", "claude")

	adapter, err := NewAgentAdapter(worktree)
	if err != nil {
		t.Fatalf("NewAgentAdapter devolvió error: %v", err)
	}
	cli, ok := adapter.(*CLIAdapter)
	if !ok {
		t.Fatalf("se esperaba *CLIAdapter, obtuve %T", adapter)
	}
	if cli.BinaryName != "claude" {
		t.Errorf("BinaryName esperado 'claude' por MY_SUB_AGENT, obtuve %q", cli.BinaryName)
	}
}

func TestNewAgentAdapterSinBinariosDevuelveError(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	dirVacio := t.TempDir()
	fijarPATH(t, dirVacio)
	t.Setenv("MY_SUB_AGENT", "")

	_, err := NewAgentAdapter(worktree)
	if err == nil {
		t.Fatalf("se esperaba error sin agentes disponibles en el PATH")
	}
	if !strings.Contains(err.Error(), "ningun agente") {
		t.Errorf("el error debe mencionar 'ningun agente', obtuve: %v", err)
	}
}
