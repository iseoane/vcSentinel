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
	Rutas             []string
	LineasAnadidas    map[string][]string
	Contenidos        map[string]string
	Gitattributes     string
	PatronesAPI       []string
	PatronesData      []string
	PatronesSensibles []string
	MapaTests         map[string]bool
	// Reglas son las reglas de clasificación a usar; si está vacío, los
	// detectores caen en ReglasPorDefecto(). Un único punto de configuración
	// para todos, en vez de que unos detectores acepten reglas inyectadas y
	// otros llamen al global directamente (revisión de T3.3).
	Reglas []Regla
}

var identificadorExportado = regexp.MustCompile(`\b[A-Z][A-Za-z0-9_]*\b`)

// marcaGoStatement exige un límite de palabra antes de "go ": sin él,
// "algo ", "tengo ", "luego ", "largo " (habituales en comentarios e
// identificadores en castellano) disparaban un falso positivo de
// concurrencia (revisión de T3.3).
var marcaGoStatement = regexp.MustCompile(`\bgo `)

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
	presente := rutasCoinciden(e.Rutas, e.PatronesAPI) || algunaLinea(e.LineasAnadidas, func(linea string) bool {
		return esCodigo(linea) && identificadorExportado.MatchString(linea)
	})
	return Caracteristica{Nombre: "public_api", Estado: estado(presente), Heuristica: true}
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
	presente := rutasCoinciden(e.Rutas, e.PatronesSensibles) || algunaLinea(e.LineasAnadidas, func(linea string) bool {
		linea = strings.ToLower(linea)
		for _, clave := range claves {
			if strings.Contains(linea, clave) {
				return true
			}
		}
		return false
	})
	return Caracteristica{Nombre: "security_sensitive", Estado: estado(presente), Heuristica: true}
}
func detectarConcurrencia(e EntradaCaracteristicas) Caracteristica {
	marcas := []string{"sync.", "chan ", "context."}
	presente := algunaLinea(e.LineasAnadidas, func(linea string) bool {
		if marcaGoStatement.MatchString(linea) {
			return true
		}
		for _, marca := range marcas {
			if strings.Contains(linea, marca) {
				return true
			}
		}
		return false
	})
	return Caracteristica{Nombre: "concurrency", Estado: estado(presente), Heuristica: true}
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
	for _, archivo := range lineas {
		for _, linea := range archivo {
			if cumple(linea) {
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
