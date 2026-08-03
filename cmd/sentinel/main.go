package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

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
	case "--version", "-v", "version":
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
	case "review":
		ejecutarReview(worktreeActual, os.Args[2:])
	case "lint":
		ejecutarLint(worktreeActual)
	case "rebase":
		ejecutarRebase()
	case "status":
		ejecutarStatus(worktreeActual, os.Args[2:])
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
	case "uninstall":
		if err := setup.EjecutarDesinstalacionCompleta(); err != nil {
			fmt.Printf("❌ Error en la desinstalación: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("❌ Subcomando desconocido: '%s'. Usa 'version', 'help', 'init', 'check', 'slice', 'review', 'lint', 'rebase', 'status', 'install', 'upgrade' o 'uninstall'.\n", subcomando)
		os.Exit(1)
	}
}

func imprimirUso() {
	fmt.Println("🤖 VAS Sentinel: Guardián de Código Local")
	fmt.Println("Uso: sentinel [version | help | init | check | slice | review | lint | rebase | status | install | upgrade | uninstall]")
}

func imprimirAyuda() {
	imprimirUso()
	fmt.Println()
	fmt.Println("Subcomandos:")
	fmt.Println("  version    Muestra la versión instalada.")
	fmt.Println("  help       Muestra esta ayuda.")
	fmt.Println("  init       Inyecta las reglas de volumen en tus agentes, crea la config per-proyecto e instala el hook global pre-commit. Se ejecuta siempre en la raíz del repositorio (redirige automáticamente desde un subdirectorio).")
	fmt.Println("  check      Audita el volumen de líneas modificadas del worktree activo.")
	fmt.Println("  slice      Fragmenta las modificaciones en commits de máximo 400 líneas.")
	fmt.Println("  review     Audita un commit (default HEAD) contra las dimensiones de su saco y guarda la ficha. Flags: <sha|HEAD~n> --dims a,b --all --chain --gate --profile X --answer \"...\".")
	fmt.Println("  lint       Ejecuta los comandos de lint_commands de la configuración.")
	fmt.Println("  rebase     Actualiza la rama con fetch + rebase contra su upstream (pide confirmación).")
	fmt.Println("  status     Resumen del guardián: volumen, fichas de auditoría y últimos eventos. Con --json emite JSON.")
	fmt.Println("  install    Descarga e instala la última release publicada desde GitHub.")
	fmt.Println("  upgrade    Reemplaza el binario actual por la última release publicada.")
	fmt.Println("  uninstall  Elimina el binario instalado y la configuración global.")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --version, -v   Muestra la versión instalada (equivalente a 'version').")
	fmt.Println("  --help, -h      Muestra esta ayuda (equivalente a 'help').")
}

func ejecutarInit(path string) {
	raiz, err := git.ObtenerRaizWorktree()
	if err != nil {
		fmt.Println("❌ init debe ejecutarse dentro de un repositorio Git (no se encontró la raíz del worktree).")
		os.Exit(1)
	}
	if !git.EsMismaRuta(path, raiz) {
		fmt.Printf("📂 Detectada la raíz del repositorio: %s\n", raiz)
		fmt.Println("⚙️ Redirigiendo init a la raíz del repositorio...")
		path = raiz
	}

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

// errRefactorAplicado señala que un agente ya aplicó el plan de refactorización
// y que ejecutarSlice debe recalcular el plan con el working tree actualizado.
var errRefactorAplicado = errors.New("refactorización aplicada por el agente")

func ejecutarSlice(path string) {
	for {
		fmt.Println("✂️ Iniciando algoritmo de partición determinista...")
		archivos, err := git.ObtenerArchivosModificados()
		if err != nil || len(archivos) == 0 {
			fmt.Println("📭 No hay modificaciones pendientes para procesar.")
			return
		}

		plan, err := git.ConstruirPlanFragmentacion(archivos, construirDecisionGigante(path))
		if errors.Is(err, errRefactorAplicado) {
			fmt.Println("\n♻️ Refactorización aplicada. Recalculando el plan de fragmentación...")
			continue
		}
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}

		_, cancelado := elegirAdaptadorYGenerarMensajes(path, plan)
		if cancelado {
			fmt.Println("\n🚫 Operación cancelada. No se ha commiteado nada.")
			return
		}

		if !aprobarYEjecutar(plan, path) {
			fmt.Println("\n🚫 Operación cancelada. No se ha commiteado nada.")
			return
		}
		return
	}
}

