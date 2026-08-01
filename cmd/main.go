package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/setup"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		imprimirUso()
		os.Exit(1)
	}

	subcomando := os.Args[1]

	switch subcomando {
	case "--version", "-v":
		fmt.Printf("📦 VAS Sentinel versión: %s\n", version)
		return
	case "--help", "-h", "help":
		imprimirAyuda()
		return
	}

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
	case "install":
		if err := setup.EjecutarInstalacionCompleta(); err != nil {
			fmt.Printf("❌ Error en la instalación: %v\n", err)
			os.Exit(1)
		}
	case "upgrade":
		if err := setup.EjecutarUpgradeDesdeGitHub(); err != nil {
			fmt.Printf("❌ Error en la actualización: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("❌ Subcomando desconocido: '%s'. Usa 'init', 'check', 'slice', 'install' o 'upgrade'.\n", subcomando)
		os.Exit(1)
	}
}

func imprimirUso() {
	fmt.Println("🤖 VAS Sentinel: Guardián de Código Local")
	fmt.Println("Uso: sentinel [init | check | slice | install | upgrade]")
}

func imprimirAyuda() {
	imprimirUso()
	fmt.Println()
	fmt.Println("Subcomandos:")
	fmt.Println("  init       Inyecta las reglas de volumen en tus agentes, crea la config per-proyecto e instala el hook global pre-commit.")
	fmt.Println("  check      Audita el volumen de líneas modificadas del worktree activo.")
	fmt.Println("  slice      Fragmenta las modificaciones en commits de máximo 400 líneas.")
	fmt.Println("  install    Descarga e instala la última release publicada desde GitHub.")
	fmt.Println("  upgrade    Reemplaza el binario actual por la última release publicada.")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --version, -v   Muestra la versión instalada.")
	fmt.Println("  --help, -h      Muestra esta ayuda.")
}

func ejecutarInit(path string) {
	fmt.Println("⚙️ Inicializando VAS Sentinel en este entorno...")

	archivosObjetivo := []string{"AGENTS.md", "CLAUDE.md", ".claudecode.md"}
	reglas := "\n## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)\n- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: \"sentinel check\".\n- Si el estado es \"CRÍTICO\" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.\n- Debes detenerte de inmediato e invocar: \"sentinel slice\" para fragmentar el código acumulado antes de continuar.\n"

	for _, nombre := range archivosObjetivo {
		f, err := os.OpenFile(filepath.Join(path, nombre), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			f.WriteString(reglas)
			f.Close()
			fmt.Printf("📝 Reglas de volumen inyectadas en: %s\n", nombre)
		}
	}

	homeDir, _ := os.UserHomeDir()
	folderHooks := filepath.Join(homeDir, ".git_global_hooks")
	os.MkdirAll(folderHooks, 0755)

	exec.Command("git", "config", "--global", "core.hooksPath", filepath.ToSlash(folderHooks)).Run()

	scriptContent := generarScriptHook()
	hookPath := filepath.Join(folderHooks, "pre-commit")
	os.WriteFile(hookPath, []byte(scriptContent), 0755)

	if err := setup.CrearConfiguracionPerProyecto(path); err != nil {
		fmt.Printf("⚠️ No se pudo crear la configuración per-proyecto: %v\n", err)
	} else {
		fmt.Println("📄 Configuración per-proyecto creada en: .vas_sentinel/vassentinel.yml")
	}

	fmt.Println("⚓ Git Hook global 'pre-commit' instalado. Entorno securizado con éxito.")
}

// generarScriptHook devuelve el contenido del hook pre-commit adaptado al
// sistema operativo donde se ejecuta el inicializador. Usa la ruta absoluta
// del binario en ejecución para no depender del PATH del shell del hook.
func generarScriptHook() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "sentinel"
	}
	exeAbs := filepath.ToSlash(exe)

	switch runtime.GOOS {
	case "windows":
		// Git for Windows ejecuta los hooks con su sh.exe: usa comillas
		// dobles para tolerar espacios en la ruta (p. ej. "C:/Program Files").
		return "#!/bin/sh\n\"" + exeAbs + "\" check\n"
	default:
		// Linux/macOS: sh estándar con la ruta absoluta del binario.
		return "#!/bin/sh\n\"" + exeAbs + "\" check\n"
	}
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
	fmt.Println("✅ Volumen bajo control. Puedes continuar.")
}

func ejecutarSlice(path string) {
	fmt.Println("✂️ Iniciando algoritmo de partición determinista...")
	archivos, err := git.ObtenerArchivosModificados()
	if err != nil || len(archivos) == 0 {
		fmt.Println("📭 No hay modificaciones pendientes para procesar.")
		return
	}

	adapter, err := agentadapter.NewAgentAdapter(path)
	if err != nil {
		fmt.Printf("❌ Error configurando el adaptador: %v\n", err)
		os.Exit(1)
	}
	err = git.FragmentarYCommitear(archivos, adapter)
	if err != nil {
		fmt.Printf("❌ Error crítico durante la creación de commits: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\n🎉 ¡Historial fragmentado con éxito! Tus cambios están en commits de máximo 400 líneas.")
}
