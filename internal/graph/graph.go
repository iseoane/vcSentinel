// Package graph define los contratos del grafo verificable y del contexto
// opcional. Solo el primero puede autorizar validación parcial.
package graph

// GraphProvider representa el grafo nativo y verificable usado para calcular
// el alcance de validación.
type GraphProvider interface {
	Nombre() string
	Paquetes(rutas []string) ([]Paquete, error)
	Importadores(paquete string) ([]string, error)
	TestsDe(paquete string) ([]string, error)
	Completitud(rutas []string) Completitud
}

// Paquete identifica un paquete conocido por el proveedor nativo.
type Paquete struct {
	Nombre string
}

// Completitud aporta la prueba que permite o impide acotar una validación.
// Su valor cero es incompleto por diseño.
type Completitud struct {
	Completo    bool
	Motivo      string
	NoCubiertos []string
}

// AlcanceAfectado conserva el resultado acotado y por qué cada dependencia fue
// incluida, para poder explicarlo sin recalcular el grafo.
type AlcanceAfectado struct {
	Paquetes    []string
	Tests       []string
	Explicacion []string
}

// AutorizarAlcanceParcial solo revela el alcance cuando el proveedor afirmó
// completitud. Ante incompletitud devuelve el valor cero y obliga al fallback.
func AutorizarAlcanceParcial(completitud Completitud, alcance AlcanceAfectado) (AlcanceAfectado, bool) {
	if !completitud.Completo {
		return AlcanceAfectado{}, false
	}
	return alcance, true
}

// ContextProvider puede enriquecer una revisión, pero su interfaz no expone
// completitud ni autorización de alcance.
type ContextProvider interface {
	Nombre() string
	Contexto(rutas []string) ([]ReferenciaContexto, error)
}

// ReferenciaContexto explica por qué una ruta puede ser relevante al revisor.
type ReferenciaContexto struct {
	Ruta        string
	Explicacion string
}