// construirDecisionGigante devuelve el callback que decide qué hacer con un
// archivo de código masivo (>500 líneas) durante la construcción del plan:
// 1) refactorizar con IA, 2) hacer bypass IA o 3) abortar.
func construirDecisionGigante(path string) func(git.ArchivoModificado) (bool, error) {
	return func(f git.ArchivoModificado) (bool, error) {
		for {
			fmt.Printf("\n⚠️ El archivo %s tiene %d líneas y supera el máximo sugerido de %d.\n", f.Ruta, f.Lineas, git.LimiteCodigoGigante)
			fmt.Println("¿Cómo quieres proceder?")
			fmt.Println("  1) Refactorizar con IA: propone un plan de división del archivo (SRP)")
			fmt.Println("  2) Bypass IA: fragmentar el archivo tal cual (sin revisión)")
			fmt.Println("  3) Abortar la operación")
			fmt.Print("Opción (1-3): ")

			respuesta, err := leerLinea()
			if err != nil {
				return false, fmt.Errorf("error leyendo la opción: %w", err)
			}
			respuesta = strings.TrimSpace(respuesta)
			switch respuesta {
			case "1":
				refactorizado, err := refactorizarGigante(path, f)
				if err != nil {
					return false, err
				}
				if !refactorizado {
					continue
				}
				return false, fmt.Errorf("aplica el plan de refactorización propuesto y vuelve a ejecutar 'sentinel slice' para que el archivo ya dividido reingrese al plan")
			case "2":
				return true, nil
			case "3":
				return false, nil
			default:
				fmt.Println("Opción no válida. Elige 1, 2 o 3.")
			}
		}
	}
}

// refactorizarGigante pide al agente automático un plan de división para el
// archivo masivo, lo muestra y pregunta cómo aplicarlo: manualmente por el
// usuario, delegándolo en un agente (con fallback a otro agente si falla) o
// cancelar. Devuelve (false, nil) para volver al menú anterior; devuelve un
// error para abortar el slice (errRefactorAplicado si el agente ya aplicó el
// plan y hay que recalcular el plan).
func refactorizarGigante(path string, f git.ArchivoModificado) (bool, error) {
	adapter, err := agentadapter.NewAgentAdapter(path)
	if err != nil {
		fmt.Printf("⚠️ No se pudo crear el agente de refactorización: %v\n", err)
		return false, nil
	}

	refactorizador, ok := adapter.(agentadapter.AdapterRefactor)
	if !ok {
		fmt.Println("⚠️ El agente activo no soporta propuestas de refactorización.")
		return false, nil
	}

	if !git.VerificarAdaptador(adapter) {
		fmt.Println("⚠️ El agente activo no respondió correctamente.")
		return false, nil
	}

	fmt.Printf("🔍 Pidiendo al agente configurado un plan de división para %s...\n", f.Ruta)
	planRefactor, err := refactorizador.ProponerPlanRefactor(f.Ruta)
	if err != nil {
		fmt.Printf("⚠️ El agente falló al generar el plan: %v\n", err)
		return false, nil
	}
	if strings.TrimSpace(planRefactor) == "" {
		fmt.Println("⚠️ El agente devolvió un plan vacío.")
		return false, nil
	}

	fmt.Println("\n📋 Plan de división propuesto:")
	for _, linea := range strings.Split(planRefactor, "\n") {
		fmt.Printf("   %s\n", linea)
	}

	for {
		fmt.Println("\n¿Cómo quieres aplicar el plan?")
		fmt.Println("  1) Aplicarlo yo: lo aplicas manualmente y luego ejecutas de nuevo 'sentinel slice'")
		fmt.Println("  2) Delegarlo en un agente: el agente lo aplica y slice se re-ejecuta automáticamente")
		fmt.Println("  c) Cancelar la refactorización")
		fmt.Print("Opción (1, 2 o c): ")

		respuesta, err := leerLinea()
		if err != nil {
			return false, fmt.Errorf("error leyendo la opción: %w", err)
		}
		respuesta = strings.ToLower(strings.TrimSpace(respuesta))
		switch respuesta {
		case "1":
			return false, fmt.Errorf("aplica el plan de refactorización propuesto y vuelve a ejecutar 'sentinel slice' para que el archivo ya dividido reingrese al plan")
		case "2":
			aplicada, err := delegarRefactorizacion(path, f, planRefactor)
			if err != nil {
				return false, err
			}
			if aplicada {
				return false, errRefactorAplicado
			}
			return false, nil
		case "c", "cancelar":
			return false, nil
		default:
			fmt.Println("Opción no válida. Elige 1, 2 o c.")
		}
	}
}

