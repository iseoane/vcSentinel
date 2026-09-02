package change

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// EstadoCaracteristica evita representar como false una señal que no pudo
// calcularse con la información disponible.
type EstadoCaracteristica string

const (
	CaracteristicaPresente      EstadoCaracteristica = "present"
	CaracteristicaAusente       EstadoCaracteristica = "absent"
	CaracteristicaIndeterminada EstadoCaracteristica = "indeterminate"
)

type Caracteristica struct {
	Nombre     string               `json:"name"`
	Estado     EstadoCaracteristica `json:"state"`
	Heuristica bool                 `json:"heuristic,omitempty"`
}

// EntradaCaracteristicas contiene señales ya derivadas del diff y de la
// política. Los mapas se indexan por rutas Git normalizadas con "/".
type EntradaCaracteristicas struct {
	Symbols           ChangeSymbols
	Rutas             []string
	LineasAnadidas    map[string][]string
	Contenidos        map[string]string
	Gitattributes     string
	PatronesData      []string
	PatronesSensibles []string
	MapaTests         map[string]bool
	// Reglas son las reglas de clasificación a usar; si está vacío, los
	// detectores caen en ReglasPorDefecto(). Un único punto de configuración
	// para todos, en vez de que unos detectores acepten reglas inyectadas y
	// otros llamen al global directamente (revisión de T3.3).
	Reglas []Regla
}

// marcasConcurrencia usan límite de palabra en las cuatro, no solo en "go ":
// una subcadena suelta ("context." dentro de un identificador más largo,
// p. ej.) es el mismo riesgo de falso positivo que ya se corrigió para "go "
// (habitual en castellano: "algo ", "tengo ", "luego ") (revisión de T3.3).
var marcasConcurrencia = []*regexp.Regexp{
	regexp.MustCompile(`\bgo `),
	regexp.MustCompile(`\bsync\.`),
	regexp.MustCompile(`\bchan `),
	regexp.MustCompile(`\bcontext\.`),
}

// clasesSinEvidenciaDeContenido lists the path classes whose added text is not
// evidence of anything a detector should act on: documentation is prose and
// generated files are output. Before this filter detectarSeguridadSensible and
// detectarConcurrencia read the lines of EVERY path, so a metrics artifact
// under docs/ holding "cached_input_tokens" marked security_sensitive, and any
// ficha discussing context.Background() marked concurrency. Both raised
// documentation commits above their real risk (FU-10, ticket 03b).
//
// It is a deny-list and not an allow-list on purpose: config and infra paths
// genuinely can hold credentials, and admitting only ClaseSource would lose
// them. For a risk detector, excluding less is the conservative direction.
//
// A word boundary is not the fix here: \btoken\b rejects cached_input_tokens,
// but it also rejects accessToken and refreshTokens, which is how credential
// identifiers are actually spelled in Go.
var clasesSinEvidenciaDeContenido = map[string]bool{
	ClaseDocs:      true,
	ClaseGenerated: true,
}

// admiteEvidenciaDeContenido reports whether the added lines of ruta may be
// read as evidence at all. One helper for every content-reading detector: two
// detectors excluding the same classes through two mechanisms is how the FU-10
// defect started.
func admiteEvidenciaDeContenido(e EntradaCaracteristicas, reglas []Regla, ruta string) bool {
	return !clasesSinEvidenciaDeContenido[Clasificar(ruta, reglas, e.Gitattributes)]
}

// reglasDe devuelve entrada.Reglas si se inyectaron, o los defaults si no:
// mismo criterio en todo detector que necesite clasificar rutas.
func reglasDe(e EntradaCaracteristicas) []Regla {
	if len(e.Reglas) > 0 {
		return e.Reglas
	}
	return ReglasPorDefecto()
}

