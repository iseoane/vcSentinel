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

// marcadorInicio y marcadorFin delimitan el bloque de reglas de volumen de
// forma estable entre versiones: init/uninit lo detectan y lo retiran por
// estos marcadores, no por el texto interior, así una futura versión puede
// reformular la redacción sin dejar de ser idempotente ni dejar huérfanos.
const marcadorInicio = "<!-- vas-sentinel:begin -->"
const marcadorFin = "<!-- vas-sentinel:end -->"

// cuerpoReglasVolumen es el texto visible de la regla, libre de cambiar de
// redacción entre versiones: la detección no depende de él.
const cuerpoReglasVolumen = "## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)\n- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: \"sentinel check\".\n- Si el estado es \"CRÍTICO\" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.\n- Debes detenerte de inmediato e invocar: \"sentinel slice\" para fragmentar el código acumulado antes de continuar.\n"

// reglasVolumen es el bloque que 'init' inyecta hoy en AGENTS.md, CLAUDE.md y
// .claudecode.md, envuelto en marcadorInicio/marcadorFin.
const reglasVolumen = "\n" + marcadorInicio + "\n" + cuerpoReglasVolumen + marcadorFin + "\n"

// reglasVolumenLegado es el bloque EXACTO (sin marcadores) que todas las
// versiones anteriores a esta inyectaban. Se congela tal cual para siempre:
// nunca se vuelve a escribir en este formato, solo se reconoce y se retira,
// para poder limpiar y migrar los archivos que ya lo tienen de versiones
// previas (incluidos los de este propio repo).
const reglasVolumenLegado = "\n## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)\n- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: \"sentinel check\".\n- Si el estado es \"CRÍTICO\" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.\n- Debes detenerte de inmediato e invocar: \"sentinel slice\" para fragmentar el código acumulado antes de continuar.\n"

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

	// Un argumento que el subcomando no admite corta antes de ejecutar nada:
	// vale más un error claro que una operación que parece haber obedecido a
	// un flag que en realidad ignoró (H1/B6).
	if mensaje := validarArgumentos(subcomando, os.Args[2:]); mensaje != "" {
		fmt.Println(mensaje)
		os.Exit(1)
	}

	switch subcomando {
	case "init":
		ejecutarInit(worktreeActual)
	case "uninit":
		ejecutarUninit(worktreeActual)
	case "check":
		requireInicializado(worktreeActual)
		ejecutarCheck(worktreeActual)
	case "slice":
		requireInicializado(worktreeActual)
		if len(os.Args) > 2 {
			switch os.Args[2] {
			case "plan":
				os.Exit(ejecutarSlicePlan(os.Stdout, os.Args[3:]))
			case "apply":
				os.Exit(ejecutarSliceApply(os.Stdout, os.Args[3:]))
			}
		}
		ejecutarSlice(worktreeActual)
	case "review":
		requireInicializado(worktreeActual)
		ejecutarReview(worktreeActual, os.Args[2:])
	case "gate":
		requireInicializado(worktreeActual)
		os.Exit(ejecutarGate(os.Stdout, worktreeActual, os.Args[2:]))
	case "lint":
		requireInicializado(worktreeActual)
		ejecutarLint(worktreeActual)
	case "rebase":
		requireInicializado(worktreeActual)
		ejecutarRebase()
	case "status":
		requireInicializado(worktreeActual)
		ejecutarStatus(worktreeActual, os.Args[2:])
	case "explain":
		requireInicializado(worktreeActual)
		if err := ejecutarExplain(os.Stdout, os.Args[2:]); err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}
	case "pr":
		requireInicializado(worktreeActual)
		ejecutarPr(worktreeActual, os.Args[2:])
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
		fmt.Printf("❌ Subcomando desconocido: '%s'. Usa 'version', 'help', 'init', 'uninit', 'check', 'slice', 'review', 'lint', 'rebase', 'status', 'explain', 'pr', 'install', 'upgrade' o 'uninstall'.\n", subcomando)
		os.Exit(1)
	}
}

// requireInicializado exige que el worktree tenga la configuración
// per-proyecto (sentinel init) antes de ejecutar cualquier subcomando que
// dependa de ella; sin ella, corta con exit 1 en vez de fallar más adelante
// con un error menos claro.
func requireInicializado(worktreeActual string) {
	if !setup.EstaInicializado(worktreeActual) {
		fmt.Println("❌ Este repositorio no ha sido inicializado con VAS Sentinel.")
		fmt.Println("Ejecuta 'sentinel init' para configurar el guardián en este proyecto.")
		os.Exit(1)
	}
}

