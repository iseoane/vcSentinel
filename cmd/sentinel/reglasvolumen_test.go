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

func TestInyectarMigraBloqueMarcadoAntiguo(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "AGENTS.md")
	antiguo := "\n" + marcadorInicio + "\n## Old guardian rule\n- Block all worktree changes.\n" + marcadorFin + "\n"
	escribirArchivo(t, ruta, aCRLF("# Guide\n"+antiguo))

	escribio, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("inyectarReglasDeArchivo devolvió error: %v", err)
	}
	if !escribio {
		t.Fatal("init did not migrate the old marked rule")
	}

	contenido := leerArchivo(t, ruta)
	if strings.Contains(contenido, "Block all worktree changes") {
		t.Errorf("the old marked rule remained after migration: %q", contenido)
	}
	if !strings.Contains(contenido, "sentinel check --staged") {
		t.Errorf("the current staged enforcement rule was not injected: %q", contenido)
	}
	if strings.Contains(strings.ReplaceAll(contenido, "\r\n", ""), "\n") {
		t.Errorf("migration changed a CRLF file to LF: %q", contenido)
	}

	escribio, err = inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("second inyectarReglasDeArchivo returned an error: %v", err)
	}
	if escribio {
		t.Error("init was not idempotent after migrating the marked rule")
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
	if strings.Contains(restante, "CRITICAL VOLUME RULE") {
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
	if strings.Contains(restante, "CRITICAL VOLUME RULE") {
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

// TestInyectarMigraBloqueLegadoDuplicado reproduce el caso EXACTO que tiene
// hoy .claudecode.md de este repo: dos copias del bloque legado (sin
// marcadores), fruto de un binario anterior a la fix de idempotencia. init
// debe migrar: retirar ambas copias legadas y dejar un único bloque marcado.
func TestInyectarMigraBloqueLegadoDuplicado(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), ".claudecode.md")
	escribirArchivo(t, ruta, "# Guía\n"+reglasVolumenLegado+reglasVolumenLegado)

	escribio, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("inyectarReglasDeArchivo devolvió error: %v", err)
	}
	if !escribio {
		t.Fatal("init no migró el bloque legado duplicado")
	}

	contenido := leerArchivo(t, ruta)
	if strings.Count(contenido, marcadorInicio) != 1 {
		t.Errorf("se esperaba exactamente 1 marcadorInicio, contenido: %q", contenido)
	}
	if strings.Count(contenido, marcadorFin) != 1 {
		t.Errorf("se esperaba exactamente 1 marcadorFin, contenido: %q", contenido)
	}
	if strings.Count(contenido, "CRITICAL VOLUME RULE") != 1 {
		t.Errorf("se esperaba exactamente 1 aparición de la regla, contenido: %q", contenido)
	}
	if !strings.Contains(contenido, "# Guía") {
		t.Errorf("se perdió el contenido propio del archivo: %q", contenido)
	}

	escribioOtraVez, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("segunda inyección devolvió error: %v", err)
	}
	if escribioOtraVez {
		t.Error("init no es idempotente tras la migración: volvió a escribir")
	}
}

// TestInyectarMigraBloqueLegadoSinContenidoPrevio cubre el caso real de este
// repo: un archivo cuyo ÚNICO contenido son dos copias del bloque legado
// pegadas desde el byte 0 (sin línea previa, así que la primera copia no
// tiene ningún salto de línea por delante). Antes de este test, la regex
// exigía siempre un "\r?\n" delante del bloque, así que dejaba esa primera
// copia sin reconocer: init migraba solo la segunda y el archivo terminaba
// con un bloque legado huérfano más el bloque marcado nuevo.
func TestInyectarMigraBloqueLegadoSinContenidoPrevio(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), ".claudecode.md")
	sinLeadingNewline := strings.TrimPrefix(reglasVolumenLegado, "\n")
	escribirArchivo(t, ruta, sinLeadingNewline+sinLeadingNewline)

	escribio, err := inyectarReglasDeArchivo(ruta)
	if err != nil {
		t.Fatalf("inyectarReglasDeArchivo devolvió error: %v", err)
	}
	if !escribio {
		t.Fatal("init no migró el bloque legado sin contenido previo")
	}

	contenido := leerArchivo(t, ruta)
	if strings.Count(contenido, marcadorInicio) != 1 {
		t.Errorf("se esperaba exactamente 1 marcadorInicio, contenido: %q", contenido)
	}
	if strings.Count(contenido, "CRITICAL VOLUME RULE") != 1 {
		t.Errorf("se esperaba exactamente 1 aparición de la regla, contenido: %q", contenido)
	}
}

// TestContieneReconoceBloqueMarcadoConOtraRedaccion simula una versión futura
// que reformuló el texto interior del bloque. La detección debe seguir
// funcionando porque depende solo de los marcadores, no del texto.
func TestContieneReconoceBloqueMarcadoConOtraRedaccion(t *testing.T) {
	bloqueFuturo := "\n" + marcadorInicio + "\nOtra redacción completamente distinta.\nMás líneas.\n" + marcadorFin + "\n"

	if !contieneReglasVolumen("# Guía\n" + bloqueFuturo) {
		t.Error("no se reconoció un bloque marcado con redacción distinta a la actual")
	}
}

// TestQuitarRetiraBloqueMarcadoConOtraRedaccion comprueba que un bloque
// marcado con redacción futura distinta puede retirarse igual, sin quedar
// huérfano.
func TestQuitarRetiraBloqueMarcadoConOtraRedaccion(t *testing.T) {
	bloqueFuturo := "\n" + marcadorInicio + "\nOtra redacción completamente distinta.\nMás líneas.\n" + marcadorFin + "\n"

	restante := quitarReglasVolumen("# Guía\n" + bloqueFuturo)
	if strings.Contains(restante, marcadorInicio) {
		t.Errorf("quedó marcadorInicio sin retirar: %q", restante)
	}
	if strings.Contains(restante, marcadorFin) {
		t.Errorf("quedó marcadorFin sin retirar: %q", restante)
	}
	if !strings.Contains(restante, "# Guía") {
		t.Errorf("se perdió el contenido propio del archivo: %q", restante)
	}
}

// TestQuitarRetiraMezclaDeLegadoYMarcado cubre un repo a medio migrar: una
// copia legada y una copia marcada a la vez. uninit debe retirar ambas.
func TestQuitarRetiraMezclaDeLegadoYMarcado(t *testing.T) {
	contenido := "# Guía\n" + reglasVolumenLegado + reglasVolumen

	restante := quitarReglasVolumen(contenido)
	if strings.Contains(restante, "CRITICAL VOLUME RULE") {
		t.Errorf("quedó texto de la regla sin retirar: %q", restante)
	}
	if strings.Contains(restante, marcadorInicio) || strings.Contains(restante, marcadorFin) {
		t.Errorf("quedaron marcadores sin retirar: %q", restante)
	}
}

func TestInjectedRuleDescribesAdvisoryWorktreeAndStagedEnforcement(t *testing.T) {
	required := []string{
		"sentinel check",
		"whole worktree",
		"is advisory",
		"sentinel check --staged",
		"staged authored code",
		"sentinel slice plan --json",
		"sentinel slice apply --plan plan.json --answers answers.json",
	}
	for _, fragment := range required {
		if !strings.Contains(cuerpoReglasVolumen, fragment) {
			t.Errorf("injected rule does not contain %q: %q", fragment, cuerpoReglasVolumen)
		}
	}
	if strings.Contains(cuerpoReglasVolumen, "STRICTLY PROHIBITED") {
		t.Error("injected rule still blocks implementation because of advisory worktree volume")
	}
}
