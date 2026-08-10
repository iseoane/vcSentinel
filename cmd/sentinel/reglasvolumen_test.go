package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aCRLF convierte todos los finales de línea a CRLF, para reproducir el
// archivo real que provocó B10 en Windows con `* text=auto`.
func aCRLF(texto string) string {
	return strings.ReplaceAll(texto, "\n", "\r\n")
}

func escribirArchivo(t *testing.T, ruta, contenido string) {
	t.Helper()
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", ruta, err)
	}
}

func leerArchivo(t *testing.T, ruta string) string {
	t.Helper()
	datos, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v", ruta, err)
	}
	return string(datos)
}

// TestInyectarNoDuplicaConCRLF: el bloque ya presente en CRLF debe
// reconocerse. Es el caso exacto de B10: init dejaba de ser idempotente.
func TestInyectarNoDuplicaConCRLF(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "CLAUDE.md")
	contenido := aCRLF("# Guía\n" + reglasVolumen)
	escribirArchivo(t, ruta, contenido)

	escribio, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("inyectarReglasDeArchivo devolvió error: %v", err)
	}
	if escribio {
		t.Error("init duplicó el bloque: no reconoció la versión en CRLF")
	}
	if leerArchivo(t, ruta) != contenido {
		t.Error("el archivo se modificó pese a que el bloque ya estaba")
	}
}

// TestQuitarConCRLF: uninit debe poder retirar un bloque en CRLF. Si no
// puede, uninit no revierte lo que init hizo, que es su contrato.
func TestQuitarConCRLF(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "AGENTS.md")
	escribirArchivo(t, ruta, aCRLF("# Guía\n"+reglasVolumen))

	quito, err := quitarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("quitarReglasDeArchivo devolvió error: %v", err)
	}
	if !quito {
		t.Fatal("uninit no reconoció el bloque en CRLF")
	}
	restante := leerArchivo(t, ruta)
	if strings.Contains(restante, "REGLA CRÍTICA DE VOLUMEN") {
		t.Errorf("el bloque sigue presente: %q", restante)
	}
	if !strings.Contains(restante, "# Guía") {
		t.Errorf("se perdió el contenido propio del archivo: %q", restante)
	}
}

// TestQuitarBloquesDuplicadosMixtos: uninit debe reparar los archivos que las
// versiones anteriores de init dejaron con el bloque repetido, aunque cada
// copia tenga finales de línea distintos.
func TestQuitarBloquesDuplicadosMixtos(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "CLAUDE.md")
	escribirArchivo(t, ruta, "# Guía\n"+aCRLF(reglasVolumen)+reglasVolumen)

	quito, err := quitarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("quitarReglasDeArchivo devolvió error: %v", err)
	}
	if !quito {
		t.Fatal("uninit no retiró nada")
	}
	restante := leerArchivo(t, ruta)
	if strings.Contains(restante, "REGLA CRÍTICA DE VOLUMEN") {
		t.Errorf("quedaron bloques sin retirar: %q", restante)
	}
}

// TestInyectarRespetaElFinalDeLineaDominante: escribir el bloque en LF dentro
// de un archivo CRLF crearía justo la mezcla que originó B10.
func TestInyectarRespetaElFinalDeLineaDominante(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "CLAUDE.md")
	escribirArchivo(t, ruta, aCRLF("# Guía\nContenido previo.\n"))

	if _, err := inyectarReglasDeArchivo(ruta); err != nil {
		t.Fatalf("inyectarReglasDeArchivo devolvió error: %v", err)
	}
	contenido := leerArchivo(t, ruta)
	if strings.Contains(strings.ReplaceAll(contenido, "\r\n", ""), "\n") {
		t.Errorf("el bloque se escribió con LF en un archivo CRLF: %q", contenido)
	}

	// Y sigue siendo idempotente tras escribirlo en CRLF.
	escribio, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("segunda inyección devolvió error: %v", err)
	}
	if escribio {
		t.Error("la segunda ejecución de init duplicó el bloque que ella misma escribió")
	}
}

// TestInyectarEnArchivoLFSigueUsandoLF protege el comportamiento actual en
// Debian: un archivo con finales LF no debe recibir CRLF.
func TestInyectarEnArchivoLFSigueUsandoLF(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "AGENTS.md")
	escribirArchivo(t, ruta, "# Guía\nContenido previo.\n")

	if _, err := inyectarReglasDeArchivo(ruta); err != nil {
		t.Fatalf("inyectarReglasDeArchivo devolvió error: %v", err)
	}
	if strings.Contains(leerArchivo(t, ruta), "\r\n") {
		t.Error("se introdujo CRLF en un archivo con finales LF")
	}
}