// delegarRefactorizacion pide al agente automático aplicar el plan de división
// sobre el working tree. Si el agente automático no existe o falla, ofrece
// elegir otro agente disponible o cancelar. Devuelve true si un agente aplicó
// la refactorización.
func delegarRefactorizacion(path string, f git.ArchivoModificado, planRefactor string) (bool, error) {
	adapter, err := agentadapter.NewAgentAdapter(path)
	if err != nil || !git.VerificarAdaptador(adapter) {
		return bucleElegirAgenteRefactor(path, f, planRefactor)
	}

	refactorizador, ok := adapter.(agentadapter.AdapterRefactor)
	if !ok {
		return bucleElegirAgenteRefactor(path, f, planRefactor)
	}

	aplicada, err := aplicarRefactor(refactorizador, f.Ruta, planRefactor)
	if err != nil || !aplicada {
		return bucleElegirAgenteRefactor(path, f, planRefactor)
	}
	return true, nil
}

// bucleElegirAgenteRefactor ofrece elegir otro agente para aplicar el plan de
// refactorización cuando el agente automático no pudo, o cancelar la
// refactorización. Devuelve true si algún agente aplicó el plan.
func bucleElegirAgenteRefactor(path string, f git.ArchivoModificado, planRefactor string) (bool, error) {
	for {
		nombres := agentadapter.NombresAdaptadoresDisponibles(path)
		if len(nombres) == 0 {
			fmt.Println("⚠️ No hay agentes disponibles para aplicar la refactorización.")
			return false, nil
		}

		fmt.Println("\n⚠️ El agente automático no pudo aplicar el plan. Elige otro agente:")
		for i, nombre := range nombres {
			fmt.Printf("  %d) %s\n", i+1, nombre)
		}
		fmt.Println("  c) Cancelar la refactorización")
		fmt.Print("Opción: ")

		respuesta, err := leerLinea()
		if err != nil {
			return false, nil
		}
		respuesta = strings.ToLower(strings.TrimSpace(respuesta))
		if respuesta == "c" || respuesta == "cancelar" {
			return false, nil
		}

		numero, err := strconv.Atoi(respuesta)
		if err != nil || numero < 1 || numero > len(nombres) {
			fmt.Println("⚠️ Opción no válida. Intenta de nuevo.")
			continue
		}

		adapter, err := agentadapter.NewAgentAdapterNamed(path, nombres[numero-1])
		if err != nil {
			fmt.Printf("⚠️ No se pudo crear el adaptador para %s: %v\n", nombres[numero-1], err)
			continue
		}
		refactorizador, ok := adapter.(agentadapter.AdapterRefactor)
		if !ok {
			fmt.Printf("⚠️ El agente %s no soporta refactorizaciones.\n", nombres[numero-1])
			continue
		}
		if !git.VerificarAdaptador(adapter) {
			fmt.Printf("⚠️ El agente %s no respondió correctamente. Elige otra opción.\n", nombres[numero-1])
			continue
		}

		aplicada, err := aplicarRefactor(refactorizador, f.Ruta, planRefactor)
		if err != nil || !aplicada {
			fmt.Printf("⚠️ El agente %s falló al aplicar el plan. Elige otra opción.\n", nombres[numero-1])
			continue
		}
		return true, nil
	}
}

// aplicarRefactor ejecuta el plan de refactorización con el agente dado y
// confirma que devolvió una respuesta no vacía.
func aplicarRefactor(refactorizador agentadapter.AdapterRefactor, ruta string, planRefactor string) (bool, error) {
	fmt.Printf("🔧 Pidiendo al agente seleccionado aplicar el plan sobre %s...\n", ruta)
	resumen, err := refactorizador.AplicarPlanRefactor(ruta, planRefactor)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(resumen) == "" {
		return false, fmt.Errorf("el agente devolvió un resumen vacío")
	}
	fmt.Printf("✅ %s\n", strings.TrimSpace(resumen))
	return true, nil
}

// lectorStdin lee línea a línea la entrada estándar para los flujos interactivos.
var lectorStdin = bufio.NewReader(os.Stdin)

func leerLinea() (string, error) {
	linea, err := lectorStdin.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(linea, "\r\n"), nil
}

