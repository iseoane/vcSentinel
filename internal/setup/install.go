package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const archivoConfiguracionBase = `version: "1.0"
active_agent: "auto"
agents:
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
    # Perfil 'commit' (mensajes de 'sentinel slice'): razonamiento bajo
    # porque nombrar un commit no necesita el razonamiento del resto de la
    # tarea. Ejemplo (descomenta y ajusta):
    # profiles:
    #   commit:
    #     reasoning_effort: "low"
  opencode:
    model: "deepseek-v4-flash"
    reasoning_effort: "max"
    # profiles:
    #   commit:
    #     reasoning_effort: "low"
`

// archivoConfiguracionPerProyectoBase es el template que init escribe en el
// repo. Deliberadamente NO fija active_agent/agents con valores literales: si
// lo hiciera, sobreescribiría en todos los repos las preferencias definidas
// en la config global (defaults -> global -> per-proyecto), dejando el yml
// global sin ningún efecto real. Solo lo que el usuario descomente aquí
// sobreescribe la config global para este repo.
const archivoConfiguracionPerProyectoBase = `version: "1.0"
# Config per-proyecto de VAS Sentinel. Solo sobreescribe aquí lo que este
# repo necesite distinto de tu config global (~/.vas_sentinel/vassentinel.yml,
# creada por 'sentinel install'). Todo lo que no definas se resuelve desde ahí.
#
# Verificación determinista SIN agente: si defines estas listas, 'pr review'
# ejecuta los comandos directamente en la shell del sistema y NUNCA consulta
# al agente (el agente solo se ofrece cuando no hay nada configurado).
# Descomenta y ajusta:
# lint_commands:
#   - "go vet ./..."
# test_commands:
#   - "go test ./..."
# build_commands:
#   - "go build ./..."
#
# Validación por paquetes: habilita scope solo en comandos que lo soporten de
# verdad. go test acepta import paths; gofmt, go vet y go build quedan completos.
# validation:
#   capabilities:
#     unit_test:
#       command: "go test ./..."
#       supports_scope: true
#       scoped_command: "go test {packages}"
#   profiles:
#     standard: [unit_test]
#   mode: worktree
#
# Idioma de los mensajes de commit que genera 'sentinel slice'. Por defecto
# "es" (el del historial de este repositorio). Sin fijarlo, el agente lo
# elegía al azar y mezclaba idiomas dentro de la misma fragmentación.
# commit_language: "en"
#
# Solicitud del repositorio: permite generar mensajes semánticos externos, pero
# NO es consentimiento personal. También se requiere el grant local y no
# versionado de 'sentinel consentimiento-diff otorgar'.
request_external_agent_diff: false
#
# Contexto CodeGraph para el revisor: solo metadatos de rutas de tests
# afectadas. Requiere además el consentimiento local anterior.
review:
  codegraph_context: false
#
# Ejemplo (descomenta y ajusta):
# active_agent: "claude"
# agents:
#   claude:
#     model: "claude-opus"
#     reasoning_effort: "high"
#     profiles:
#       commit:
#         reasoning_effort: "low"
# (El perfil 'commit' define el modelo/esfuerzo que 'sentinel slice' usa para
# generar los mensajes de commit; si no lo defines, se usa el modelo/esfuerzo
# base del agente sin ningún cambio de comportamiento.)
`

