package graph_test

import (
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

type proveedorContextoDoble struct{}

func (proveedorContextoDoble) Nombre() string { return "context-test" }
func (proveedorContextoDoble) Contexto(string, []string) ([]review.Reference, error) {
	return []review.Reference{{Path: "internal/git/diff_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}}, nil
}

func TestValorPublicoNoPuedeAutorizarAlcance(t *testing.T) {
	resultado := graph.ResultadoAnalisis{}

	if _, autorizado := graph.AutorizarAlcanceParcial(resultado); autorizado {
		t.Fatal("el valor cero construido fuera de graph autorizó alcance parcial")
	}
	if resultado.Completo() || len(resultado.RutasAnalizadas()) != 0 || len(resultado.Alcance().Paquetes()) != 0 {
		t.Fatal("el valor cero expuso evidencia de confianza")
	}
}

func TestContextProviderSoloAportaContexto(t *testing.T) {
	var proveedor review.ContextProvider = proveedorContextoDoble{}

	referencias, err := proveedor.Contexto("head", []string{"internal/git/diff.go"})
	if err != nil || len(referencias) != 1 {
		t.Fatalf("contexto = %+v, error = %v", referencias, err)
	}
	if _, autorizado := graph.AutorizarAlcanceParcial(graph.ResultadoAnalisis{}); autorizado {
		t.Fatal("el contexto independiente autorizó alcance parcial")
	}
}