func imprimirUso() {
	fmt.Println("🤖 VAS Sentinel: Guardián de Código Local")
	fmt.Println("Uso: sentinel [version | help | init | uninit | check | slice | review | lint | rebase | status | explain | pr | install | upgrade | uninstall]")
}

// imprimirAyuda muestra la ayuda de subcomandos construida por construirAyuda.
func imprimirAyuda() {
	fmt.Print(construirAyuda())
}

// construirAyuda devuelve el texto de la ayuda. Las descripciones de los
// subcomandos se envuelven a un ancho fijo con las continuaciones alineadas en
// la columna de descripción (14 espacios), para que nada invada la zona de
// argumentos de los ítems.
func construirAyuda() string {
	var b strings.Builder
	b.WriteString("🤖 VAS Sentinel: Guardián de Código Local\n")
	b.WriteString("Uso: sentinel [version | help | init | uninit | check | slice | review |\n")
	b.WriteString("             lint | rebase | status | explain | pr | install | upgrade | uninstall]\n\n")
	b.WriteString("Subcomandos:\n")
	imprimirItemAyuda(&b, "version", "Muestra la versión instalada.")
	imprimirItemAyuda(&b, "help", "Muestra esta ayuda.")
	imprimirItemAyuda(&b, "init", "Inyecta las reglas de volumen en tus agentes, crea la config per-proyecto e instala el hook pre-commit del repositorio. Se ejecuta siempre en la raíz del repositorio (redirige automáticamente desde un subdirectorio).")
	imprimirItemAyuda(&b, "uninit", "Reverte 'init' en este repositorio: retira las reglas de volumen, borra la config per-proyecto y elimina el hook pre-commit (solo si sigue siendo el que instaló VAS Sentinel).")
	imprimirItemAyuda(&b, "check", "Audita el volumen de líneas modificadas del worktree activo.")
	imprimirItemAyuda(&b, "slice", "Fragmenta las modificaciones en commits de máximo 400 líneas.")
	imprimirItemAyuda(&b, "", "slice plan [--json] propone sin commitear (exit 3 si hay decisiones).")
	imprimirItemAyuda(&b, "", "slice apply --plan X --answers Y ejecuta un plan ya aprobado.")
	imprimirItemAyuda(&b, "review", "Audita un commit (default HEAD) contra las dimensiones de su saco y guarda la ficha.")
	imprimirItemAyuda(&b, "", "Flags: <sha|HEAD~n> --dims a,b --all --chain --gate --profile X --answer \"...\" --timeout N.")
	imprimirItemAyuda(&b, "lint", "Ejecuta los comandos de lint_commands de la configuración.")
	imprimirItemAyuda(&b, "rebase", "Actualiza la rama con fetch + rebase contra su upstream (pide confirmación).")
	imprimirItemAyuda(&b, "status", "Resumen del guardián: volumen, fichas de auditoría y últimos eventos.")
	imprimirItemAyuda(&b, "", "Con --json emite JSON; con --prune borra fichas huérfanas.")
	imprimirItemAyuda(&b, "explain", "Explica el perfil, los detectores, el riesgo y la cohesión de un rango. Uso: explain [base..head] [--json].")
	imprimirItemAyuda(&b, "pr", "Crea un pull request con gh (passthrough).")
	imprimirItemAyuda(&b, "", "pr review analiza la rama sin publicar (matriz + decisión single/chain).")
	imprimirItemAyuda(&b, "", "Flags de pr review: --base X --only-unaudited --overview --json.")
	imprimirItemAyuda(&b, "install", "Descarga e instala la última release publicada desde GitHub.")
	imprimirItemAyuda(&b, "upgrade", "Reemplaza el binario actual por la última release publicada.")
	imprimirItemAyuda(&b, "uninstall", "Elimina el binario instalado y la configuración global.")
	b.WriteString("\nFlags:\n")
	b.WriteString("  --version, -v   Muestra la versión instalada (equivalente a 'version').\n")
	b.WriteString("  --help, -h      Muestra esta ayuda (equivalente a 'help').\n")
	return b.String()
}

