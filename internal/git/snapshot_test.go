package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// requiereGitReal salta el test en modo -short o si git no está en el PATH,
// igual que el resto de tests de integración del paquete.
func requiereGitReal(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}
}

func TestArbolDeCoincideConRevParse(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	esperado := ejecutarGit(t, dir, "rev-parse", "HEAD^{tree}")
	obtenido, err := ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("ArbolDe devolvió error: %v", err)
	}
	if obtenido != esperado {
		t.Errorf("ArbolDe(HEAD) = %q, esperado %q", obtenido, esperado)
	}
}

// TestArbolDeRechazaRevisionQueEmpiezaConGuion cubre B14: una revisión que
// empieza con "-" se interpretaría como una opción de "git rev-parse" en vez
// de como el nombre de una revisión (option injection), así que debe
// rechazarse antes de interpolarla en el comando.
func TestArbolDeRechazaRevisionQueEmpiezaConGuion(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if _, err := ArbolDe("--upload-pack=touch /tmp/pwned"); err == nil {
		t.Fatal("ArbolDe debería rechazar una revisión que empieza con \"-\"")
	}
}

func TestCrearSnapshotContieneLosArchivosDelArbol(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("ArbolDe devolvió error: %v", err)
	}

	ruta, err := CrearSnapshot(tree)
	if err != nil {
		t.Fatalf("CrearSnapshot devolvió error: %v", err)
	}
	info, err := os.Stat(ruta)
	if err != nil || !info.IsDir() {
		t.Fatalf("CrearSnapshot debería devolver un directorio existente, ruta=%q err=%v", ruta, err)
	}
	contenido, err := os.ReadFile(filepath.Join(ruta, "a.go"))
	if err != nil {
		t.Fatalf("no se pudo leer a.go dentro del snapshot: %v", err)
	}
	if string(contenido) != "package a\n" {
		t.Errorf("contenido de a.go = %q, esperado %q", contenido, "package a\n")
	}
}

func TestCrearSnapshotEsIdempotente(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("ArbolDe devolvió error: %v", err)
	}

	primera, err := CrearSnapshot(tree)
	if err != nil {
		t.Fatalf("primera llamada a CrearSnapshot devolvió error: %v", err)
	}
	segunda, err := CrearSnapshot(tree)
	if err != nil {
		t.Fatalf("segunda llamada a CrearSnapshot devolvió error: %v", err)
	}
	if primera != segunda {
		t.Errorf("CrearSnapshot no es idempotente: %q != %q", primera, segunda)
	}

	listado := ejecutarGit(t, dir, "worktree", "list")
	if n := strings.Count(listado, primera); n != 1 {
		t.Errorf("git worktree list muestra %d entradas para %q, esperada 1:\n%s", n, primera, listado)
	}
}

func TestCrearSnapshotConcurrenteNoDuplicaNiFalla(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("ArbolDe devolvió error: %v", err)
	}

	const llamadas = 4
	rutas := make([]string, llamadas)
	errores := make([]error, llamadas)
	var wg sync.WaitGroup
	for i := 0; i < llamadas; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ruta, err := CrearSnapshot(tree)
			rutas[idx] = ruta
			errores[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errores {
		if err != nil {
			t.Errorf("llamada %d devolvió error: %v", i, err)
		}
	}
	for i := 1; i < llamadas; i++ {
		if rutas[i] != rutas[0] {
			t.Errorf("llamada %d devolvió una ruta distinta: %q != %q", i, rutas[i], rutas[0])
		}
	}

	listado := ejecutarGit(t, dir, "worktree", "list")
	if n := strings.Count(listado, rutas[0]); n != 1 {
		t.Errorf("git worktree list muestra %d entradas para %q, esperada 1:\n%s", n, rutas[0], listado)
	}
}

func TestPurgarSnapshotsConAntiguedadCortaPurgaElRecienCreado(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("ArbolDe devolvió error: %v", err)
	}
	ruta, err := CrearSnapshot(tree)
	if err != nil {
		t.Fatalf("CrearSnapshot devolvió error: %v", err)
	}

	if err := PurgarSnapshots(0); err != nil {
		t.Fatalf("PurgarSnapshots devolvió error: %v", err)
	}
	if _, err := os.Stat(ruta); !os.IsNotExist(err) {
		t.Errorf("el snapshot debería haberse purgado, pero sigue existiendo (err=%v)", err)
	}
}

// TestPurgarSnapshotsPropagaErrorSiNoPuedeEliminar cubre B15: hoy la función
// devuelve nil incondicional aunque "worktree remove" y su reintento con
// --force fallen los dos, así que el llamador cree que el disco quedó
// limpio cuando en realidad el snapshot roto sigue ahí. Se simula con un
// directorio que NO es un worktree registrado (git no puede quitarlo con
// ninguna de las dos vías) y con una fecha de modificación antigua para que
// entre en el rango a purgar.
func TestPurgarSnapshotsPropagaErrorSiNoPuedeEliminar(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	snapshots, err := directorioSnapshots()
	if err != nil {
		t.Fatalf("directorioSnapshots devolvió error: %v", err)
	}
	rutaRota := filepath.Join(snapshots, "no-es-un-worktree")
	if err := os.MkdirAll(rutaRota, 0755); err != nil {
		t.Fatalf("no se pudo crear el directorio roto: %v", err)
	}
	antigua := time.Now().Add(-time.Hour)
	if err := os.Chtimes(rutaRota, antigua, antigua); err != nil {
		t.Fatalf("no se pudo envejecer el directorio roto: %v", err)
	}

	if err := PurgarSnapshots(time.Minute); err == nil {
		t.Fatal("PurgarSnapshots debería devolver error: no pudo eliminar un snapshot roto")
	}
	if _, err := os.Stat(rutaRota); err != nil {
		t.Errorf("el directorio roto debería seguir existiendo tras el fallo, pero: %v", err)
	}
}

func TestPurgarSnapshotsConAntiguedadLargaNoPurga(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	tree, err := ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("ArbolDe devolvió error: %v", err)
	}
	ruta, err := CrearSnapshot(tree)
	if err != nil {
		t.Fatalf("CrearSnapshot devolvió error: %v", err)
	}

	if err := PurgarSnapshots(time.Hour); err != nil {
		t.Fatalf("PurgarSnapshots devolvió error: %v", err)
	}
	if _, err := os.Stat(ruta); err != nil {
		t.Errorf("el snapshot no debería haberse purgado (err=%v)", err)
	}
}

func TestTreeOIDIsValid(t *testing.T) {
	if !treeOIDIsValid(strings.Repeat("a", 40)) || !treeOIDIsValid(strings.Repeat("B", 64)) {
		t.Fatal("expected SHA-1 and SHA-256 object ids to be valid")
	}
	for _, value := range []string{"", "../" + strings.Repeat("a", 40), strings.Repeat("g", 40), strings.Repeat("a", 39)} {
		if treeOIDIsValid(value) {
			t.Fatalf("treeOIDIsValid(%q) = true", value)
		}
	}
}

func TestRemoveStalePurgeMarker(t *testing.T) {
	marker := filepath.Join(t.TempDir(), snapshotPurgeMarker)
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-snapshotPurgeMarkerMaxAge - time.Second)
	if err := os.Chtimes(marker, old, old); err != nil {
		t.Fatal(err)
	}
	if !removeStalePurgeMarker(marker) {
		t.Fatal("expected stale purge marker to be removed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker still exists or could not be checked: %v", err)
	}
}
