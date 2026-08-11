// Package graph define los contratos del grafo verificable y del contexto
// opcional. Solo el proveedor nativo del paquete puede autorizar alcance parcial.
package graph

// GraphProvider representa el grafo nativo que produce un análisis atómico.
type GraphProvider interface {
	Nombre() string
	Analizar(rutas []string) (ResultadoAnalisis, error)
}

type evidenciaCompletitud struct {
	completo    bool
	motivo      string
	noCubiertos []string
}

type alcanceAfectado struct {
	paquetes    []string
	tests       []string
	explicacion []string
}

// ResultadoAnalisis vincula la entrada analizada, el alcance y su evidencia.
// Su valor cero no es confiable; solo graph puede producir un valor autorizable.
type ResultadoAnalisis struct {
	identidad string
	rutas     []string
	alcance   alcanceAfectado
	evidencia evidenciaCompletitud
	confiable bool
}

func nuevoResultadoAnalisis(identidad string, rutas []string, alcance alcanceAfectado, evidencia evidenciaCompletitud) ResultadoAnalisis {
	return ResultadoAnalisis{
		identidad: identidad,
		rutas:     clonar(rutas),
		alcance: alcanceAfectado{
			paquetes:    clonar(alcance.paquetes),
			tests:       clonar(alcance.tests),
			explicacion: clonar(alcance.explicacion),
		},
		evidencia: evidenciaCompletitud{
			completo:    evidencia.completo,
			motivo:      evidencia.motivo,
			noCubiertos: clonar(evidencia.noCubiertos),
		},
		confiable: true,
	}
}

func (r ResultadoAnalisis) IdentidadSnapshot() string { return r.identidad }
func (r ResultadoAnalisis) RutasAnalizadas() []string { return clonar(r.rutas) }
func (r ResultadoAnalisis) Completo() bool            { return r.evidencia.completo }
func (r ResultadoAnalisis) MotivoIncompleto() string  { return r.evidencia.motivo }
func (r ResultadoAnalisis) NoCubiertos() []string     { return clonar(r.evidencia.noCubiertos) }
func (r ResultadoAnalisis) Alcance() AlcanceAfectado  { return copiarAlcance(r.alcance) }

// AlcanceAfectado es una vista de solo lectura del alcance autorizado.
type AlcanceAfectado struct {
	paquetes    []string
	tests       []string
	explicacion []string
}

func (a AlcanceAfectado) Paquetes() []string    { return clonar(a.paquetes) }
func (a AlcanceAfectado) Tests() []string       { return clonar(a.tests) }
func (a AlcanceAfectado) Explicacion() []string { return clonar(a.explicacion) }

// AutorizacionAlcance es la única entrada válida para ejecutar parcialmente.
// Su valor cero no autoriza; solo AutorizarAlcanceParcial puede crearla válida.
type AutorizacionAlcance struct {
	alcance    alcanceAfectado
	autorizada bool
}

func (a AutorizacionAlcance) Autorizada() bool      { return a.autorizada }
func (a AutorizacionAlcance) Paquetes() []string    { return clonar(a.alcance.paquetes) }
func (a AutorizacionAlcance) Tests() []string       { return clonar(a.alcance.tests) }
func (a AutorizacionAlcance) Explicacion() []string { return clonar(a.alcance.explicacion) }

// AutorizarAlcanceParcial falla cerrado ante resultados ajenos, incompletos,
// contradictorios o sin una entrada y un alcance explicable válidos.
func AutorizarAlcanceParcial(resultado ResultadoAnalisis) (AutorizacionAlcance, bool) {
	a := resultado.alcance
	e := resultado.evidencia
	if !resultado.confiable || resultado.identidad == "" || !e.completo || e.motivo != "" || len(e.noCubiertos) != 0 ||
		!validos(resultado.rutas) || len(a.paquetes) == 0 ||
		!validosOpcionales(a.paquetes) || !validosOpcionales(a.tests) || !validos(a.explicacion) {
		return AutorizacionAlcance{}, false
	}
	return AutorizacionAlcance{alcance: alcanceAfectado{
		paquetes: clonar(a.paquetes), tests: clonar(a.tests), explicacion: clonar(a.explicacion),
	}, autorizada: true}, true
}

func copiarAlcance(a alcanceAfectado) AlcanceAfectado {
	return AlcanceAfectado{
		paquetes:    clonar(a.paquetes),
		tests:       clonar(a.tests),
		explicacion: clonar(a.explicacion),
	}
}

func validos(valores []string) bool {
	if len(valores) == 0 {
		return false
	}
	return validosOpcionales(valores)
}

func validosOpcionales(valores []string) bool {
	for _, valor := range valores {
		if valor == "" {
			return false
		}
	}
	return true
}

func clonar(valores []string) []string {
	return append([]string(nil), valores...)
}

// ContextProvider puede enriquecer una revisión, pero no producir análisis
// nativos ni autorizar alcance.
type ContextProvider interface {
	Nombre() string
	Contexto(rutas []string) ([]ReferenciaContexto, error)
}

// ReferenciaContexto explica por qué una ruta puede ser relevante al revisor.
type ReferenciaContexto struct {
	Ruta        string
	Explicacion string
}
