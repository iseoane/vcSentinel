package graph

import (
	"reflect"
	"testing"
)

func TestResultadoNativoVinculaEntradaEvidenciaYAlcance(t *testing.T) {
	rutas := []string{"internal/git/diff.go"}
	resultado := nuevoResultadoAnalisis(rutas, alcanceAfectado{
		paquetes:    []string{"internal/git"},
		tests:       []string{"./internal/git"},
		explicacion: []string{"diff.go pertenece a internal/git"},
	}, evidenciaCompletitud{completo: true})
	rutas[0] = "alterada.go"

	alcance, autorizado := AutorizarAlcanceParcial(resultado)
	if !autorizado {
		t.Fatal("un análisis nativo coherente no fue autorizado")
	}
	if !reflect.DeepEqual(resultado.RutasAnalizadas(), []string{"internal/git/diff.go"}) {
		t.Fatalf("identidad analizada = %v", resultado.RutasAnalizadas())
	}
	if !reflect.DeepEqual(resultado.Alcance().Explicacion(), []string{"diff.go pertenece a internal/git"}) {
		t.Fatalf("explicación inspeccionable = %v", resultado.Alcance().Explicacion())
	}
	paquetes := alcance.Paquetes()
	paquetes[0] = "alterado"
	if !reflect.DeepEqual(alcance.Paquetes(), []string{"internal/git"}) ||
		!reflect.DeepEqual(alcance.Tests(), []string{"./internal/git"}) ||
		!reflect.DeepEqual(alcance.Explicacion(), []string{"diff.go pertenece a internal/git"}) {
		t.Fatalf("alcance autorizado incompleto: %+v", alcance)
	}
}

func TestResultadoNativoFallaCerrado(t *testing.T) {
	alcance := alcanceAfectado{
		paquetes:    []string{"internal/git"},
		tests:       []string{"./internal/git"},
		explicacion: []string{"ruta afectada"},
	}
	casos := []struct {
		nombre     string
		rutas      []string
		alcance    alcanceAfectado
		evidencia  evidenciaCompletitud
		noCubierto string
	}{
		{nombre: "incompleto", rutas: []string{"a.go"}, alcance: alcance, evidencia: evidenciaCompletitud{motivo: "lenguaje no soportado"}},
		{nombre: "contradice no cubiertos", rutas: []string{"a.go"}, alcance: alcance, evidencia: evidenciaCompletitud{completo: true, noCubiertos: []string{"a.go"}}, noCubierto: "a.go"},
		{nombre: "contradice motivo", rutas: []string{"a.go"}, alcance: alcance, evidencia: evidenciaCompletitud{completo: true, motivo: "parcial"}},
		{nombre: "sin entrada", alcance: alcance, evidencia: evidenciaCompletitud{completo: true}},
		{nombre: "entrada invalida", rutas: []string{""}, alcance: alcance, evidencia: evidenciaCompletitud{completo: true}},
		{nombre: "sin alcance", rutas: []string{"a.go"}, evidencia: evidenciaCompletitud{completo: true}},
		{nombre: "alcance invalido", rutas: []string{"a.go"}, alcance: alcanceAfectado{paquetes: []string{""}, explicacion: []string{"ruta afectada"}}, evidencia: evidenciaCompletitud{completo: true}},
		{nombre: "sin explicacion", rutas: []string{"a.go"}, alcance: alcanceAfectado{paquetes: []string{"p"}}, evidencia: evidenciaCompletitud{completo: true}},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			resultado := nuevoResultadoAnalisis(caso.rutas, caso.alcance, caso.evidencia)
			if _, autorizado := AutorizarAlcanceParcial(resultado); autorizado {
				t.Fatal("un estado incompleto o contradictorio fue autorizado")
			}
			if caso.noCubierto != "" && !reflect.DeepEqual(resultado.NoCubiertos(), []string{caso.noCubierto}) {
				t.Fatalf("no cubiertos = %v", resultado.NoCubiertos())
			}
		})
	}
}
