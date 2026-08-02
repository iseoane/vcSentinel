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

	if err := comprobarVersionPublicada(version); err != nil {
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

// comprobarVersionPublicada aborta la generación de assets cuando la versión de
// release.yml ya está publicada o es inferior a la última publicada. Consulta el
// tag de la última release con gh CLI; si no hay release previa (primera
// publicación) o gh no está disponible, permite continuar.
func comprobarVersionPublicada(version string) error {
	tag, err := obtenerTagPublicado()
	if err != nil {
		fmt.Printf("⚠️ No se pudo consultar la última release publicada: %v\n", err)
		return nil
	}
	if tag == "" {
		return nil
	}

	publicada := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if versionMenorOIgual(version, publicada) {
		return fmt.Errorf(
			"la versión %q no es superior a la ya publicada (%s). Debes incrementar la versión en %s antes de publicar",
			version, publicada, nombreArchivoRelease,
		)
	}
	fmt.Printf("✅ La versión %s es superior a la publicada (%s).", version, publicada)
	return nil
}

// obtenerTagPublicado devuelve el tag_name de la última release vía gh CLI
// (reutiliza la sesión autenticada de gh). Devuelve cadena vacía si aún no hay
// ninguna release publicada.
func obtenerTagPublicado() (string, error) {
	cmd := exec.Command("gh", "release", "view", "--json", "tagName", "--jq", ".tagName")
	out, err := cmd.CombinedOutput()
	if err != nil {
		salida := strings.TrimSpace(string(out))
		if strings.Contains(salida, "not found") || strings.Contains(salida, "Not Found") {
			return "", nil
		}
		return "", fmt.Errorf("gh release view falló: %v (salida: %s)", err, salida)
	}
	return strings.TrimSpace(string(out)), nil
}

// versionMenorOIgual compara dos versiones semver (major.minor.patch). Devuelve
// true si nueva <= publicada. Si alguna no tiene formato numérico reconocible,
// compara por longitud y lexicográficamente para no bloquear versiones raras.
func versionMenorOIgual(nueva string, publicada string) bool {
	n := versionComponentes(nueva)
	p := versionComponentes(publicada)
	limite := len(n)
	if len(p) > limite {
		limite = len(p)
	}
	for i := 0; i < limite; i++ {
		var ni, pi int
		if i < len(n) {
			ni = n[i]
		}
		if i < len(p) {
			pi = p[i]
		}
		if ni < pi {
			return true
		}
		if ni > pi {
			return false
		}
	}
	return true
}

// versionComponentes convierte "1.2.3" (o "v1.2.3-beta") en [1, 2, 3] ignorando
// prefijos "v" y sufijos no numéricos. Componentes no numéricos cuentan como 0.
func versionComponentes(v string) []int {
	partes := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	componentes := make([]int, 0, len(partes))
	for _, parte := range partes {
		num := 0
		for _, r := range parte {
			if r < '0' || r > '9' {
				break
			}
			num = num*10 + int(r-'0')
		}
		componentes = append(componentes, num)
	}
	return componentes
}

func compilar(version string, a asset, nombre string) error {
	salida := filepath.Join("bin", version, nombre)
	cmd := exec.Command("go", "build",
		"-ldflags", fmt.Sprintf("-s -w -X main.version=%s", version),
		"-o", salida,
		"./cmd/sentinel/main.go",
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
