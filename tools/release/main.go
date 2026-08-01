package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const nombreArchivoRelease = "release.yml"

type asset struct {
	goos   string
	goarch string
}

func main() {
	if err := generarAssets(); err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
}

func generarAssets() error {
	version, assets, err := leerRelease(nombreArchivoRelease)
	if err != nil {
		return err
	}

	directorioSalida := filepath.Join("bin", version)
	if err := os.MkdirAll(directorioSalida, 0755); err != nil {
		return fmt.Errorf("no se pudo crear el directorio %s: %w", directorioSalida, err)
	}

	nombres := make([]string, 0, len(assets))
	for _, a := range assets {
		nombre := "sentinel-" + a.goos + "-" + a.goarch
		if a.goos == "windows" {
			nombre += ".exe"
		}
		nombres = append(nombres, nombre)
	}

	for i, a := range assets {
		fmt.Printf("🔨 Compilando sentinel-%s-%s ...\n", a.goos, a.goarch)
		if err := compilar(version, a, nombres[i]); err != nil {
			return err
		}
	}

	fmt.Printf("✅ Assets generados en bin/%s/: %s\n", version, strings.Join(nombres, ", "))
	return nil
}

func compilar(version string, a asset, nombre string) error {
	salida := filepath.Join("bin", version, nombre)
	cmd := exec.Command("go", "build",
		"-ldflags", fmt.Sprintf("-s -w -X main.version=%s", version),
		"-o", salida,
		"./cmd/main.go",
	)
	cmd.Env = append(os.Environ(), "GOOS="+a.goos, "GOARCH="+a.goarch)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("falló la compilación de sentinel-%s-%s: %w\n%s", a.goos, a.goarch, err, output)
	}
	return nil
}

func leerRelease(ruta string) (string, []asset, error) {
	contenido, err := os.ReadFile(ruta)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("no se encontró %s en el directorio actual. Ejecuta este comando desde la raíz del repositorio", ruta)
		}
		return "", nil, fmt.Errorf("no se pudo leer %s: %w", ruta, err)
	}

	var version string
	var assets []asset
	var goosPendiente string

	for _, linea := range strings.Split(string(contenido), "\n") {
		linea = strings.TrimSpace(linea)
		if linea == "" {
			continue
		}
		if strings.HasPrefix(linea, "version:") {
			version = extraerValor(linea)
			continue
		}
		if strings.HasPrefix(linea, "- goos:") {
			goosPendiente = extraerValor(strings.TrimSpace(strings.TrimPrefix(linea, "-")))
			continue
		}
		if strings.HasPrefix(linea, "goarch:") && goosPendiente != "" {
			assets = append(assets, asset{goos: goosPendiente, goarch: extraerValor(linea)})
			goosPendiente = ""
		}
	}

	if version == "" {
		return "", nil, fmt.Errorf("no se encontró la clave \"version\" en %s", ruta)
	}
	if len(assets) == 0 {
		return "", nil, fmt.Errorf("no se definieron assets (bloques \"goos\"/\"goarch\") en %s", ruta)
	}

	return version, assets, nil
}

func extraerValor(linea string) string {
	_, valor, _ := strings.Cut(linea, ":")
	valor = strings.TrimSpace(valor)
	valor = strings.Trim(valor, `"'`)
	return valor
}
