package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prepararRepositorioConCommits crea un repo de prueba con estado inicial
// (base.txt), un commit que añade a.go y otro que añade b.go.
func prepararRepositorioConCommits(t *testing.T) string {
	t.Helper()
	dir := prepararRepositorioPrueba(t, map[string]string{"base.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatalf("no se pudo crear a.go: %v", err)
	}
	ejecutarGit(t, dir, "add", "a.go")
	ejecutarGit(t, dir, "commit", "-m", "feat(a): primer commit")
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644); err != nil {
		t.Fatalf("no se pudo crear b.go: %v", err)
	}
	ejecutarGit(t, dir, "add", "b.go")
	ejecutarGit(t, dir, "commit", "-m", "feat(b): segundo commit")
	return dir
}

func TestMensajeCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	head, err := SHAHead()
	if err != nil {
		t.Fatalf("SHAHead devolvió error: %v", err)
	}
	mensaje, err := MensajeCommit(head)
	if err != nil {
		t.Fatalf("MensajeCommit devolvió error: %v", err)
	}
	if mensaje != "feat(b): segundo commit" {
		t.Errorf("mensaje = %q, esperado 'feat(b): segundo commit'", mensaje)
	}
}

func TestDiffCommitContieneArchivo(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	diff, err := DiffCommit(head)
	if err != nil {
		t.Fatalf("DiffCommit devolvió error: %v", err)
	}
	if !strings.Contains(diff, "b.go") {
		t.Errorf("el diff debería mencionar b.go, obtenido: %s", diff)
	}
}

func TestSHAsRangoCronologico(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	todos, err := SHAsRango("", "HEAD")
	if err != nil {
		t.Fatalf("SHAsRango devolvió error: %v", err)
	}
	if len(todos) != 3 {
		t.Fatalf("SHAsRango(\"\", HEAD) = %d commits, esperado 3 (inicial + 2)", len(todos))
	}

	// Rango desde el estado inicial: solo los dos commits de trabajo.
	shas, err := SHAsRango(todos[0], "HEAD")
	if err != nil {
		t.Fatalf("SHAsRango devolvió error: %v", err)
	}
	if len(shas) != 2 {
		t.Fatalf("SHAsRango = %d commits, esperado 2", len(shas))
	}
	primero, _ := MensajeCommit(shas[0])
	segundo, _ := MensajeCommit(shas[1])
	if primero != "feat(a): primer commit" || segundo != "feat(b): segundo commit" {
		t.Errorf("orden cronológico roto: %q, %q", primero, segundo)
	}
}

func TestArchivosDeCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	shas, _ := SHAsRango("", "HEAD")
	// shas[1] es "feat(a): primer commit" (shas[0] es el estado inicial).
	archivos, err := ArchivosDeCommit(shas[1])
	if err != nil {
		t.Fatalf("ArchivosDeCommit devolvió error: %v", err)
	}
	if len(archivos) != 1 || archivos[0] != "a.go" {
		t.Errorf("archivos = %+v, esperado [a.go]", archivos)
	}
}
