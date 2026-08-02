package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func EjecutarUpgradeDesdeGitHub() error {
	fmt.Println("🔄 Buscando la última versión...")

	PrepararTokenGitHub()

	release, err := obtenerUltimaRelease()
	if err != nil {
		if fallbackGoInstall {
			if errFallback := UpgradeViaGoInstall(); errFallback != nil {
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

	binarioActual, err := localizarBinarioActual()
	if err != nil {
		return err
	}

	fmt.Printf("⬇️ Descargando %s...\n", release.TagName)

	tmpFile, err := os.CreateTemp("", "sentinel-upgrade-*")
	if err != nil {
		return fmt.Errorf("no se pudo crear el archivo temporal de descarga: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := descargarBinario(asset, tmpPath); err != nil {
		if fallbackGoInstall {
			if errFallback := UpgradeViaGoInstall(); errFallback != nil {
				return fmt.Errorf("%v\nAdemás, el fallback con go install falló: %v", err, errFallback)
			}
			return nil
		}
		return err
	}

	var respaldo string
	switch runtime.GOOS {
	case "windows":
		respaldo, err = reemplazarWindows(tmpPath, binarioActual)
	default:
		err = reemplazarLinux(tmpPath, binarioActual)
	}
	if err != nil {
		return err
	}

	if err := verificarBinario(binarioActual); err != nil {
		if respaldo != "" {
			return fmt.Errorf("el binario pudo quedar corrupto y tu respaldo está en %s: %w", respaldo, err)
		}
		return fmt.Errorf("el binario pudo quedar corrupto: %w", err)
	}

	fmt.Printf("✅ Actualizado a %s\n", release.TagName)
	return nil
}

// UpgradeViaGoInstall es el fallback de actualización cuando la descarga desde
// la release falla (por ejemplo, repositorio privado sin token). Compila la
// última versión con go install y reemplaza el binario en ejecución.
func UpgradeViaGoInstall() error {
	fmt.Println("🔄 La descarga no está disponible. Reintentando con go install (compila desde el código fuente)...")

	binarioActual, err := localizarBinarioActual()
	if err != nil {
		return err
	}

	output, err := ejecutarGoInstall()
	if err != nil {
		return fmt.Errorf("go install falló (¿GOPRIVATE y credenciales git configuradas?): %w\n%s", err, output)
	}

	gopath, err := goEnvGOPATH()
	if err != nil {
		return err
	}
	binario := filepath.Join(gopath, "bin", nombreBinarioGo())

	var respaldo string
	switch runtime.GOOS {
	case "windows":
		respaldo, err = reemplazarWindows(binario, binarioActual)
	default:
		err = reemplazarLinux(binario, binarioActual)
	}
	if err != nil {
		return err
	}

	if err := verificarBinario(binarioActual); err != nil {
		if respaldo != "" {
			return fmt.Errorf("el binario pudo quedar corrupto y tu respaldo está en %s: %w", respaldo, err)
		}
		return fmt.Errorf("el binario pudo quedar corrupto: %w", err)
	}

	fmt.Println("✅ Actualizado vía go install")
	return nil
}

func localizarBinarioActual() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("no se pudo localizar el binario en ejecución: %w", err)
	}
	if resuelto, err := filepath.EvalSymlinks(exe); err == nil {
		return resuelto, nil
	}
	return exe, nil
}

func reemplazarWindows(tmpPath string, binarioActual string) (string, error) {
	pathRespaldado := binarioActual + ".old"
	if err := os.Rename(binarioActual, pathRespaldado); err != nil {
		return "", fmt.Errorf("no se pudo respaldar el binario actual en %s: %w", pathRespaldado, err)
	}

	if err := os.Rename(tmpPath, binarioActual); err != nil {
		os.Rename(pathRespaldado, binarioActual)
		return "", fmt.Errorf("no se pudo instalar la nueva versión en %s: %w", binarioActual, err)
	}

	if err := os.Remove(pathRespaldado); err != nil {
		fmt.Printf("⚠️ No se pudo eliminar el respaldo %s (está bloqueado). Puedes borrarlo manualmente: %v\n", pathRespaldado, err)
	}
	return pathRespaldado, nil
}

func reemplazarLinux(tmpPath string, binarioActual string) error {
	if err := os.Rename(tmpPath, binarioActual); err != nil {
		return fmt.Errorf("no se pudo reemplazar el binario actual en %s: %w", binarioActual, err)
	}
	if err := os.Chmod(binarioActual, 0755); err != nil {
		return fmt.Errorf("no se pudieron asignar permisos de ejecución al nuevo binario: %w", err)
	}
	return nil
}

func verificarBinario(binarioActual string) error {
	cmd := exec.Command(binarioActual, "--version")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("el nuevo binario no respondió a --version: %w", err)
	}
	version := strings.TrimSpace(string(out))
	if version != "" {
		fmt.Printf("ℹ️ Nueva versión instalada: %s\n", version)
	}
	return nil
}
