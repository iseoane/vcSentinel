package setup

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
)

const repoOwner = "ISeoane-Quental"
const repoName = "vas.sentinel"

type ReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
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
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
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

func descargarBinario(url string, destPath string) error {
	client := &http.Client{}
	resp, err := client.Get(url)
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
