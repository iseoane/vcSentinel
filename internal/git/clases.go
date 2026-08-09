package git

import (
	"path/filepath"
	"strings"
)

// Clases de archivo. La clase describe QUÉ es un archivo; la capa
// (ClasificarCapa) solo decide en qué orden salen los lotes de slice. Son ejes
// distintos y no deben confundirse: un .md es clase docs, y la capa lo llama
// "backend" porque cae en el caso por defecto de ClasificarCapa.
const (
	ClaseSource   = "source"
	ClaseTest     = "test"
	ClaseConfig   = "config"
	ClaseGenerada = "generated"
	ClaseDocs     = "docs"
)

// ClaseArchivo determina la clase de un archivo por precedencia fija:
//
//	generado > test > documentación > configuración > código
//
// La precedencia importa: un .pb.go es generado aunque sea .go, y un
// testdata/config.yml es test aunque sea .yml. A diferencia de
// ClasificarCapa, no clasifica por subcadena suelta: "latest/" y "contest.go"
// son código, no tests.
func ClaseArchivo(ruta string) string {
	normalizada := filepath.ToSlash(ruta)
	base := filepath.Base(normalizada)
	ext := strings.ToLower(filepath.Ext(base))

	switch {
	case esGenerado(base, ext):
		return ClaseGenerada
	case esTest(base, normalizada):
		return ClaseTest
	case esDocumentacion(normalizada, ext):
		return ClaseDocs
	case esConfiguracion(base, ext):
		return ClaseConfig
	default:
		return ClaseSource
	}
}

// CuentaParaVolumen indica si una clase frena al guardián. El guardián mide
// revisabilidad de código, no bytes: la documentación y los archivos generados
// se informan aparte pero no bloquean. Un lock file de 2000 líneas no lo
// revisa nadie línea a línea, y bloquear por escribir documentación es
// fricción sin ninguna seguridad a cambio.
func CuentaParaVolumen(clase string) bool {
	return clase != ClaseDocs && clase != ClaseGenerada
}

// esGenerado reconoce lo que produce una herramienta y nadie edita a mano.
func esGenerado(base, ext string) bool {
	if ext == ".lock" {
		return true
	}
	if base == "go.sum" {
		return true
	}
	if strings.HasSuffix(base, "-lock.json") || strings.HasSuffix(base, "-lock.yaml") {
		return true
	}
	if strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_gen.go") ||
		strings.HasSuffix(base, ".gen.go") {
		return true
	}
	return strings.Contains(base, ".generated.")
}

// esTest reconoce tests por sufijo de archivo o por directorio dedicado, nunca
// por la subcadena "test" suelta: ese es exactamente el defecto de
// ClasificarCapa que hace pasar "latest/version.go" por un test.
func esTest(base, normalizada string) bool {
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.Contains(base, ".spec.") || strings.Contains(base, ".test.") {
		return true
	}
	for _, segmento := range strings.Split(normalizada, "/") {
		if segmento == "testdata" || segmento == "__tests__" || segmento == "__mocks__" {
			return true
		}
	}
	return false
}

// esDocumentacion reconoce prosa: por extensión o por vivir bajo docs/.
func esDocumentacion(normalizada, ext string) bool {
	switch ext {
	case ".md", ".markdown", ".rst", ".adoc":
		return true
	}
	return strings.HasPrefix(normalizada, "docs/") || strings.Contains(normalizada, "/docs/")
}

// esConfiguracion reconoce la configuración escrita a mano. No incluye lo
// generado, que ya se filtró antes: un docker-compose.yml es código que se
// ejecuta y sí cuenta para el volumen; un package-lock.json no.
func esConfiguracion(base, ext string) bool {
	switch ext {
	case ".json", ".yaml", ".yml", ".toml", ".ini", ".mod", ".cfg", ".conf", ".properties":
		return true
	}
	switch base {
	case "requirements.txt", "Dockerfile", "Makefile", ".gitattributes", ".gitignore":
		return true
	}
	return false
}
