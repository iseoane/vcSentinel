package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInyectarReglasIdempotente: ejecutar init dos veces NO debe duplicar el
// bloque de reglas en los archivos objetivo (regresión del bug donde init
// appendeaba incondicionalmente y duplicaba la inserción).
func TestInyectarReglasIdempotente(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "AGENTS.md")
	contenidoBase := "# Proyecto\n\n## Convenciones\n"

	if err := os.WriteFile(ruta, []byte(contenidoBase), 0644); err != nil {
		t.Fatal(err)
	}

	// Primera inyección: debe escribir el bloque.
	escrito, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("primera inyección devolvió error: %v", err)
	}
	if !escrito {
		t.Fatal("la primera inyección debería escribir el bloque")
	}

	datos, _ := os.ReadFile(ruta)
	if n := strings.Count(string(datos), "## REGLA"); n != 1 {
		t.Fatalf("tras la primera inyección hay %d bloques, esperado 1", n)
	}

	// Segunda inyección (init repetido): no debe duplicar.
	escrito, err = inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("segunda inyección devolvió error: %v", err)
	}
	if escrito {
		t.Error("la segunda inyección no debería modificar el archivo (bloque ya presente)")
	}

	datos, _ = os.ReadFile(ruta)
	if n := strings.Count(string(datos), "## REGLA"); n != 1 {
		t.Fatalf("tras la segunda inyección hay %d bloques, esperado 1 (idempotencia rota)", n)
	}
	// El contenido original se conserva íntegro.
	if !strings.Contains(string(datos), contenidoBase) {
		t.Error("la inyección perdió el contenido original del archivo")
	}
}

// TestInyectarReglasCreaArchivo: si el archivo no existe, init lo crea con el
// bloque (mismo comportamiento que antes).
func TestInyectarReglasCreaArchivo(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "CLAUDE.md")

	escrito, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("inyección devolvió error: %v", err)
	}
	if !escrito {
		t.Error("con archivo inexistente debería escribirse el bloque")
	}
	datos, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se creó el archivo: %v", err)
	}
	if !strings.Contains(string(datos), reglasVolumen) {
		t.Error("el archivo creado no contiene el bloque de reglas")
	}
}

// TestQuitarReglasReparaDuplicados: uninit debe retirar TODAS las apariciones
// del bloque, incluidos los duplicados que init dejó en versiones anteriores.
func TestQuitarReglasReparaDuplicados(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, ".claudecode.md")
	duplicado := "# previo\n" + reglasVolumen + reglasVolumen + "fin\n"
	if err := os.WriteFile(ruta, []byte(duplicado), 0644); err != nil {
		t.Fatal(err)
	}

	retiradas, err := quitarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("quitarReglasDeArchivo devolvió error: %v", err)
	}
	if !retiradas {
		t.Fatal("debería haberse retirado algo (había duplicados)")
	}
	datos, _ := os.ReadFile(ruta)
	if strings.Contains(string(datos), "## REGLA") {
		t.Errorf("quedan bloques de reglas tras uninit: %q", datos)
	}
	if !strings.Contains(string(datos), "# previo") || !strings.Contains(string(datos), "fin\n") {
		t.Errorf("uninit borró contenido ajeno: %q", datos)
	}
}