func EjecutarInstalacionCompleta() error {
	fmt.Println("⬇️ Descargando la última versión desde GitHub...")

	PrepararTokenGitHub()

	release, err := obtenerUltimaRelease()
	if err != nil {
		if fallbackGoInstall {
			if errFallback := InstalarViaGoInstall(); errFallback != nil {
				return fmt.Errorf("%v\nAdemás, el fallback con go install falló: %v", err, errFallback)
			}
			return nil
		}
		return err
	}

	asset, err := elegirAssetParaSO(release.Assets)
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp("", "sentinel-download-*")
	if err != nil {
		return fmt.Errorf("no se pudo crear el archivo temporal de descarga: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := descargarBinario(asset, tmpPath); err != nil {
		if fallbackGoInstall {
			if errFallback := InstalarViaGoInstall(); errFallback != nil {
				return fmt.Errorf("%v\nAdemás, el fallback con go install falló: %v", err, errFallback)
			}
			return nil
		}
		return err
	}

	var destino string
	switch runtime.GOOS {
	case "windows":
		destino, err = instalarWindows(tmpPath)
	default:
		destino, err = instalarLinux(tmpPath)
	}
	if err != nil {
		return err
	}

	if err := crearConfiguracionGlobal(); err != nil {
		return err
	}

	fmt.Printf("✅ Instalado en: %s\n", destino)
	return nil
}

// InstalarViaGoInstall es el fallback de instalación cuando la descarga desde
// la release falla (por ejemplo, repositorio privado sin token). Compila e
// instala desde el código fuente con go install y deja el binario en el mismo
// destino que una instalación normal.
func InstalarViaGoInstall() error {
	fmt.Println("⬇️ La descarga no está disponible. Reintentando con go install (compila desde el código fuente)...")

	output, err := ejecutarGoInstall()
	if err != nil {
		return fmt.Errorf("go install falló (¿GOPRIVATE y credenciales git configuradas?): %w\n%s", err, output)
	}

	gopath, err := goEnvGOPATH()
	if err != nil {
		return err
	}
	binario := filepath.Join(gopath, "bin", nombreBinarioGo())

	if err := instalarBinarioCompilado(binario); err != nil {
		return err
	}
	return nil
}

// ejecutarGoInstall compila e instala la última versión con go install. Para
// repositorios privados configura GOPRIVATE (evita consultar sum.golang.org) y
// entrega las credenciales a git vía GIT_CONFIG_COUNT, sin tocar la
// configuración global del usuario.
//
// Intenta primero la última release (@latest) y, si esa versión no contiene el
// paquete (release anterior a un cambio de estructura), cae a la rama main.
func ejecutarGoInstall() ([]byte, error) {
	PrepararTokenGitHub()

	output, err := goInstallPaquete("github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest")
	if err != nil && strings.Contains(string(output), "does not contain package") {
		fmt.Println("⚠️ La última release no incluye el paquete actual. Probando con la rama main...")
		output, err = goInstallPaquete("github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@main")
	}
	return output, err
}

// goInstallPaquete ejecuta go install de un paquete con el entorno preparado
// para repositorios privados (GOPRIVATE + credenciales git temporales).
func goInstallPaquete(paquete string) ([]byte, error) {
	cmd := exec.Command("go", "install", paquete)
	cmd.Env = append(os.Environ(), "GOPRIVATE=github.com/ISeoane-Quental/*")

	if token := tokenGitHub(); token != "" {
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=url.https://x-access-token:"+token+"@github.com/.insteadOf",
			"GIT_CONFIG_VALUE_0=https://github.com/",
		)
	}

	return cmd.CombinedOutput()
}

// instalarBinarioCompilado instala un binario ya compilado en el destino del
// sistema y crea la configuración global, igual que el flujo normal.
func instalarBinarioCompilado(binario string) error {
	var destino string
	var err error
	switch runtime.GOOS {
	case "windows":
		destino, err = instalarWindows(binario)
	default:
		destino, err = instalarLinux(binario)
	}
	if err != nil {
		return err
	}

	if err := crearConfiguracionGlobal(); err != nil {
		return err
	}

	fmt.Printf("✅ Instalado en: %s (vía go install)\n", destino)
	return nil
}

// goEnvGOPATH devuelve el GOPATH configurado.
func goEnvGOPATH() (string, error) {
	cmd := exec.Command("go", "env", "GOPATH")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no se pudo consultar GOPATH: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// nombreBinarioGo devuelve el nombre del binario que genera go install.
func nombreBinarioGo() string {
	if runtime.GOOS == "windows" {
		return "sentinel.exe"
	}
	return "sentinel"
}

func rutaBinarioWindows(homeDir string) string {
	return filepath.Join(homeDir, ".vas_sentinel", "bin", "sentinel.exe")
}

func instalarWindows(tmpPath string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no se pudo identificar el directorio del usuario: %w", err)
	}

	dir := filepath.Join(homeDir, ".vas_sentinel", "bin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("no se pudo crear el directorio %s: %w", dir, err)
	}

	destino := rutaBinarioWindows(homeDir)
	if err := moverYReemplazar(tmpPath, destino); err != nil {
		return "", fmt.Errorf("no se pudo instalar el binario en %s: %w", destino, err)
	}

	if err := anadirPathWindows(dir); err != nil {
		return "", err
	}

	return destino, nil
}

func necesitaAnadirPathWindows(pathActual string, dir string) bool {
	return !strings.Contains(pathActual, dir)
}

func anadirPathWindows(dir string) error {
	pathActual, err := obtenerPathUsuarioWindows()
	if err != nil {
		return err
	}
	if !necesitaAnadirPathWindows(pathActual, dir) {
		return nil
	}

	nuevoPath := pathActual + ";" + dir
	comando := fmt.Sprintf("[Environment]::SetEnvironmentVariable('Path', '%s', 'User')", nuevoPath)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", comando)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("no se pudo añadir %s al PATH de usuario: %w", dir, err)
	}

	fmt.Printf("🛣️ Ruta añadida al PATH de usuario: %s\n", dir)
	return nil
}

func obtenerPathUsuarioWindows() (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", "[Environment]::GetEnvironmentVariable('Path','User')")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no se pudo consultar el PATH de usuario: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func rutaBinarioLinux() string {
	return "/usr/local/bin/sentinel"
}

