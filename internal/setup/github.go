package setup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const repoOwner = "ISeoane-Quental"
const repoName = "vas.sentinel"

// fallbackGoInstall habilita reintentar con go install cuando la descarga de
// la release falla. Los tests lo desactivan para no ejecutar compilaciones
// reales durante las pruebas de error.
var fallbackGoInstall = true

// tokenGitHub devuelve el token de GitHub configurado en el entorno. Un
// repositorio privado exige autenticación tanto para consultar la release como
// para descargar sus assets: el cliente usa este token en ambas peticiones.
func tokenGitHub() string {
	return os.Getenv("GITHUB_TOKEN")
}

// PrepararTokenGitHub asegura que la operación de instalación/actualización
// disponga de credenciales para repositorios privados. Orden de resolución:
//  1. Variable de entorno GITHUB_TOKEN (si ya está definida, no hace nada).
//  2. Token de gh CLI (gh auth token) cuando gh está autenticado.
//  3. Pregunta interactiva al usuario por si dispone de un token.
//
// Devuelve el token resuelto (posiblemente vacío si no hay credenciales).
func PrepararTokenGitHub() string {
	if tokenGitHub() != "" {
		return tokenGitHub()
	}
	if token := tokenDesdeGHCLI(); token != "" {
		os.Setenv("GITHUB_TOKEN", token)
		return token
	}
	return preguntarTokenGitHub()
}

// tokenDesdeGHCLI obtiene el token de gh CLI cuando el usuario ya está
// autenticado en el repositorio (por ejemplo, porque publica releases). Devuelve
// cadena vacía si gh no está disponible o no hay sesión iniciada.
func tokenDesdeGHCLI() string {
	cmd := exec.Command("gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// preguntarTokenGitHub pide el token por teclado cuando no se pudo resolver de
// otra forma. Devuelve cadena vacía si el usuario no dispone de token (o no
// hay terminal interactiva).
func preguntarTokenGitHub() string {
	if !esTerminalStdin() {
		return ""
	}
	fmt.Println("🔑 El repositorio es privado y no se encontró GITHUB_TOKEN en el entorno.")
	fmt.Println("   Si tienes un token de GitHub, pégalo a continuación (vacío para continuar sin token):")
	fmt.Print("   Token: ")

	linea, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return ""
	}
	token := strings.TrimSpace(linea)
	if token != "" {
		os.Setenv("GITHUB_TOKEN", token)
	}
	return token
}

// esTerminalStdin indica si la entrada estándar es una terminal interactiva.
// Evita que el prompt de token bloquee en tests, tuberías o ejecución no interactiva.
func esTerminalStdin() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	// URL es la dirección de la API de GitHub para el asset. Es la vía fiable
	// de descarga en repositorios privados, donde browser_download_url
	// responde 404 incluso con token.
	URL string `json:"url"`
}

type ReleaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

func obtenerUltimaRelease() (ReleaseInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", repoOwner, repoName)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("no se pudo construir la petición a GitHub: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vas-sentinel")
	if token := tokenGitHub(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("error de red al consultar la última release en GitHub: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		return ReleaseInfo{}, fmt.Errorf("no se encontró una release publicada en %s/%s (HTTP %d). El repositorio debe tener una release publicada con assets; si es privado, configura la variable de entorno GITHUB_TOKEN", repoOwner, repoName, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return ReleaseInfo{}, fmt.Errorf("GitHub respondió con estado HTTP %d al consultar la última release", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("no se pudo leer la respuesta de GitHub: %w", err)
	}

	var release ReleaseInfo
	if err := json.Unmarshal(body, &release); err != nil {
		return ReleaseInfo{}, fmt.Errorf("no se pudo interpretar la respuesta de GitHub: %w", err)
	}
	if release.TagName == "" {
		return ReleaseInfo{}, fmt.Errorf("la release consultada no contiene un tag_name válido")
	}

	return release, nil
}

func elegirAssetParaSO(assets []ReleaseAsset) (ReleaseAsset, error) {
	return elegirAssetParaSistema(runtime.GOOS, runtime.GOARCH, assets)
}

func elegirAssetParaSistema(goos string, goarch string, assets []ReleaseAsset) (ReleaseAsset, error) {
	nombreEsperado := "sentinel-" + goos + "-" + goarch
	if goos == "windows" {
		nombreEsperado += ".exe"
	}

	for _, asset := range assets {
		if strings.EqualFold(asset.Name, nombreEsperado) {
			return asset, nil
		}
	}

	nombres := make([]string, 0, len(assets))
	for _, asset := range assets {
		nombres = append(nombres, asset.Name)
	}
	return ReleaseAsset{}, fmt.Errorf("no se encontró un asset de release para tu sistema (%s/%s). Se esperaba el patrón %q. Assets disponibles: %s", goos, goarch, nombreEsperado, strings.Join(nombres, ", "))
}

// descargarBinario descarga un asset de release. En repositorios privados usa
// la URL de la API del asset (Accept: application/octet-stream) en lugar del
// browser_download_url, que responde 404 para ese caso.
func descargarBinario(asset ReleaseAsset, destPath string) error {
	url := asset.BrowserDownloadURL
	if asset.URL != "" {
		url = asset.URL
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("no se pudo construir la petición de descarga: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "vas-sentinel")
	if token := tokenGitHub(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error de red al descargar el binario: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("la descarga del binario falló con estado HTTP %d", resp.StatusCode)
	}

	dest, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("no se pudo crear el archivo destino %s: %w", destPath, err)
	}

	_, err = io.Copy(dest, resp.Body)
	if cerr := dest.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("no se pudo escribir el binario en %s: %w", destPath, err)
	}

	return nil
}
