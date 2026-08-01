package agentadapter

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConstruirPromptAgente(t *testing.T) {
	prompt := construirPromptAgente("backend", 2, []string{"a.go", "b.go"})

	if !strings.Contains(prompt, "backend") {
		t.Errorf("el prompt debe contener la capa, obtuve: %s", prompt)
	}
	if !strings.Contains(prompt, "Lote #2") {
		t.Errorf("el prompt debe contener el número de lote, obtuve: %s", prompt)
	}
	if !strings.Contains(prompt, "a.go b.go") {
		t.Errorf("el prompt debe unir los archivos con espacio, obtuve: %s", prompt)
	}
	if strings.Contains(prompt, "```") {
		t.Errorf("el prompt no debe contener marcas de markdown, obtuve: %s", prompt)
	}
	if strings.Contains(prompt, "\"") {
		t.Errorf("el prompt no debe contener comillas, obtuve: %s", prompt)
	}
}

func TestConstruirPromptAgenteSinArchivos(t *testing.T) {
	prompt := construirPromptAgente("test", 1, nil)
	if !strings.Contains(prompt, "[test]") || !strings.Contains(prompt, "Lote #1") {
		t.Errorf("prompt incompleto con archivos vacíos, obtuve: %s", prompt)
	}
	if strings.Contains(prompt, "\"") {
		t.Errorf("el prompt no debe contener comillas, obtuve: %s", prompt)
	}
}

func escribirFalsoBinarioConSalida(t *testing.T, dir string, nombre string, salida string) string {
	t.Helper()
	ruta := filepath.Join(dir, nombre)
	var contenido string
	if runtime.GOOS == "windows" {
		ruta += ".cmd"
		contenido = "@echo off\r\necho " + salida + "\r\nexit /b 0\r\n"
	} else {
		contenido = "#!/bin/sh\necho '" + salida + "'\nexit 0\n"
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

func TestObtenerMensajeCommitConBinarioFalso(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con binario falso en modo -short")
	}

	dirBinarios := t.TempDir()
	escribirFalsoBinarioConSalida(t, dirBinarios, "opencode", "fix: cambio de prueba")
	fijarPATH(t, dirBinarios)

	adapter := &CLIAdapter{BinaryName: "opencode"}
	mensaje, err := adapter.ObtenerMensajeCommit([]string{"a.go", "b.go"}, "backend", 1)
	if err != nil {
		t.Fatalf("ObtenerMensajeCommit devolvió error: %v", err)
	}
	if mensaje != "fix: cambio de prueba" {
		t.Errorf("mensaje esperado 'fix: cambio de prueba', obtuve %q", mensaje)
	}
}

func TestObtenerMensajeCommitConAgenteClaude(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con binario falso en modo -short")
	}

	dirBinarios := t.TempDir()
	escribirFalsoBinarioConSalida(t, dirBinarios, "claude", "refactor: ajuste menor")
	fijarPATH(t, dirBinarios)

	adapter := &CLIAdapter{BinaryName: "claude"}
	mensaje, err := adapter.ObtenerMensajeCommit([]string{"a.go"}, "config", 3)
	if err != nil {
		t.Fatalf("ObtenerMensajeCommit devolvió error: %v", err)
	}
	if mensaje != "refactor: ajuste menor" {
		t.Errorf("mensaje esperado 'refactor: ajuste menor', obtuve %q", mensaje)
	}
}

func TestObtenerMensajeCommitConBinarioQueFalla(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con binario falso en modo -short")
	}

	dirBinarios := t.TempDir()
	ruta := filepath.Join(dirBinarios, "opencode")
	if runtime.GOOS == "windows" {
		ruta += ".cmd"
		os.WriteFile(ruta, []byte("@echo off\r\nexit /b 1\r\n"), 0755)
	} else {
		os.WriteFile(ruta, []byte("#!/bin/sh\nexit 1\n"), 0755)
		os.Chmod(ruta, 0755)
	}
	fijarPATH(t, dirBinarios)

	adapter := &CLIAdapter{BinaryName: "opencode"}
	if _, err := adapter.ObtenerMensajeCommit([]string{"a.go"}, "backend", 1); err == nil {
		t.Fatalf("se esperaba error cuando el binario falla")
	}
}

func TestComandoArgsPorBinario(t *testing.T) {
	prompt := "mensaje de prueba"
	casos := []struct {
		nombre   string
		binario  string
		esperado []string
	}{
		{"claude usa -p", "claude", []string{"-p", prompt}},
		{"opencode usa run", "opencode", []string{"run", prompt}},
		{"rutas completas se normalizan", filepath.Join("ruta", "a", "opencode"), []string{"run", prompt}},
		{"extensiones de Windows se ignoran", "opencode.exe", []string{"run", prompt}},
		{"ps1 se ignora para claude", "claude.ps1", []string{"-p", prompt}},
		{"binarios no reconocidos usan -p", "otro-agente", []string{"-p", prompt}},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			adapter := &CLIAdapter{BinaryName: caso.binario}
			obtenido := adapter.comandoArgs(prompt)
			if len(obtenido) != len(caso.esperado) {
				t.Fatalf("se esperaban %d argumentos (%v), obtuve %d (%v)", len(caso.esperado), caso.esperado, len(obtenido), obtenido)
			}
			for i := range caso.esperado {
				if obtenido[i] != caso.esperado[i] {
					t.Errorf("argumento %d esperado %q, obtuve %q", i, caso.esperado[i], obtenido[i])
				}
			}
		})
	}
}

func TestConstruirPromptRefactor(t *testing.T) {
	prompt := construirPromptRefactor("internal/git/plan.go")

	if !strings.Contains(prompt, "internal/git/plan.go") {
		t.Errorf("el prompt debe contener la ruta del archivo, obtuve: %s", prompt)
	}
	if strings.Contains(prompt, "```") {
		t.Errorf("el prompt no debe contener marcas de markdown, obtuve: %s", prompt)
	}
	if strings.Contains(prompt, "\"") {
		t.Errorf("el prompt no debe contener comillas, obtuve: %s", prompt)
	}
}

func TestProponerPlanRefactorConBinarioFalso(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con binario falso en modo -short")
	}

	dirBinarios := t.TempDir()
	escribirFalsoBinarioConSalida(t, dirBinarios, "opencode", "Crear internal/git/refactor.go con la lógica extraída.")
	fijarPATH(t, dirBinarios)

	adapter := &CLIAdapter{BinaryName: "opencode"}
	plan, err := adapter.ProponerPlanRefactor("internal/git/plan.go")
	if err != nil {
		t.Fatalf("ProponerPlanRefactor devolvió error: %v", err)
	}
	if !strings.Contains(plan, "refactor.go") {
		t.Errorf("plan esperado con refactor.go, obtuve %q", plan)
	}
}

func TestProponerPlanRefactorConBinarioQueFalla(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con binario falso en modo -short")
	}

	dirBinarios := t.TempDir()
	ruta := filepath.Join(dirBinarios, "claude")
	if runtime.GOOS == "windows" {
		ruta += ".cmd"
		os.WriteFile(ruta, []byte("@echo off\r\nexit /b 1\r\n"), 0755)
	} else {
		os.WriteFile(ruta, []byte("#!/bin/sh\nexit 1\n"), 0755)
		os.Chmod(ruta, 0755)
	}
	fijarPATH(t, dirBinarios)

	adapter := &CLIAdapter{BinaryName: "claude"}
	if _, err := adapter.ProponerPlanRefactor("internal/git/plan.go"); err == nil {
		t.Fatalf("se esperaba error cuando el binario falla")
	}
}
