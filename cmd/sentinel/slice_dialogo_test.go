package main

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// conEntrada sustituye lectorStdin por un lector que devuelve s, y restaura el
// lector original al terminar el test (t.Cleanup). Permite a los tests del
// paquete main dirigir las respuestas de los flujos interactivos sin tocar la
// entrada estándar real del proceso de test.
func conEntrada(t *testing.T, s string) {
	t.Helper()
	original := lectorStdin
	t.Cleanup(func() { lectorStdin = original })
	lectorStdin = bufio.NewReader(strings.NewReader(s))
}

// --- aprobarYEjecutar ---

// TestAprobarYEjecutar_Cancelar: la opción C cancela y devuelve false sin
// intentar ejecutar el plan (ni tocar git).
func TestAprobarYEjecutar_Cancelar(t *testing.T) {
	conEntrada(t, "c\n")
	plan := &git.PlanFragmentacion{}

	if aprobarYEjecutar(plan, ".") {
		t.Fatal("esperaba false al cancelar, obtuve true")
	}
}

// TestAprobarYEjecutar_OpcionInvalidaVuelveAPreguntar: una opción no
// reconocida no aborta el diálogo; debe volver a mostrar el menú y aceptar la
// siguiente respuesta válida.
func TestAprobarYEjecutar_OpcionInvalidaVuelveAPreguntar(t *testing.T) {
	conEntrada(t, "xyz\nc\n")
	plan := &git.PlanFragmentacion{}

	if aprobarYEjecutar(plan, ".") {
		t.Fatal("esperaba false tras opción inválida seguida de cancelar, obtuve true")
	}
}

// TestAprobarYEjecutar_EnterVacioApruebaConPlanVacio: Enter vacío equivale a
// aprobar (case "", "a", "aprobar"). Se usa un plan sin lotes para que
// ejecutarPlanAprobado no cree ningún commit real (EjecutarPlanFragmentacion
// con Lotes vacío no ejecuta ningún comando git de escritura).
func TestAprobarYEjecutar_EnterVacioApruebaConPlanVacio(t *testing.T) {
	conEntrada(t, "\n")
	plan := &git.PlanFragmentacion{}

	if !aprobarYEjecutar(plan, ".") {
		t.Fatal("esperaba true al aprobar con Enter vacío, obtuve false")
	}
}

// TestAprobarYEjecutar_EOFCancela fija el comportamiento correcto tras B9
// (CRITICAL): un EOF de stdin (entrada vacía, sin ni siquiera un '\n') debe
// CANCELAR el plan, no aprobarlo. leerLinea() devuelve error en EOF; a
// diferencia de un Enter vacío real (línea = "", que sí debe aprobar, es el
// default anunciado por el propio menú "Opción [A]:"), un stdin cerrado no es
// una respuesta del usuario y no puede interpretarse como "sí, ejecuta". Un
// stdin cerrado (CI, nohup, una tubería que termina, un agente sin consola) no
// debe saltarse el único control humano del desbloqueo del guardián.
func TestAprobarYEjecutar_EOFCancela(t *testing.T) {
	conEntrada(t, "") // reader vacío: ReadString('\n') devuelve io.EOF de inmediato
	plan := &git.PlanFragmentacion{}

	resultado := aprobarYEjecutar(plan, ".")
	if resultado {
		t.Fatal("B9: un EOF de stdin no debe aprobar el plan, debe cancelarlo (false)")
	}
}

// TestAprobarYEjecutar_NoCuelgaEnBucleInfinitoTrasEOF asegura que un EOF no
// deja a aprobarYEjecutar reintentando leerLinea indefinidamente: la función
// debe retornar en la primera iteración. Si alguna vez leerLinea() devolviera
// EOF pero el bucle no reconociera el error, esta prueba colgaría (el test
// runner la mataría por timeout), delatando el defecto.
func TestAprobarYEjecutar_NoCuelgaEnBucleInfinitoTrasEOF(t *testing.T) {
	conEntrada(t, "")
	plan := &git.PlanFragmentacion{}

	done := make(chan bool, 1)
	go func() {
		done <- aprobarYEjecutar(plan, ".")
	}()

	select {
	case <-done:
		// No se cuelga: correcto, con independencia de si el resultado es
		// aprobar o cancelar (eso ya lo cubre el test anterior).
	case <-time.After(5 * time.Second):
		t.Fatal("aprobarYEjecutar no retornó tras EOF: posible bucle infinito")
	}
}

