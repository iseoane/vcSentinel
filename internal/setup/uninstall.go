package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// EjecutarDesinstalacionCompleta deshace la instalación global: elimina el
// binario, quita la ruta del PATH de usuario, limpia la configuración global y
// restaura los archivos de shell. No toca las configuraciones per-proyecto.
func EjecutarDesinstalacionCompleta() error {
	fmt.Println("🗑️ Desinstalando VAS Sentinel...")

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no se pudo identificar el directorio del usuario: %w", err)
	}

	switch runtime.GOOS {
	case "windows":
		if err := desinstalarWindows(homeDir); err != nil {
			return err
		}
	default:
		if err := desinstalarLinux(); err != nil {
			return err
		}
	}

	if err := eliminarConfiguracionGlobal(homeDir); err != nil {
		return err
	}

	if err := quitarRutaShell(); err != nil {
		return err
	}

	fmt.Println("✅ VAS Sentinel desinstalado.")
	fmt.Println("   El hook pre-commit instalado en cada repositorio (.git/hooks/pre-commit) no se elimina: gestiona hooks de git, no de sentinel.")
	return nil
}

func desinstalarWindows(homeDir string) error {
	dir := filepath.Join(homeDir, ".vas_sentinel", "bin")
	destino := rutaBinarioWindows(homeDir)

	if err := eliminarBinario(destino); err != nil {
		return err
	}

	if err := quitarPathWindows(dir); err != nil {
		return err
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("no se pudo eliminar el directorio %s: %w", dir, err)
	}
	return nil
}

func desinstalarLinux() error {
	if err := eliminarBinario(rutaBinarioLinux()); err != nil {
		return err
	}
	return nil
}

// eliminarBinario borra el binario si existe. Si está en uso (típico de
// Windows), sugiere el comando manual.
func eliminarBinario(ruta string) error {
	if _, err := os.Stat(ruta); os.IsNotExist(err) {
		fmt.Printf("ℹ️ No se encontró el binario en %s. Se continúa con el resto.\n", ruta)
		return nil
	}

	if err := os.Remove(ruta); err != nil {
		if runtime.GOOS == "windows" {
			return fmt.Errorf("no se pudo eliminar %s (probablemente está en uso). Cierra el proceso y borra el archivo manualmente: %w", ruta, err)
		}
		return fmt.Errorf("no se pudo eliminar %s: %w", ruta, err)
	}
	fmt.Printf("🗑️ Binario eliminado: %s\n", ruta)
	return nil
}

// quitarPathWindows elimina el directorio del PATH de usuario usando
// PowerShell, en el mismo formato en que se añadió.
func quitarPathWindows(dir string) error {
	pathActual, err := obtenerPathUsuarioWindows()
	if err != nil {
		return err
	}
	if !necesitaAnadirPathWindows(pathActual, dir) {
		return nil
	}

	comando := fmt.Sprintf("$env:Path = (($env:Path -split ';') | Where-Object { $_ -ne '%s' }) -join ';'; [Environment]::SetEnvironmentVariable('Path', $env:Path, 'User')", dir)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", comando)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("no se pudo quitar %s del PATH de usuario: %w", dir, err)
	}

	fmt.Printf("🛣️ Ruta eliminada del PATH de usuario: %s\n", dir)
	return nil
}

// quitarRutaShell elimina el bloque de export de /usr/local/bin de los
// archivos de shell en los que se haya añadido.
func quitarRutaShell() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no se pudo identificar el directorio del usuario: %w", err)
	}

	zshrc := filepath.Join(homeDir, ".zshrc")
	bashrc := filepath.Join(homeDir, ".bashrc")

	const linea = `export PATH="/usr/local/bin:$PATH"`
	bloque := "\n# VAS Sentinel\n" + linea + "\n"

	for _, ruta := range []string{zshrc, bashrc} {
		contenido, err := os.ReadFile(ruta)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("no se pudo leer %s: %w", ruta, err)
		}
		texto := string(contenido)
		if !strings.Contains(texto, bloque) {
			continue
		}

		limpio := strings.ReplaceAll(texto, bloque, "")
		if err := os.WriteFile(ruta, []byte(limpio), 0644); err != nil {
			return fmt.Errorf("no se pudo actualizar %s: %w", ruta, err)
		}
		fmt.Printf("🗑️ Bloque de VAS Sentinel eliminado de: %s\n", ruta)
	}

	return nil
}

// eliminarConfiguracionGlobal borra la configuración global y el directorio
// .vas_sentinel si quedó vacío.
func eliminarConfiguracionGlobal(homeDir string) error {
	dir := filepath.Join(homeDir, ".vas_sentinel")
	ruta := filepath.Join(dir, "vassentinel.yml")

	if _, err := os.Stat(ruta); err == nil {
		if err := os.Remove(ruta); err != nil {
			return fmt.Errorf("no se pudo eliminar la configuración global %s: %w", ruta, err)
		}
		fmt.Printf("🗑️ Configuración global eliminada: %s\n", ruta)
	}

	if contenido, err := os.ReadDir(dir); err == nil && len(contenido) == 0 {
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("no se pudo eliminar el directorio %s: %w", dir, err)
		}
	}
	return nil
}
