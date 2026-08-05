package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prepararRepoTemp crea un repo git temporal con identidad local configurada y
// cambia al directorio de trabajo (los helpers de git operan sobre el cwd).
// Devuelve la ruta del repo para que el test la use con filepath.Join.
// PRECAUCIÓN: t.Chdir muta el cwd del proceso; los tests de este paquete no
// deben usar t.Parallel(), o el cwd se filtraría entre tests. Preferir
// os.Chdir con defer de restauración si algún día se necesita paralelismo.
func prepararRepoTemp(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)

	for _, cmd := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		// El core.hooksPath global del usuario instala el hook de volumen que
		// rechazaría los commits de prueba: se desactiva solo para el repo temp.
		{"config", "core.hooksPath", ""},
	} {
		if salida, err := ejecutarGitSalida(cmd...); err != nil {
			t.Fatalf("preparación %v falló: %v (%s)", cmd, err, salida)
		}
	}
	return repo
}

// commitEnRepo crea un commit con un archivo nuevo en el repo actual y
// devuelve su SHA completo.
func commitEnRepo(t *testing.T, nombre, contenido string) string {
	t.Helper()
	ruta := filepath.Join(nombre)
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", nombre, err)
	}
	ejecutar(t, "add", nombre)
	ejecutar(t, "commit", "-m", "feat("+nombre+"): contenido de prueba")
	sha, err := ejecutarGitSalida("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("no se pudo leer HEAD: %v", err)
	}
	return strings.TrimSpace(sha)
}

func ejecutar(t *testing.T, args ...string) {
	t.Helper()
	if _, err := ejecutarGitSalida(args...); err != nil {
		t.Fatalf("git %v falló: %v", args, err)
	}
}

// TestMergeBase verifica el ancestro común entre main y una rama creada desde
// un commit intermedio.
func TestMergeBase(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "a.txt", "base\n")
	intermedio := commitEnRepo(t, "b.txt", "segundo\n")
	ejecutar(t, "checkout", "-b", "feature")
	commitEnRepo(t, "c.txt", "rama\n")

	base, err := MergeBase("main", "HEAD")
	if err != nil {
		t.Fatalf("MergeBase falló: %v", err)
	}
	if base != intermedio {
		t.Errorf("MergeBase = %s, esperado %s (el commit desde el que se creó la rama)", base, intermedio)
	}
}

// TestNumstatRango suma añadidas + borradas del rango, ignorando binarios.
func TestNumstatRango(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "a.txt", strings.Repeat("x\n", 10))
	ejecutar(t, "checkout", "-b", "feature")

	// Sustituye las 10 líneas "x" por 15 líneas "y" en a.txt (10 borradas +
	// 15 añadidas reales) y crea b.txt con 3 líneas.
	if err := os.WriteFile("a.txt", []byte(strings.Repeat("y\n", 15)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("b.txt", []byte("1\n2\n3\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutar(t, "add", "-A")
	ejecutar(t, "commit", "-m", "feat: volumen de rama")

	vol, err := NumstatRango("main", "HEAD")
	if err != nil {
		t.Fatalf("NumstatRango falló: %v", err)
	}
	// 15 añadidas en a.txt + 10 borradas en a.txt + 3 añadidas en b.txt.
	if vol != 28 {
		t.Errorf("NumstatRango = %d, esperado 28 (15+3 añadidas, 10 borradas)", vol)
	}
}

// TestNumstatRangoVacio: sin cambios el volumen es cero.
func TestNumstatRangoVacio(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "a.txt", "x\n")

	vol, err := NumstatRango("HEAD", "HEAD")
	if err != nil {
		t.Fatalf("NumstatRango falló: %v", err)
	}
	if vol != 0 {
		t.Errorf("NumstatRango = %d, esperado 0", vol)
	}
}

// TestMergeBaseSinBase: dos ramas sin ancestro común son un error explícito,
// porque no existe base sobre la que medir el rango.
func TestMergeBaseSinBase(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "a.txt", "main\n")
	// Rama huérfana: un commit raíz desconectado de main.
	ejecutar(t, "checkout", "--orphan", "otra")
	commitEnRepo(t, "b.txt", "otra\n")
	ejecutar(t, "checkout", "main")

	if _, err := MergeBase("main", "otra"); err == nil {
		t.Error("MergeBase aceptó dos ramas sin ancestro común")
	}
}

// TestNumstatRangoInvalido: un rango con una revisión inexistente falla.
func TestNumstatRangoInvalido(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "a.txt", "x\n")

	if _, err := NumstatRango("no-existe", "HEAD"); err == nil {
		t.Error("NumstatRango aceptó una revisión inexistente")
	}
}

// TestNumstatRangoBinario: un archivo binario llega como "-" en el numstat y
// no debe sumar líneas ni romper el conteo del resto.
func TestNumstatRangoBinario(t *testing.T) {
	prepararRepoTemp(t)
	commitEnRepo(t, "a.txt", "x\n")
	ejecutar(t, "checkout", "-b", "feature")

	if err := os.WriteFile("bin.dat", []byte{0x00, 0x01, 0x02, 0x00, 0xFF}, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("a.txt", []byte("x\ny\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutar(t, "add", "-A")
	ejecutar(t, "commit", "-m", "feat: con binario")

	vol, err := NumstatRango("main", "HEAD")
	if err != nil {
		t.Fatalf("NumstatRango falló: %v", err)
	}
	// Solo cuenta la línea añadida en a.txt; el binario se ignora.
	if vol != 1 {
		t.Errorf("NumstatRango = %d, esperado 1 (el binario no suma líneas)", vol)
	}
}
