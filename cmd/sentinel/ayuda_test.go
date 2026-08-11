package main

import (
	"strings"
	"testing"
)

// TestAyudaNoInvadeZonaArgumentos: las descripciones largas (p. ej. pr, review,
// status) deben envolverse y las continuaciones arrancar siempre en la columna
// de descripción (14 espacios), nunca pegarse a la izquierda con los
// argumentos ni a la derecha más allá del ancho máximo.
func TestAyudaNoInvadeZonaArgumentos(t *testing.T) {
	ayuda := construirAyuda()
	lineas := strings.Split(ayuda, "\n")

	const anchoMax = 110
	const columnaDescription = 14 // "  " + nombre de 12 caracteres

	for _, linea := range lineas {
		if len(linea) > anchoMax {
			t.Errorf("línea demasiado larga (%d > %d): %q", len(linea), anchoMax, linea)
		}
	}

	for _, linea := range lineas {
		if strings.TrimSpace(linea) == "" {
			continue
		}
		if !strings.HasPrefix(linea, "  ") {
			// Título de sección, Uso o Flags: no se exige alineación.
			continue
		}
		if esCabeceraSubcomando(linea) {
			continue
		}
		// Línea de continuación de un subcomando.
		if len(linea) < columnaDescription || strings.TrimLeft(linea, " ") == linea {
			t.Errorf("continuación se mete en la zona de argumentos: %q", linea)
		}
	}
}

// esCabeceraSubcomando reconoce la primera línea de un ítem de ayuda: un
// nombre de subcomando que empieza en la columna 3 (tras "  "), a diferencia
// de las continuaciones indentadas a la columna de descripción.
func esCabeceraSubcomando(linea string) bool {
	return len(linea) > 2 && linea[2] != ' '
}

// TestAyudaTraeTodosLosSubcomandos: la ayuda no puede omitir ninguno de los
// comandos que el dispatch maneja.
func TestAyudaTraeTodosLosSubcomandos(t *testing.T) {
	ayuda := construirAyuda()
	for _, nombre := range []string{
		"version", "help", "init", "uninit", "check", "slice", "review",
		"lint", "rebase", "status", "explain", "pr", "install", "upgrade", "uninstall",
	} {
		if !strings.Contains(ayuda, "  "+nombre+" ") {
			t.Errorf("la ayuda no documenta el subcomando %q", nombre)
		}
	}
}

// TestAyudaPrDocumentaFlags: la documentación de pr debe mencionar los flags
// de pr review sin enterrarlos en mitad de la descripción.
func TestAyudaPrDocumentaFlags(t *testing.T) {
	ayuda := construirAyuda()
	if !strings.Contains(ayuda, "Flags de pr review:") {
		t.Error("la ayuda debe documentar los flags de pr review")
	}
}

// TestEnvolverRotaTextoLargo: el helper de wrapping corta a ancho fijo sin
// perder contenido ni cortar palabras en pedazos.
func TestEnvolverRotaTextoLargo(t *testing.T) {
	texto := "Inyecta las reglas de volumen en tus agentes, crea la config per-proyecto e instala el hook pre-commit del repositorio. Se ejecuta siempre en la raíz del repositorio."
	lineas := envolver(texto, 40)
	unido := strings.Join(lineas, " ")
	if !strings.Contains(unido, "per-proyecto") || !strings.Contains(unido, "repositorio.") {
		t.Fatalf("envolver perdió contenido: %q", unido)
	}
	for _, l := range lineas {
		if len(l) > 40 {
			t.Errorf("línea excede ancho 40: %q (%d)", l, len(l))
		}
		if strings.TrimSpace(l) == "" {
			t.Errorf("línea vacía generada")
		}
	}
}
