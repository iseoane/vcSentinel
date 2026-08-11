package graph

import (
	"reflect"
	"testing"
)

type proveedorDoble struct {
	completitud Completitud
}

func (p proveedorDoble) Nombre() string { return "native-test" }
func (p proveedorDoble) Paquetes([]string) ([]Paquete, error) {
	return []Paquete{{Nombre: "internal/git"}}, nil
}
func (p proveedorDoble) Importadores(string) ([]string, error) { return nil, nil }
func (p proveedorDoble) TestsDe(string) ([]string, error)      { return nil, nil }
func (p proveedorDoble) Completitud([]string) Completitud      { return p.completitud }

type proveedorContextoDoble struct{}

func (proveedorContextoDoble) Nombre() string { return "context-test" }
func (proveedorContextoDoble) Contexto([]string) ([]ReferenciaContexto, error) {
	return []ReferenciaContexto{{Ruta: "internal/git/diff.go", Explicacion: "importado por review"}}, nil
}

func TestGraphProviderExponeCompletitudVerificable(t *testing.T) {
	var proveedor GraphProvider = proveedorDoble{completitud: Completitud{
		Motivo:      "lenguaje_sin_proveedor",
		NoCubiertos: []string{"web/app.ts"},
	}}

	completitud := proveedor.Completitud([]string{"web/app.ts"})
	if completitud.Completo {
		t.Fatal("un proveedor incompleto no debe declararse completo")
	}
	if completitud.Motivo != "lenguaje_sin_proveedor" || !reflect.DeepEqual(completitud.NoCubiertos, []string{"web/app.ts"}) {
		t.Fatalf("la incompletitud perdió su explicación: %+v", completitud)
	}
}

func TestAutorizarAlcanceParcialFallaCerrado(t *testing.T) {
	alcance := AlcanceAfectado{
		Paquetes:    []string{"internal/git", "internal/review"},
		Tests:       []string{"./internal/git", "./internal/review"},
		Explicacion: []string{"internal/review importa internal/git"},
	}

	casos := []struct {
		nombre      string
		completitud Completitud
		autorizado  bool
	}{
		{nombre: "completo", completitud: Completitud{Completo: true}, autorizado: true},
		{nombre: "incompleto", completitud: Completitud{Motivo: "reflexion_detectada"}},
		{nombre: "valor cero"},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			recibido, autorizado := AutorizarAlcanceParcial(caso.completitud, alcance)
			if autorizado != caso.autorizado {
				t.Fatalf("autorizado = %v, se esperaba %v", autorizado, caso.autorizado)
			}
			if autorizado && !reflect.DeepEqual(recibido, alcance) {
				t.Fatalf("alcance autorizado = %+v, se esperaba %+v", recibido, alcance)
			}
			if !autorizado && !reflect.DeepEqual(recibido, AlcanceAfectado{}) {
				t.Fatalf("un rechazo filtró alcance parcial: %+v", recibido)
			}
		})
	}
}

func TestContextProviderNoPuedeAutorizarAlcance(t *testing.T) {
	var _ ContextProvider = proveedorContextoDoble{}

	tipoContexto := reflect.TypeOf((*ContextProvider)(nil)).Elem()
	tipoGrafo := reflect.TypeOf((*GraphProvider)(nil)).Elem()
	if tipoContexto.Implements(tipoGrafo) {
		t.Fatal("ContextProvider no debe satisfacer GraphProvider")
	}
}
