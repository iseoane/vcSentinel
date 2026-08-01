package main

import (
	"fmt"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"os"
	"os/exec"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("🤖 VAS Sentinel: Guardián de Código Local")
		fmt.Println("Uso: sentinel [init | check | slice]")
		os.Exit(1)
	}

	subcomando := os.Args[1]

	worktreeActual, err := os.Getwd()
	if err != nil {
		fmt.Printf("❌ Error al identificar el directorio actual: %v\n", err)
		os.Exit(1)
	}

	switch subcomando {
	case "init":
		ejecutarInit(worktreeActual)
	case "check":
		ejecutarCheck(worktreeActual)
	case "slice":
		ejecutarSlice(worktreeActual)
	default:
		fmt.Printf("❌ Subcomando desconocido: '%s'. Usa 'init', 'check' o 'slice'.\n", subcomando)
		os.Exit(1)
	}
}

func ejecutarInit(path string) {
	fmt.Println("⚙️ Inicializando VAS Sentinel en este entorno...")

	archivosObjetivo := []string{".agentsmd", "claude.md", ".claudecode.md"}
	reglas := "\n## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)\n- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: \"sentinel check\".\n- Si el estado es \"CRÍTICO\" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.\n- Debes detenerte de inmediato e invocar: \"sentinel slice\" para fragmentar el código acumulado antes de continuar.\n"

	for _, nombre := range archivosObjetivo {
		f, err := os.OpenFile(path+"/"+nombre, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString(reglas)
			f.Close()
			fmt.Printf("📝 Reglas de volumen inyectadas en: %s\n", nombre)
		}
	}

	homeDir, _ := os.UserHomeDir()
	folderHooks := homeDir + "/.git_global_hooks"
	os.MkdirAll(folderHooks, 0755)

	exec.Command("git", "config", "--global", "core.hooksPath", folderHooks).Run()

	scriptContent := "#!/bin/sh\nsentinel check\n"
	os.WriteFile(folderHooks+"/pre-commit", []byte(scriptContent), 0755)

	fmt.Println("⚓ Git Hook global 'pre-commit' instalado. Entorno securizado con éxito.")
}

func ejecutarCheck(path string) {
	totalLines, status, err := git.CheckDiffLimits()
	if err != nil {
		fmt.Printf("❌ Error en Git: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("📊 Líneas modificadas en este Worktree: %d [%s]\n", totalLines, status)
	if status == "CRITICO" {
		fmt.Println("⛔ ¡Peligro! El volumen supera las 400 líneas. Debes fragmentar con: sentinel slice")
		os.Exit(1)
	}
	fmt.Println("✅ Volumen bajo control. Podés continuar.")
}

func ejecutarSlice(path string) {
	fmt.Println("✂️ Iniciando algoritmo de partición determinista...")
	archivos, err := git.ObtenerArchivosModificados()
	if err != nil || len(archivos) == 0 {
		fmt.Println("📭 No hay modificaciones pendientes para procesar.")
		return
	}

	adapter := agentadapter.NewAgentAdapter(path)
	err = git.FragmentarYCommitear(archivos, adapter)
	if err != nil {
		fmt.Printf("❌ Error crítico durante la creación de commits: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n🎉 ¡Historial fragmentado con éxito! Tus cambios están en commits de máximo 400 líneas.")
}