// --- construirDecisionGigante ---

func archivoGiganteDePrueba() git.ArchivoModificado {
	return git.ArchivoModificado{Ruta: "backend/gigante.go", Lineas: 900, Capa: "backend"}
}

// TestConstruirDecisionGigante_AbortaConOpcion3: la opción 3 aborta la
// operación devolviendo (false, nil): ConstruirPlanFragmentacion lo convierte
// en el error "fragmentación abortada".
func TestConstruirDecisionGigante_AbortaConOpcion3(t *testing.T) {
	conEntrada(t, "3\n")
	decidir := construirDecisionGigante(".")

	bypass, err := decidir(archivoGiganteDePrueba())
	if err != nil {
		t.Fatalf("abortar no debería devolver error propio, devolvió: %v", err)
	}
	if bypass {
		t.Fatal("esperaba bypass=false al abortar con la opción 3")
	}
}

// TestConstruirDecisionGigante_Bypass: la opción 2 hace bypass y devuelve
// (true, nil) sin pedir nada más.
func TestConstruirDecisionGigante_Bypass(t *testing.T) {
	conEntrada(t, "2\n")
	decidir := construirDecisionGigante(".")

	bypass, err := decidir(archivoGiganteDePrueba())
	if err != nil {
		t.Fatalf("bypass no debería devolver error, devolvió: %v", err)
	}
	if !bypass {
		t.Fatal("esperaba bypass=true con la opción 2")
	}
}

// TestConstruirDecisionGigante_OpcionInvalidaVuelveAPreguntar: una opción
// fuera de 1-3 no aborta el menú; debe volver a preguntar y aceptar la
// siguiente opción válida (aquí, 3 para abortar).
func TestConstruirDecisionGigante_OpcionInvalidaVuelveAPreguntar(t *testing.T) {
	conEntrada(t, "9\n3\n")
	decidir := construirDecisionGigante(".")

	bypass, err := decidir(archivoGiganteDePrueba())
	if err != nil {
		t.Fatalf("no debería devolver error: %v", err)
	}
	if bypass {
		t.Fatal("esperaba bypass=false tras opción inválida seguida de abortar")
	}
}

// --- decidirAccion (nivel 2: función pura extraída de aprobarYEjecutar) ---

// TestDecidirAccion cubre en tabla, sin stdin, el mapeo de cada línea leída a
// la acción que aprobarYEjecutar debe tomar. Separa la decisión pura de la
// lectura de stdin y de la ejecución, para poder probarla sin simular
// entrada ni tocar el plan ni git.
func TestDecidirAccion(t *testing.T) {
	casos := []struct {
		nombre string
		linea  string
		quiere accion
	}{
		{"vacio_aprueba", "", accionAprobar},
		{"a_aprueba", "a", accionAprobar},
		{"A_mayuscula_aprueba", "A", accionAprobar},
		{"aprobar_palabra_completa", "aprobar", accionAprobar},
		{"espacios_alrededor_de_a_aprueban", "  a  ", accionAprobar},
		{"r_regenera", "r", accionRegenerar},
		{"regenerar_palabra_completa", "regenerar", accionRegenerar},
		{"e_edita", "e", accionEditar},
		{"editar_palabra_completa", "editar", accionEditar},
		{"c_cancela", "c", accionCancelar},
		{"cancelar_palabra_completa", "cancelar", accionCancelar},
		{"opcion_desconocida_es_invalida", "xyz", accionInvalida},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := decidirAccion(c.linea); got != c.quiere {
				t.Errorf("decidirAccion(%q) = %v, quiere %v", c.linea, got, c.quiere)
			}
		})
	}
}

// TestConstruirDecisionGigante_EOFDevuelveError: a diferencia de
// aprobarYEjecutar, construirDecisionGigante SÍ propaga el error de
// leerLinea en vez de traducirlo a una opción por defecto: un EOF aquí no
// aprueba ni hace bypass, devuelve error explícito.
func TestConstruirDecisionGigante_EOFDevuelveError(t *testing.T) {
	conEntrada(t, "")
	decidir := construirDecisionGigante(".")

	_, err := decidir(archivoGiganteDePrueba())
	if err == nil {
		t.Fatal("esperaba error al leer la opción tras EOF, obtuve nil")
	}
}
