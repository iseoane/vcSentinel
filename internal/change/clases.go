// Package change clasifica archivos en clases deterministas (source, test,
// config, generated, docs, infra, ci) vía globs con precedencia por orden de
// declaración. Corrige el falso positivo de internal/git.ClasificarCapa, que
// clasifica "test" por subcadena en la ruta (p. ej. "latest/x.go").
package change

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Clases reconocidas por el contrato de file_classes (ver
// docs/design/replanteamiento-objetivo.md, sección 8).
const (
	ClaseSource    = "source"
	ClaseTest      = "test"
	ClaseConfig    = "config"
	ClaseGenerated = "generated"
	ClaseDocs      = "docs"
	ClaseInfra     = "infra"
	ClaseCI        = "ci"
)

// Regla asocia una clase con la lista de globs que la activan. El orden de
// las Reglas en el slice ES la precedencia: la primera que coincide gana.
type Regla struct {
	Clase    string
	Patrones []string
}

// ReglasPorDefecto son las reglas sensatas por defecto (Go como lenguaje
// principal del repo). Lo que no coincide con ninguna cae en ClaseSource.
func ReglasPorDefecto() []Regla {
	return []Regla{
		{Clase: ClaseTest, Patrones: []string{"**/*_test.go", "**/testdata/**", "**/*.spec.ts", "**/*.spec.tsx"}},
		{Clase: ClaseGenerated, Patrones: []string{"**/*.pb.go", "**/*_gen.go", "**/*.lock", "go.sum"}},
		{Clase: ClaseInfra, Patrones: []string{"Dockerfile", "**/Dockerfile", "**/*.tf", "infra/**"}},
		{Clase: ClaseCI, Patrones: []string{".github/workflows/**", ".gitlab-ci.yml"}},
		{Clase: ClaseDocs, Patrones: []string{"**/*.md", "docs/**"}},
		{Clase: ClaseConfig, Patrones: []string{"**/*.json", "**/*.yaml", "**/*.yml", "**/*.toml", "go.mod"}},
	}
}

// ClasificarPorRuta clasifica ruta según reglas: gana la primera cuyo glob
// coincide (determinista por posición, no por "match más específico"). Sin
// coincidencias, el archivo es ClaseSource, el catch-all del contrato.
func ClasificarPorRuta(ruta string, reglas []Regla) string {
	rutaNormalizada := filepath.ToSlash(ruta)
	for _, regla := range reglas {
		for _, patron := range regla.Patrones {
			if coincideGlob(patron, rutaNormalizada) {
				return regla.Clase
			}
		}
	}
	return ClaseSource
}

// esGeneradoPorGitAttributes indica si contenido (formato .gitattributes)
// marca ruta con linguist-generated: señal adicional a los globs. Solo la
// usa Clasificar, no es API pública del paquete.
func esGeneradoPorGitAttributes(contenido, ruta string) bool {
	rutaNormalizada := filepath.ToSlash(ruta)
	for _, linea := range strings.Split(contenido, "\n") {
		campos := strings.Fields(linea)
		if len(campos) < 2 || strings.HasPrefix(campos[0], "#") {
			continue
		}
		for _, atributo := range campos[1:] {
			if atributo == "linguist-generated" || atributo == "linguist-generated=true" {
				if coincideGlob(patronGitAttributes(campos[0]), rutaNormalizada) {
					return true
				}
				break
			}
		}
	}
	return false
}

// patronGitAttributes adapta patron a la convención .gitattributes (heredada
// de .gitignore): sin "/" coincide a cualquier profundidad, no solo en la raíz.
func patronGitAttributes(patron string) string {
	if strings.Contains(patron, "/") {
		return patron
	}
	return "**/" + patron
}

// Clasificar combina la señal de .gitattributes con las reglas de globs:
// linguist-generated gana siempre, antes de evaluar ninguna regla de ruta.
func Clasificar(ruta string, reglas []Regla, gitattributes string) string {
	if esGeneradoPorGitAttributes(gitattributes, ruta) {
		return ClaseGenerated
	}
	return ClasificarPorRuta(ruta, reglas)
}

// coincideGlob traduce patron ("**" de profundidad arbitraria) a regex y
// evalúa ruta: filepath.Match no soporta "**" cruzando directorios.
func coincideGlob(patron, ruta string) bool {
	re, err := regexp.Compile(traducirGlobARegex(patron))
	if err != nil {
		return false
	}
	return re.MatchString(ruta)
}

// traducirGlobARegex: "**/" = cero o más directorios; "**" sola = cualquier
// resto; "*"/"?" no cruzan "/"; el resto se escapa literal.
func traducirGlobARegex(patron string) string {
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(patron); {
		switch {
		case strings.HasPrefix(patron[i:], "**/"):
			out.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(patron[i:], "**"):
			out.WriteString(".*")
			i += 2
		case patron[i] == '*':
			out.WriteString("[^/]*")
			i++
		case patron[i] == '?':
			out.WriteString("[^/]")
			i++
		default:
			out.WriteString(regexp.QuoteMeta(string(patron[i])))
			i++
		}
	}
	out.WriteString("$")
	return out.String()
}
