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
  opencode:
    model: "deepseek-v4-flash"
    reasoning_effort: "max"
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

	if err := descargarBinario(asset.BrowserDownloadURL, tmpPath); err != nil {
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

	cmd := exec.Command("go", "install", "github.com/ISeoane-Quental/vas.sentinel/cmd/sentinel@latest")
	output, err := cmd.CombinedOutput()
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
	if err := os.WriteFile(ruta, []byte(archivoConfiguracionBase), 0644); err != nil {
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