// imprimirItemAyuda añade una entrada de subcomando: si nombre es no vacío,
// se coloca en la columna de comando (2 espacios + hasta 12 de nombre); las
// líneas siguientes se alinean en la columna de descripción. La descripción se
// envuelve a un ancho acorde para no superar el ancho máximo de línea.
func imprimirItemAyuda(b *strings.Builder, nombre, descripcion string) {
	const anchoDescripcion = 96
	if nombre == "" {
		for _, linea := range envolver(descripcion, anchoDescripcion) {
			b.WriteString("              " + linea + "\n")
		}
		return
	}
	for i, linea := range envolver(descripcion, anchoDescripcion) {
		if i == 0 {
			fmt.Fprintf(b, "  %-12s%s\n", nombre, linea)
		} else {
			b.WriteString("              " + linea + "\n")
		}
	}
}

// envolver divide texto en líneas de como máximo ancho caracteres cortando por
// los espacios y sin cortar palabras. Devuelve al menos una línea (vacía si el
// texto lo es).
func envolver(texto string, ancho int) []string {
	var lineas []string
	actual := ""
	for _, palabra := range strings.Fields(texto) {
		if actual == "" {
			actual = palabra
			continue
		}
		if len(actual)+1+len(palabra) <= ancho {
			actual += " " + palabra
			continue
		}
		lineas = append(lineas, actual)
		actual = palabra
	}
	if actual != "" || len(lineas) == 0 {
		lineas = append(lineas, actual)
	}
	return lineas
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

	for _, nombre := range archivosObjetivo {
		escrito, err := inyectarReglasDeArchivo(filepath.Join(path, nombre))
		switch {
		case err != nil:
			fmt.Printf("⚠️ No se pudo inyectar en %s: %v\n", nombre, err)
		case escrito:
			fmt.Printf("📝 Reglas de volumen inyectadas en: %s\n", nombre)
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("❌ git no está en el PATH. Instálalo antes de ejecutar 'sentinel init'.")
		os.Exit(1)
	}

	// El hook se escribe directamente en el common-dir del repositorio (el
	// mismo para todos sus worktrees enlazados): sin carpeta global ni
	// core.hooksPath, Git ya lo detecta ahí por defecto y no afecta a ningún
	// otro repositorio del usuario.
	commonDir, err := git.ObtenerGitCommonDir(path)
	if err != nil {
		fmt.Printf("❌ No se pudo determinar el directorio Git del repositorio: %v\n", err)
		os.Exit(1)
	}
	hooksDir := filepath.Join(commonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		fmt.Printf("❌ No se pudo crear %s: %v\n", hooksDir, err)
		os.Exit(1)
	}

	scriptContent := generarScriptHook()
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(scriptContent), 0755); err != nil {
		fmt.Printf("❌ No se pudo escribir el hook en %s: %v\n", hookPath, err)
		os.Exit(1)
	}

	if err := setup.CrearConfiguracionPerProyecto(path); err != nil {
		fmt.Printf("⚠️ No se pudo crear la configuración per-proyecto: %v\n", err)
	} else {
		fmt.Println("📄 Configuración per-proyecto creada en: .vas_sentinel/vassentinel.yml")
	}

	fmt.Println("⚓ Git Hook 'pre-commit' instalado en este repositorio. Entorno securizado con éxito.")
}

