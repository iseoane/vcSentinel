package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestObtenerGitDirEnRepositorio(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	gitDir, err := ObtenerGitDir()
	if err != nil {
		t.Fatalf("ObtenerGitDir devolvió error: %v", err)
	}
	esperado := filepath.Join(dir, ".git")
	if !EsMismaRuta(gitDir, esperado) {
		t.Errorf("ObtenerGitDir() = %q, esperado %q", gitDir, esperado)
	}
}

func TestObtenerGitDirFueraDeRepositorio(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	t.Chdir(t.TempDir())

	if _, err := ObtenerGitDir(); err == nil {
		t.Error("se esperaba error al consultar el git-dir fuera de un repositorio Git")
	}
}

func TestObtenerGitDirDesdeSubdirectorio(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	sub := filepath.Join(dir, "internal", "paquete")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("no se pudo crear el subdirectorio: %v", err)
	}
	t.Chdir(sub)

	gitDir, err := ObtenerGitDir()
	if err != nil {
		t.Fatalf("ObtenerGitDir devolvió error: %v", err)
	}
	esperado := filepath.Join(dir, ".git")
	if !EsMismaRuta(gitDir, esperado) {
		t.Errorf("ObtenerGitDir() = %q, esperado %q", gitDir, esperado)
	}
}

// TestObtenerGitDirDeResuelveDesdeLaRutaNoDesdeElCwd fija el contrato que
// faltaba: el git dir se resuelve desde la ruta indicada, no desde el
// directorio de trabajo del proceso.
//
// Sin él, un comando que opera sobre un worktree ajeno escribe en el
// repositorio donde CASUALMENTE se ejecuta. Eso es lo que llenó el registro de
// eventos real con 855 de 1082 entradas fabricadas por la suite de tests: los
// tests pasan un worktree temporal, pero ObtenerGitDir() lee el cwd, que
// durante `go test` es el propio repositorio.
func TestObtenerGitDirDeResuelveDesdeLaRutaNoDesdeElCwd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}
	ajeno := t.TempDir()
	if out, err := exec.Command("git", "-C", ajeno, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	// El cwd sigue siendo este repositorio: exactamente la situación de un test.
	resuelto, err := ObtenerGitDirDe(ajeno)
	if err != nil {
		t.Fatalf("ObtenerGitDirDe(%q): %v", ajeno, err)
	}
	esperado, err := filepath.EvalSymlinks(filepath.Join(ajeno, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	obtenido, err := filepath.EvalSymlinks(resuelto)
	if err != nil {
		t.Fatal(err)
	}
	if obtenido != esperado {
		t.Errorf("ObtenerGitDirDe = %q, esperado %q: resolvió desde el cwd en vez de desde la ruta", obtenido, esperado)
	}

	// Fuera de un repositorio debe fallar, para que el llamador no escriba en
	// ningún sitio en vez de escribir en el repositorio equivocado.
	if _, err := ObtenerGitDirDe(t.TempDir()); err == nil {
		t.Error("ObtenerGitDirDe fuera de un repositorio debería fallar")
	}
}
