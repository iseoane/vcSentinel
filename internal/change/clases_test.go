package change

import "testing"

// TestClasificarPorRuta cubre los falsos positivos reales de
// internal/git.ClasificarCapa (T3.1): esa función clasifica "test" por
// contener la subcadena "test" en la ruta, sin respetar límites de
// directorio ni de nombre de archivo.
func TestClasificarPorRuta(t *testing.T) {
	reglas := ReglasPorDefecto()
	casos := []struct {
		nombre string
		ruta   string
		quiere string
	}{
		{"directorio 'latest' no es test por subcadena", "latest/x.go", ClaseSource},
		{"contest.go no es test por subcadena", "contest.go", ClaseSource},
		{"_test.go legítimo sí es test", "internal/setup/install_test.go", ClaseTest},
		{"protobuf generado", "foo.pb.go", ClaseGenerated},
		{"go.sum generado", "go.sum", ClaseGenerated},
		{"Dockerfile es infra", "Dockerfile", ClaseInfra},
		{"workflow de github es ci", ".github/workflows/build.yml", ClaseCI},
		{"markdown en docs/ es docs", "docs/x.md", ClaseDocs},
		{"README suelto es docs", "README.md", ClaseDocs},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := ClasificarPorRuta(c.ruta, reglas); got != c.quiere {
				t.Errorf("ClasificarPorRuta(%q) = %q, quiere %q", c.ruta, got, c.quiere)
			}
		})
	}
}

// TestPrecedenciaPorOrdenDeDeclaracion verifica el contrato explícito de
// ClasificarPorRuta: gana la primera regla declarada que coincide, no la más
// específica ni la última (determinista y explicable solo por orden).
func TestPrecedenciaPorOrdenDeDeclaracion(t *testing.T) {
	especifica := Regla{Clase: ClaseInfra, Patrones: []string{"vendor/**/*.md"}}
	generica := Regla{Clase: ClaseDocs, Patrones: []string{"**/*.md"}}
	ruta := "vendor/pkg/README.md"

	if got := ClasificarPorRuta(ruta, []Regla{especifica, generica}); got != ClaseInfra {
		t.Errorf("con la específica declarada primero quiere %q, obtuve %q", ClaseInfra, got)
	}
	if got := ClasificarPorRuta(ruta, []Regla{generica, especifica}); got != ClaseDocs {
		t.Errorf("con la genérica declarada primero quiere %q, obtuve %q", ClaseDocs, got)
	}
}

// TestClasificar cubre la señal adicional de .gitattributes (sección 8 del
// documento de arquitectura, linguist-generated): un patrón sin "/" debe
// coincidir a cualquier profundidad (convención heredada de .gitignore), y la
// marca gana siempre sobre una regla de glob que clasificaría distinto.
func TestClasificar(t *testing.T) {
	reglas := ReglasPorDefecto()
	contenido := "*.pb.go linguist-generated=true\n*.md text\n"

	if got := Clasificar("api/foo.pb.go", reglas, contenido); got != ClaseGenerated {
		t.Errorf("foo.pb.go con linguist-generated quiere %q, obtuve %q", ClaseGenerated, got)
	}
	if got := Clasificar("README.md", reglas, contenido); got != ClaseDocs {
		t.Errorf("README.md sin linguist-generated quiere %q (por glob), obtuve %q", ClaseDocs, got)
	}
}