// ejecutarUninit revierte en este repositorio exactamente lo que 'init' hizo:
// retira el bloque de reglas de AGENTS.md/CLAUDE.md/.claudecode.md, borra la
// config per-proyecto y elimina el hook 'pre-commit' — pero solo si su
// contenido coincide byte a byte con el que generarScriptHook produciría hoy;
// si no coincide (otra herramienta lo reemplazó, o es de otro origen), lo deja
// intacto y avisa en vez de borrar algo que no instaló VAS Sentinel.
func ejecutarUninit(path string) {
	raiz, err := git.ObtenerRaizWorktree()
	if err != nil {
		fmt.Println("❌ uninit debe ejecutarse dentro de un repositorio Git (no se encontró la raíz del worktree).")
		os.Exit(1)
	}
	if !git.EsMismaRuta(path, raiz) {
		fmt.Printf("📂 Detectada la raíz del repositorio: %s\n", raiz)
		fmt.Println("⚙️ Redirigiendo uninit a la raíz del repositorio...")
		path = raiz
	}

	fmt.Println("🗑️ Revirtiendo VAS Sentinel en este repositorio...")

	for _, nombre := range []string{"AGENTS.md", "CLAUDE.md", ".claudecode.md"} {
		retiradas, err := quitarReglasDeArchivo(filepath.Join(path, nombre))
		switch {
		case err != nil:
			fmt.Printf("⚠️ No se pudo limpiar %s: %v\n", nombre, err)
		case retiradas:
			fmt.Printf("📝 Reglas de volumen retiradas de: %s\n", nombre)
		}
	}

	rutaConfig := filepath.Join(path, ".vas_sentinel", "vassentinel.yml")
	if err := os.Remove(rutaConfig); err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("⚠️ No se pudo eliminar %s: %v\n", rutaConfig, err)
		}
	} else {
		fmt.Println("📄 Configuración per-proyecto eliminada: .vas_sentinel/vassentinel.yml")
	}

	commonDir, err := git.ObtenerGitCommonDir(path)
	if err != nil {
		fmt.Printf("⚠️ No se pudo determinar el directorio Git del repositorio: %v\n", err)
	} else {
		quitarHookSiEsDeSentinel(filepath.Join(commonDir, "hooks", "pre-commit"))
	}

	fmt.Println("✅ VAS Sentinel revertido en este repositorio.")
}