// DetectarCaracteristicas ejecuta todos los detectores en un orden estable.
func DetectarCaracteristicas(entrada EntradaCaracteristicas) []Caracteristica {
	return []Caracteristica{
		detectarAPIPublica(entrada), detectarBaseDeDatos(entrada),
		detectarSeguridadSensible(entrada), detectarConcurrencia(entrada),
		detectarCambioDeComportamiento(entrada), detectarCoberturaDeTests(entrada),
		detectarCruceDeModulos(entrada), detectarCodigoGenerado(entrada),
		detectarCICD(entrada), detectarInfraestructura(entrada),
	}
}
func detectarAPIPublica(e EntradaCaracteristicas) Caracteristica {
	if !e.Symbols.Complete {
		return Caracteristica{"public_api", CaracteristicaIndeterminada, false}
	}
	return resultado("public_api", e.Symbols.ExportedTouched > 0)
}
func detectarBaseDeDatos(e EntradaCaracteristicas) Caracteristica {
	presente := rutasCoinciden(e.Rutas, e.PatronesData)
	for _, ruta := range e.Rutas {
		ruta = strings.ToLower(filepath.ToSlash(ruta))
		presente = presente || strings.HasSuffix(ruta, ".sql") || strings.Contains("/"+ruta+"/", "/migrations/") || strings.Contains("/"+ruta+"/", "/migration/")
	}
	return resultado("database", presente)
}
func detectarSeguridadSensible(e EntradaCaracteristicas) Caracteristica {
	claves := []string{"auth", "token", "crypto", "password", "secret"}
	reglas := reglasDe(e)
	presente := rutasCoinciden(e.Rutas, e.PatronesSensibles) || algunaLineaDeRuta(e.LineasAnadidas, func(ruta, linea string) bool {
		if !admiteEvidenciaDeContenido(e, reglas, ruta) {
			return false
		}
		linea = strings.ToLower(linea)
		for _, clave := range claves {
			if strings.Contains(linea, clave) {
				return true
			}
		}
		return false
	})
	return resultadoHeuristico("security_sensitive", presente)
}
func detectarConcurrencia(e EntradaCaracteristicas) Caracteristica {
	reglas := reglasDe(e)
	presente := algunaLineaDeRuta(e.LineasAnadidas, func(ruta, linea string) bool {
		if !admiteEvidenciaDeContenido(e, reglas, ruta) {
			return false
		}
		for _, marca := range marcasConcurrencia {
			if marca.MatchString(linea) {
				return true
			}
		}
		return false
	})
	return resultadoHeuristico("concurrency", presente)
}
func detectarCambioDeComportamiento(e EntradaCaracteristicas) Caracteristica {
	reglas := reglasDe(e)
	for _, ruta := range e.Rutas {
		if ClasificarPorRuta(ruta, reglas) != ClaseSource {
			continue
		}
		for _, linea := range lineasDeRuta(e.LineasAnadidas, ruta) {
			if esCodigo(linea) {
				return resultado("behavior_change", true)
			}
		}
	}
	return resultado("behavior_change", false)
}
func detectarCoberturaDeTests(e EntradaCaracteristicas) Caracteristica {
	paquetes, pruebas := map[string]bool{}, map[string]bool{}
	reglas := reglasDe(e)
	for _, ruta := range e.Rutas {
		clase := ClasificarPorRuta(ruta, reglas)
		if clase != ClaseSource && clase != ClaseTest {
			continue
		}
		paquete := path.Dir(filepath.ToSlash(ruta))
		paquetes[paquete] = true
		if clase == ClaseTest {
			pruebas[paquete] = true
		}
	}
	if len(paquetes) == 0 {
		return resultado("test_covered", false)
	}
	indeterminada := false
	for paquete := range paquetes {
		if pruebas[paquete] {
			continue
		}
		cubierto, conocido := testEnMapa(e.MapaTests, paquete)
		if !conocido {
			indeterminada = true
			continue
		}
		if !cubierto {
			return resultado("test_covered", false)
		}
	}
	if indeterminada {
		return Caracteristica{Nombre: "test_covered", Estado: CaracteristicaIndeterminada}
	}
	return resultado("test_covered", true)
}
func detectarCruceDeModulos(e EntradaCaracteristicas) Caracteristica {
	return resultado("cross_module", len(modulosDeRutas(e.Rutas)) >= 2)
}
func detectarCodigoGenerado(e EntradaCaracteristicas) Caracteristica {
	reglas := reglasDe(e)
	for _, ruta := range e.Rutas {
		if Clasificar(ruta, reglas, e.Gitattributes) == ClaseGenerated {
			return resultado("generated_code", true)
		}
	}
	marcador := func(contenido string) bool {
		contenido = strings.ToLower(contenido)
		return strings.Contains(contenido, "code generated") || strings.Contains(contenido, "@generated")
	}
	for _, contenido := range e.Contenidos {
		if marcador(contenido) {
			return resultado("generated_code", true)
		}
	}
	return resultado("generated_code", algunaLinea(e.LineasAnadidas, marcador))
}
func detectarCICD(e EntradaCaracteristicas) Caracteristica {
	return resultado("ci_cd", contieneClase(e, ClaseCI))
}
func detectarInfraestructura(e EntradaCaracteristicas) Caracteristica {
	return resultado("infrastructure", contieneClase(e, ClaseInfra))
}
func resultado(nombre string, presente bool) Caracteristica {
	return Caracteristica{Nombre: nombre, Estado: estado(presente)}
}

