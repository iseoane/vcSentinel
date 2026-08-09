package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestContarLineasAnadidas(t *testing.T) {
	tests := []struct {
		nombre   string
		diff     string
		esperado int
	}{
		{nombre: "diff vacio", diff: "", esperado: 0},
		{nombre: "solo cabeceras de archivo", diff: "+++ b/main.go\n--- a/main.go\n", esperado: 0},
		{nombre: "lineas anadidas normales", diff: "+func main() {\n+\treturn\n+}\n", esperado: 3},
		{
			nombre:   "mezcla con contexto y borradas",
			diff:     " func main() {\n-\tfmt.Println(\"hola\")\n+\treturn\n \t_ = 0\n+++ b/main.go\n",
			esperado: 1,
		},
		{nombre: "linea con mas de tres signos mas es cabecera", diff: "+++++ no cabecera\n", esperado: 0},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := contarLineasAnadidas(tt.diff)
			if obtenido != tt.esperado {
				t.Errorf("contarLineasAnadidas(%q) = %d, esperado %d", tt.diff, obtenido, tt.esperado)
			}
		})
	}
}

func TestClasificarEstado(t *testing.T) {
	tests := []struct {
		nombre   string
		lineas   int
		esperado string
	}{
		{nombre: "cero", lineas: 0, esperado: "PEQUENO"},
		{nombre: "justo bajo el optimo", lineas: 199, esperado: "PEQUENO"},
		{nombre: "limite inferior del optimo", lineas: 200, esperado: "PUNTO_OPTIMO"},
		{nombre: "dentro del optimo", lineas: 300, esperado: "PUNTO_OPTIMO"},
		{nombre: "limite superior del optimo", lineas: 400, esperado: "PUNTO_OPTIMO"},
		{nombre: "sobre el limite critico", lineas: 401, esperado: "CRITICO"},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			obtenido := clasificarEstado(tt.lineas)
			if obtenido != tt.esperado {
				t.Errorf("clasificarEstado(%d) = %q, esperado %q", tt.lineas, obtenido, tt.esperado)
			}
		})
	}
}

func TestCheckDiffLimitsEnRepositorioReal(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	t.Run("estado limpio reporta cero y PEQUENO", func(t *testing.T) {
		lineas, estado, err := CheckDiffLimits()
		if err != nil {
			t.Fatalf("CheckDiffLimits devolvió error: %v", err)
		}
		if lineas != 0 {
			t.Errorf("líneas = %d, esperado 0", lineas)
		}
		if estado != "PEQUENO" {
			t.Errorf("estado = %q, esperado PEQUENO", estado)
		}
	})

	t.Run("250 lineas anadidas reportan PUNTO_OPTIMO", func(t *testing.T) {
		agregarLineas(t, "a.go", 250)
		lineas, estado, err := CheckDiffLimits()
		if err != nil {
			t.Fatalf("CheckDiffLimits devolvió error: %v", err)
		}
		if lineas != 250 {
			t.Errorf("líneas = %d, esperado 250", lineas)
		}
		if estado != "PUNTO_OPTIMO" {
			t.Errorf("estado = %q, esperado PUNTO_OPTIMO", estado)
		}
	})

	t.Run("mas de 400 lineas reportan CRITICO", func(t *testing.T) {
		agregarLineas(t, "a.go", 250)
		lineas, estado, err := CheckDiffLimits()
		if err != nil {
			t.Fatalf("CheckDiffLimits devolvió error: %v", err)
		}
		if lineas != 500 {
			t.Errorf("líneas = %d, esperado 500", lineas)
		}
		if estado != "CRITICO" {
			t.Errorf("estado = %q, esperado CRITICO", estado)
		}
	})
}

func TestCheckDiffLimitsIncluyeArchivosNoRastreados(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	// Archivo nuevo sin stagear (untracked) de más de 400 líneas: debe verse
	// como CRITICO, no como PEQUENO. Reproduce B1: "git diff HEAD" ignora los
	// archivos sin rastrear.
	contenido := strings.Repeat("// linea generada\n", 450)
	if err := os.WriteFile(filepath.Join(dir, "nuevo.go"), []byte(contenido), 0644); err != nil {
		t.Fatal(err)
	}

	lineas, estado, err := CheckDiffLimits()
	if err != nil {
		t.Fatalf("CheckDiffLimits devolvió error: %v", err)
	}
	if lineas != 450 {
		t.Errorf("líneas = %d, esperado 450", lineas)
	}
	if estado != "CRITICO" {
		t.Errorf("estado = %q, esperado CRITICO", estado)
	}
}

func TestCheckDiffLimitsCoincideConObtenerArchivosModificados(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	// Mezcla de archivo rastreado modificado y archivo nuevo sin rastrear:
	// check y slice deben medir exactamente el mismo volumen (B2).
	agregarLineas(t, "a.go", 100)
	contenidoNuevo := strings.Repeat("// linea\n", 50)
	if err := os.WriteFile(filepath.Join(dir, "nuevo.go"), []byte(contenidoNuevo), 0644); err != nil {
		t.Fatal(err)
	}

	lineas, _, err := CheckDiffLimits()
	if err != nil {
		t.Fatalf("CheckDiffLimits devolvió error: %v", err)
	}

	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		t.Fatalf("ObtenerArchivosModificados devolvió error: %v", err)
	}
	sumaEsperada := 0
	for _, a := range archivos {
		sumaEsperada += a.Lineas
	}

	if lineas != sumaEsperada {
		t.Errorf("CheckDiffLimits = %d, ObtenerArchivosModificados suma %d; deben coincidir", lineas, sumaEsperada)
	}
}

func prepararRepositorioPrueba(t *testing.T, archivos map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_AUTHOR_NAME", "VAS Sentinel Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@vas-sentinel")
	t.Setenv("GIT_COMMITTER_NAME", "VAS Sentinel Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@vas-sentinel")

	ejecutarGit(t, dir, "init", "-q")
	ejecutarGit(t, dir, "config", "commit.gpgsign", "false")
	ejecutarGit(t, dir, "config", "core.autocrlf", "false")
	ejecutarGit(t, dir, "config", "core.hooksPath", "no-hooks")

	for ruta, contenido := range archivos {
		if err := os.WriteFile(filepath.Join(dir, ruta), []byte(contenido), 0644); err != nil {
			t.Fatalf("no se pudo crear %s: %v", ruta, err)
		}
	}

	ejecutarGit(t, dir, "add", "-A")
	ejecutarGit(t, dir, "commit", "-q", "-m", "estado inicial")
	return dir
}

func ejecutarGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	comando := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", comando...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s falló: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func agregarLineas(t *testing.T, ruta string, cantidad int) {
	t.Helper()
	f, err := os.OpenFile(ruta, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("no se pudo abrir %s: %v", ruta, err)
	}
	defer f.Close()
	for i := 0; i < cantidad; i++ {
		if _, err := f.WriteString("// linea generada\n"); err != nil {
			t.Fatalf("no se pudo escribir en %s: %v", ruta, err)
		}
	}
}
