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