// resultadoHeuristico es resultado() para detectores que coinciden por
// subcadena/identificador en vez de una señal exacta (security_sensitive y
// concurrency): único punto de construcción también para
// el caso heurístico, en vez de que cada detector heurístico monte su propio
// Caracteristica{...} a mano (revisión de T3.3).
func resultadoHeuristico(nombre string, presente bool) Caracteristica {
	c := resultado(nombre, presente)
	c.Heuristica = true
	return c
}
func estado(presente bool) EstadoCaracteristica {
	if presente {
		return CaracteristicaPresente
	}
	return CaracteristicaAusente
}
func contieneClase(e EntradaCaracteristicas, clase string) bool {
	reglas := reglasDe(e)
	for _, ruta := range e.Rutas {
		if ClasificarPorRuta(ruta, reglas) == clase {
			return true
		}
	}
	return false
}
func rutasCoinciden(rutas, patrones []string) bool {
	for _, ruta := range rutas {
		for _, patron := range patrones {
			if coincideGlob(patron, filepath.ToSlash(ruta)) {
				return true
			}
		}
	}
	return false
}
func algunaLinea(lineas map[string][]string, cumple func(string) bool) bool {
	return algunaLineaDeRuta(lineas, func(_, linea string) bool { return cumple(linea) })
}

// algunaLineaDeRuta keeps the path alongside the line: a detector that
// classifies by path cannot use algunaLinea, which discards the map key.
func algunaLineaDeRuta(lineas map[string][]string, cumple func(ruta, linea string) bool) bool {
	for ruta, archivo := range lineas {
		for _, linea := range archivo {
			if cumple(ruta, linea) {
				return true
			}
		}
	}
	return false
}
func lineasDeRuta(lineas map[string][]string, ruta string) []string {
	ruta = filepath.ToSlash(ruta)
	for candidata, contenido := range lineas {
		if filepath.ToSlash(candidata) == ruta {
			return contenido
		}
	}
	return nil
}
func esCodigo(linea string) bool {
	linea = strings.TrimSpace(linea)
	return linea != "" && !strings.HasPrefix(linea, "//") && !strings.HasPrefix(linea, "/*") && !strings.HasPrefix(linea, "*") && !strings.HasPrefix(linea, "*/")
}
func testEnMapa(mapa map[string]bool, paquete string) (bool, bool) {
	for ruta, cubierto := range mapa {
		if filepath.ToSlash(ruta) == paquete {
			return cubierto, true
		}
	}
	return false, false
}