// inyectarReglasDeArchivo añade el bloque de reglas al final del archivo SOLO
// si todavía no está presente en formato marcado (init idempotente): ejecutar
// init dos veces no duplica la inserción, sea cual sea la versión que la
// escribió. Si el archivo trae el bloque legado (sin marcadores, de una
// versión anterior a esta), lo migra: retira TODAS sus apariciones —repara
// también los duplicados que dejaron versiones anteriores no idempotentes— y
// escribe un único bloque marcado. El archivo inexistente se crea con el
// bloque. Devuelve si escribió algo.
func inyectarReglasDeArchivo(ruta string) (bool, error) {
	datos, err := os.ReadFile(ruta)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	contenido := string(datos)

	if patronReglasVolumenMarcado.MatchString(contenido) {
		// Ya tiene el bloque marcado, sea de esta versión o de una futura que
		// comparta los mismos marcadores: nada que hacer.
		return false, nil
	}

	if patronReglasVolumenLegado.MatchString(contenido) {
		// quitarReglasVolumen (no un ReplaceAllString directo) porque retira
		// hasta el punto fijo: necesario cuando dos copias legadas están
		// pegadas con un único salto de línea de separación, ver
		// quitarTodasLasCoincidencias en reglasvolumen.go.
		sinLegado := quitarReglasVolumen(contenido)
		nuevo := sinLegado + reglasVolumenPara(sinLegado)
		return true, os.WriteFile(ruta, []byte(nuevo), 0644)
	}

	f, err := os.OpenFile(ruta, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(reglasVolumenPara(contenido)); err != nil {
		return false, err
	}
	return true, nil
}

// quitarReglasDeArchivo retira TODAS las apariciones del bloque reglasVolumen
// del archivo si está presente y devuelve si hizo algún cambio. Un archivo
// inexistente o sin el bloque no es error: simplemente no había nada que
// retirar. Retirar todas las apariciones repara también los duplicados que
// dejaron versiones anteriores de init.
func quitarReglasDeArchivo(ruta string) (bool, error) {
	datos, err := os.ReadFile(ruta)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !contieneReglasVolumen(string(datos)) {
		return false, nil
	}
	nuevo := quitarReglasVolumen(string(datos))
	if strings.TrimSpace(nuevo) == "" {
		// El archivo no tenía contenido propio: init lo creó solo para el
		// bloque de reglas, así que uninit lo borra en vez de dejarlo vacío.
		return true, os.Remove(ruta)
	}
	return true, os.WriteFile(ruta, []byte(nuevo), 0644)
}

// quitarHookSiEsDeSentinel borra el hook pre-commit en hookPath solo si su
// contenido coincide exactamente con el que generarScriptHook produce ahora;
// si difiere (otro origen) o no existe, no lo toca.
func quitarHookSiEsDeSentinel(hookPath string) {
	actual, err := os.ReadFile(hookPath)
	switch {
	case os.IsNotExist(err):
		return
	case err != nil:
		fmt.Printf("⚠️ No se pudo leer el hook existente: %v\n", err)
		return
	case string(actual) != generarScriptHook():
		fmt.Println("⚠️ El hook 'pre-commit' actual no coincide con el instalado por VAS Sentinel: no se toca.")
		return
	}
	if err := os.Remove(hookPath); err != nil {
		fmt.Printf("⚠️ No se pudo eliminar el hook: %v\n", err)
		return
	}
	fmt.Println("⚓ Hook 'pre-commit' eliminado.")
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
	volumen, err := git.MedirVolumen()
	if err != nil {
		fmt.Printf("❌ Error en Git: %v\n", err)
		os.Exit(1)
	}
	status := volumen.Estado

	fmt.Printf("📊 Líneas añadidas de código en este Worktree: %d [%s]\n", volumen.Bloqueante, status)
	if volumen.Informativo > 0 {
		// La documentación y lo generado se informan pero no frenan: el
		// guardián mide revisabilidad de código, no bytes.
		fmt.Printf("📄 Además, %d líneas de documentación y archivos generados (no cuentan para el límite).\n", volumen.Informativo)
	}
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

		plan, err := git.ConstruirPlanFragmentacionConLector(archivos, construirDecisionGigante(path), ejecutarGitParaChange)
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

			// Un EOF aquí (stdin cerrado) no hace bypass ni nada por defecto:
			// se propaga como error y ConstruirPlanFragmentacion aborta la
			// fragmentación. aprobarYEjecutar aplica el mismo criterio ante
			// EOF (cancela, ver B9): mantén ambos diálogos coherentes ante un
			// stdin cerrado si se modifica cualquiera de los dos.
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
	if !permiteDiffAgenteExterno(path) {
		fmt.Println("ℹ️ No se envía código al agente: allow_external_agent_diff está desactivado; se usan mensajes deterministas locales.")
		git.AplicarMensajesAutomaticos(plan)
		return nil, false
	}
	adapter, err := agentadapter.NewAgentAdapterParaMensaje(path)
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
			adapter, err := agentadapter.NewAgentAdapterNamedParaMensaje(path, nombre)
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

// accion representa la decisión tomada en el menú de aprobación del plan de
// fragmentación, separada de la lectura de stdin y de su ejecución para que
// decidirAccion se pueda probar sin simular entrada ni tocar el plan ni git.
type accion int

const (
	accionInvalida accion = iota
	accionAprobar
	accionRegenerar
	accionEditar
	accionCancelar
)

// decidirAccion traduce la línea leída del menú de aprobación a la acción
// correspondiente. Es una función pura: no lee stdin ni tiene efectos.
func decidirAccion(linea string) accion {
	switch strings.ToLower(strings.TrimSpace(linea)) {
	case "", "a", "aprobar":
		return accionAprobar
	case "r", "regenerar":
		return accionRegenerar
	case "e", "editar":
		return accionEditar
	case "c", "cancelar":
		return accionCancelar
	default:
		return accionInvalida
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

		// B9 (CRITICAL): un error de leerLinea (EOF con stdin cerrado — CI,
		// nohup, una tubería que termina, un agente sin consola) NO es una
		// respuesta del usuario y no puede tratarse como línea vacía: la
		// línea vacía real (Enter, sin error) sí debe aprobar, porque es el
		// default que anuncia el propio menú ("Opción [A]:"). Por eso la
		// decisión de EOF se toma aquí, antes de decidirAccion — decidirAccion
		// sigue siendo una función pura que solo clasifica texto, nunca debe
		// conocer el estado de error de la lectura. Este mismo criterio
		// (EOF cancela, no aprueba ni hace bypass) es el que ya aplicaba
		// construirDecisionGigante; que ningún cambio futuro vuelva a
		// separarlos.
		linea, err := leerLinea()
		if err != nil {
			fmt.Println("\n⚠️ Entrada estándar cerrada (EOF). Operación cancelada, no se ha commiteado nada.")
			return false
		}

		switch decidirAccion(linea) {
		case accionAprobar:
			return ejecutarPlanAprobado(plan)
		case accionRegenerar:
			regenerarMensajeLoteInteractivo(plan, path)
		case accionEditar:
			editarMensajeLoteInteractivo(plan)
		case accionCancelar:
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
	if !permiteDiffAgenteExterno(path) {
		fmt.Println("⚠️ No se puede regenerar con un agente sin allow_external_agent_diff: el micro-diff expondría código fuente al proveedor externo.")
		return
	}
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
			adapter, err := agentadapter.NewAgentAdapterNamedParaMensaje(path, nombre)
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