// elegirAdaptadorYGenerarMensajes selecciona el adaptador (auto por defecto),
// lo prueba una vez y genera los mensajes del plan. Si el adaptador automático
// no existe, falla la sonda o falla al generar algún mensaje, ofrece al usuario
// elegir mensajes automáticos, otro adaptador disponible o cancelar.
func elegirAdaptadorYGenerarMensajes(path string, plan *git.PlanFragmentacion) (agentadapter.AgentAdapter, bool) {
	adapter, err := agentadapter.NewAgentAdapter(path)
	if err != nil {
		fmt.Printf("⚠️ %v\n", err)
		return bucleElegirAdaptador(path, plan)
	}

	if git.VerificarAdaptador(adapter) {
		if fallbacks := git.GenerarMensajesLotes(plan, adapter); fallbacks == 0 {
			return adapter, false
		}
		fmt.Println("⚠️ El agente automático falló al generar algunos mensajes.")
		return bucleElegirAdaptador(path, plan)
	}

	fmt.Println("⚠️ El agente automático no respondió correctamente.")
	return bucleElegirAdaptador(path, plan)
}

// bucleElegirAdaptador ofrece la elección de origen de los mensajes cuando el
// adaptador automático no sirve: mensajes automáticos deterministas, un agente
// de los disponibles o cancelar. Devuelve el adaptador elegido (nil si se eligen
// mensajes automáticos) y si la operación quedó cancelada.
func bucleElegirAdaptador(path string, plan *git.PlanFragmentacion) (agentadapter.AgentAdapter, bool) {
	for {
		nombres := agentadapter.NombresAdaptadoresDisponibles(path)
		fmt.Println("\nElige cómo obtener los mensajes de commit:")
		fmt.Println("  1) Usar mensajes automáticos deterministas para todos los lotes")
		for i, nombre := range nombres {
			fmt.Printf("  %d) Usar el agente %s\n", i+2, nombre)
		}
		fmt.Println("  c) Cancelar la operación")
		fmt.Print("Opción: ")

		respuesta, err := leerLinea()
		if err != nil {
			return nil, true
		}
		respuesta = strings.ToLower(strings.TrimSpace(respuesta))

		switch {
		case respuesta == "1" || respuesta == "a" || respuesta == "auto":
			git.AplicarMensajesAutomaticos(plan)
			return nil, false
		case respuesta == "c" || respuesta == "cancelar":
			return nil, true
		default:
			numero, err := strconv.Atoi(respuesta)
			if err != nil || numero < 2 || numero > len(nombres)+1 {
				fmt.Println("⚠️ Opción no válida. Intenta de nuevo.")
				continue
			}
			nombre := nombres[numero-2]
			adapter, err := agentadapter.NewAgentAdapterNamed(path, nombre)
			if err != nil {
				fmt.Printf("⚠️ No se pudo crear el adaptador para %s: %v\n", nombre, err)
				continue
			}
			if !git.VerificarAdaptador(adapter) {
				fmt.Printf("⚠️ El agente %s no respondió correctamente. Elige otra opción.\n", nombre)
				continue
			}
			if fallbacks := git.GenerarMensajesLotes(plan, adapter); fallbacks > 0 {
				fmt.Printf("⚠️ El agente %s falló al generar algunos mensajes. Elige otra opción.\n", nombre)
				continue
			}
			return adapter, false
		}
	}
}

// aprobarYEjecutar muestra el plan propuesto y dirige el flujo de aprobación:
// aprobar todo, regenerar un mensaje con otro agente, editar un mensaje o
// cancelar. Devuelve false si el usuario canceló sin commitear nada.
func aprobarYEjecutar(plan *git.PlanFragmentacion, path string) bool {
	for {
		imprimirPlan(plan)
		fmt.Println("\nOpciones:")
		fmt.Println("  (A)probar todo y ejecutar (Enter)")
		fmt.Println("  (R)egenerar mensaje de un lote con otro agente")
		fmt.Println("  (E)ditar mensaje de un lote manualmente")
		fmt.Println("  (C)ancelar sin commitear nada")
		fmt.Print("Opción [A]: ")

		linea, err := leerLinea()
		if err != nil {
			linea = ""
		}
		opcion := strings.ToLower(strings.TrimSpace(linea))

		switch opcion {
		case "", "a", "aprobar":
			return ejecutarPlanAprobado(plan)
		case "r", "regenerar":
			regenerarMensajeLoteInteractivo(plan, path)
		case "e", "editar":
			editarMensajeLoteInteractivo(plan)
		case "c", "cancelar":
			return false
		default:
			fmt.Println("⚠️ Opción no válida. Usa A, R, E o C.")
		}
	}
}