func instalarLinux(tmpPath string) (string, error) {
	destino := rutaBinarioLinux()

	if err := moverYReemplazar(tmpPath, destino); err != nil {
		if err := ejecutarSudoMV(tmpPath, destino); err != nil {
			return "", fmt.Errorf("no se pudo instalar el binario en %s. Ejecuta la instalación con permisos de superusuario (por ejemplo: 'sudo mv %s %s' y luego 'sudo chmod 0755 %s'): %w", destino, tmpPath, destino, destino, err)
		}
	}

	if err := os.Chmod(destino, 0755); err != nil {
		if err := ejecutarSudoChmod(destino); err != nil {
			return "", fmt.Errorf("no se pudieron asignar permisos de ejecución a %s: %w", destino, err)
		}
	}

	if err := anadirRutaShell(); err != nil {
		return "", err
	}

	return destino, nil
}

func ejecutarSudoMV(origen string, destino string) error {
	cmd := exec.Command("sudo", "mv", origen, destino)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("falló el intento con sudo de mover el binario: %w", err)
	}
	return nil
}

func ejecutarSudoChmod(destino string) error {
	cmd := exec.Command("sudo", "chmod", "0755", destino)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("falló el intento con sudo de asignar permisos: %w", err)
	}
	return nil
}

func anadirRutaShell() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no se pudo identificar el directorio del usuario: %w", err)
	}

	zshrc := filepath.Join(homeDir, ".zshrc")
	bashrc := filepath.Join(homeDir, ".bashrc")

	rutas := make([]string, 0, 2)
	if existeArchivo(zshrc) {
		rutas = append(rutas, zshrc)
	}
	if existeArchivo(bashrc) {
		rutas = append(rutas, bashrc)
	}
	if len(rutas) == 0 {
		rutas = append(rutas, zshrc)
	}

	const linea = `export PATH="/usr/local/bin:$PATH"`

	for _, ruta := range rutas {
		contenido, err := os.ReadFile(ruta)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("no se pudo leer %s: %w", ruta, err)
		}
		if strings.Contains(string(contenido), linea) {
			continue
		}

		bloque := fmt.Sprintf("\n# VAS Sentinel\nexport PATH=\"/usr/local/bin:$PATH\"\n")
		f, err := os.OpenFile(ruta, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("no se pudo actualizar %s: %w", ruta, err)
		}
		if _, err := f.WriteString(bloque); err != nil {
			f.Close()
			return fmt.Errorf("no se pudo escribir en %s: %w", ruta, err)
		}
		f.Close()
	}

	return nil
}

func existeArchivo(ruta string) bool {
	info, err := os.Stat(ruta)
	return err == nil && !info.IsDir()
}

// EstaInicializado indica si el worktree tiene la configuración per-proyecto
// (.vas_sentinel/vassentinel.yml), es decir, si ya pasó por 'sentinel init'.
func EstaInicializado(worktreePath string) bool {
	ruta := filepath.Join(worktreePath, ".vas_sentinel", "vassentinel.yml")
	return existeArchivo(ruta)
}

// crearConfiguracionGlobal materializa el archivo de configuración global en
// ~/.vas_sentinel/vassentinel.yml si todavía no existe. No lo sobreescribe.
func crearConfiguracionGlobal() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no se pudo identificar el directorio del usuario: %w", err)
	}

	dir := filepath.Join(homeDir, ".vas_sentinel")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("no se pudo crear el directorio %s: %w", dir, err)
	}

	ruta := filepath.Join(dir, "vassentinel.yml")
	if existeArchivo(ruta) {
		return nil
	}
	if err := os.WriteFile(ruta, []byte(archivoConfiguracionBase), 0644); err != nil {
		return fmt.Errorf("no se pudo crear el archivo de configuración global %s: %w", ruta, err)
	}
	return nil
}

// CrearConfiguracionPerProyecto materializa el archivo de configuración
// per-proyecto en <worktree>/.vas_sentinel/vassentinel.yml si todavía no
// existe. No lo sobreescribe.
func CrearConfiguracionPerProyecto(worktreePath string) error {
	ruta := filepath.Join(worktreePath, ".vas_sentinel", "vassentinel.yml")
	if existeArchivo(ruta) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		return fmt.Errorf("no se pudo crear el directorio %s: %w", filepath.Dir(ruta), err)
	}
	if err := os.WriteFile(ruta, []byte(archivoConfiguracionPerProyectoBase), 0644); err != nil {
		return fmt.Errorf("no se pudo crear el archivo de configuración per-proyecto %s: %w", ruta, err)
	}
	return nil
}

func moverYReemplazar(origen string, destino string) error {
	if err := os.Rename(origen, destino); err == nil {
		return nil
	}
	contenido, err := os.ReadFile(origen)
	if err != nil {
		return err
	}
	if err := os.WriteFile(destino, contenido, 0755); err != nil {
		return err
	}
	return os.Remove(origen)
}
