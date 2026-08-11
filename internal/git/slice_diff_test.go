package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffPendienteRutasIncluyeCambiosSinMutarIndice(t *testing.T) {
	dir := prepararRepositorioPrueba(t, map[string]string{
		"rastreado.go": "package ejemplo\n",
	})
	t.Chdir(dir)

	contenidoStaged := "package ejemplo\n\nfunc Staged() {}\n"
	if err := os.WriteFile("rastreado.go", []byte(contenidoStaged), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, dir, "add", "--", "rastreado.go")
	if err := os.WriteFile("rastreado.go", []byte(contenidoStaged+"\nfunc Unstaged() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archivosNuevos := map[string]string{
		"archivo con espacios.go": "package nuevo\n\nfunc ConEspacios() {}\n",
		"-opcion.go":              "package nuevo\n\nfunc OpcionLiteral() {}\n",
	}
	for ruta, contenido := range archivosNuevos {
		if err := os.WriteFile(filepath.Join(dir, ruta), []byte(contenido), 0644); err != nil {
			t.Fatal(err)
		}
	}

	indiceAntes := ejecutarGit(t, dir, "write-tree")
	diff, err := diffPendienteRutas([]string{"rastreado.go", "archivo con espacios.go", "-opcion.go"})
	if err != nil {
		t.Fatalf("diffPendienteRutas devolvió error: %v", err)
	}

	for _, fragmento := range []string{"func Staged() {}", "func Unstaged() {}", "func ConEspacios() {}", "func OpcionLiteral() {}"} {
		if !strings.Contains(diff, fragmento) {
			t.Errorf("el diff no contiene %q:\n%s", fragmento, diff)
		}
	}
	if veces := strings.Count(diff, "diff --git a/rastreado.go b/rastreado.go"); veces != 1 {
		t.Errorf("el diff rastreado aparece %d veces, esperado 1:\n%s", veces, diff)
	}
	if indiceDespues := ejecutarGit(t, dir, "write-tree"); indiceDespues != indiceAntes {
		t.Errorf("el índice cambió: antes %s, después %s", indiceAntes, indiceDespues)
	}
}

func TestDiffPendienteRutasRechazaNoRastreadoBinario(t *testing.T) {
	dir := prepararRepositorioPrueba(t, map[string]string{"rastreado.go": "package ejemplo\n"})
	t.Chdir(dir)
	if err := os.WriteFile("binario.dat", []byte{'V', 'A', 'S', 0, 'X'}, 0644); err != nil {
		t.Fatal(err)
	}

	diff, err := diffPendienteRutas([]string{"binario.dat"})
	if err == nil || diff != "" || !strings.Contains(err.Error(), "binario") {
		t.Fatalf("resultado = (%q, %v), esperado rechazo binario sin contenido", diff, err)
	}
}

func TestDiffPendienteRutasRechazaNoRastreadoDemasiadoGrande(t *testing.T) {
	dir := prepararRepositorioPrueba(t, map[string]string{"rastreado.go": "package ejemplo\n"})
	t.Chdir(dir)
	if err := os.WriteFile("grande.txt", []byte(strings.Repeat("x", limiteBytesMicroDiff+1)), 0644); err != nil {
		t.Fatal(err)
	}

	diff, err := diffPendienteRutas([]string{"grande.txt"})
	if err == nil || diff != "" || !strings.Contains(err.Error(), "supera el límite") {
		t.Fatalf("resultado = (%q, %v), esperado rechazo por tamaño sin contenido", diff, err)
	}
}