func imprimirPlan(plan *git.PlanFragmentacion) {
	fmt.Println("\n📋 Plan de fragmentación propuesto (nada se ha commiteado todavía):")
	totalLineas := 0
	for _, lote := range plan.Lotes {
		sufijo := ""
		if lote.MensajeDeterminista {
			sufijo = " (automático)"
		}
		totalLineas += lote.LineasTotales
		fmt.Printf("  [%s] lote #%d — %d archivos (%d líneas) — mensaje: %s%s\n",
			lote.Capa, lote.Numero, len(lote.Rutas), lote.LineasTotales, lote.Mensaje, sufijo)
	}
	fmt.Printf("Total: %d lotes, %d líneas.\n", len(plan.Lotes), totalLineas)
}

func regenerarMensajeLoteInteractivo(plan *git.PlanFragmentacion, path string) {
	fmt.Print("¿Qué lote quieres regenerar? (número): ")
	linea, err := leerLinea()
	if err != nil {
		return
	}
	numero, err := strconv.Atoi(strings.TrimSpace(linea))
	if err != nil {
		fmt.Println("⚠️ Número de lote no válido.")
		return
	}

	nombres := agentadapter.NombresAdaptadoresDisponibles(path)
	for {
		fmt.Printf("¿Con qué agente regeneras el lote #%d?\n", numero)
		fmt.Println("  1) Mensaje automático determinista")
		for i, nombre := range nombres {
			fmt.Printf("  %d) %s\n", i+2, nombre)
		}
		fmt.Print("Opción: ")

		respuesta, err := leerLinea()
		if err != nil {
			return
		}
		respuesta = strings.ToLower(strings.TrimSpace(respuesta))

		switch {
		case respuesta == "1" || respuesta == "a" || respuesta == "auto":
			if err := git.AplicarMensajeAutomaticoLote(plan, numero); err != nil {
				fmt.Printf("⚠️ %v\n", err)
			}
			return
		case respuesta == "c" || respuesta == "cancelar":
			return
		default:
			indice, err := strconv.Atoi(respuesta)
			if err != nil || indice < 2 || indice > len(nombres)+1 {
				fmt.Println("⚠️ Opción no válida. Intenta de nuevo.")
				continue
			}
			nombre := nombres[indice-2]
			adapter, err := agentadapter.NewAgentAdapterNamed(path, nombre)
			if err != nil {
				fmt.Printf("⚠️ No se pudo crear el adaptador para %s: %v\n", nombre, err)
				continue
			}
			if !git.VerificarAdaptador(adapter) {
				fmt.Printf("⚠️ El agente %s no respondió correctamente. Elige otra opción.\n", nombre)
				continue
			}
			if err := git.RegenerarMensajeLote(plan, numero, adapter); err != nil {
				fmt.Printf("⚠️ %v\n", err)
			}
			return
		}
	}
}

func editarMensajeLoteInteractivo(plan *git.PlanFragmentacion) {
	fmt.Print("¿Qué lote quieres editar? (número): ")
	linea, err := leerLinea()
	if err != nil {
		return
	}
	numero, err := strconv.Atoi(strings.TrimSpace(linea))
	if err != nil {
		fmt.Println("⚠️ Número de lote no válido.")
		return
	}
	fmt.Print("Nuevo mensaje de commit: ")
	mensaje, err := leerLinea()
	if err != nil {
		return
	}
	if strings.TrimSpace(mensaje) == "" {
		fmt.Println("⚠️ El mensaje no puede estar vacío.")
		return
	}
	if err := git.EditarMensajeLote(plan, numero, mensaje); err != nil {
		fmt.Printf("⚠️ %v\n", err)
	}
}

func ejecutarPlanAprobado(plan *git.PlanFragmentacion) bool {
	resultados, err := git.EjecutarPlanFragmentacion(plan)
	if err != nil {
		fmt.Printf("❌ Error crítico durante la creación de commits: %v\n", err)
		os.Exit(1)
	}
	imprimirResumen(resultados)
	return true
}

func imprimirResumen(resultados []git.ResultadoCommit) {
	fmt.Printf("\n🎉 ¡Historial fragmentado con éxito! Se crearon %d commits.\n", len(resultados))
	for _, r := range resultados {
		fmt.Printf("  %s  [%s]  %d archivos  %s\n", r.Hash, r.Capa, r.Archivos, r.Mensaje)
	}
	limpio, err := git.WorktreeLimpio()
	switch {
	case err != nil:
		fmt.Printf("⚠️ No se pudo verificar el estado del worktree: %v\n", err)
	case limpio:
		fmt.Println("✅ El worktree está limpio. Volumen bajo control.")
	default:
		fmt.Println("⚠️ Quedan cambios pendientes en el worktree. Revisa con 'sentinel check'.")
	}
}
