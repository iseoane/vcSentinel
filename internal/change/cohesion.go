package change

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResultadoCohesion resume las componentes conexas de las rutas cambiadas.
type ResultadoCohesion struct {
	Clusters        int
	Puntuacion      float64
	SugerenciaSplit bool
	// Grupos conserva las rutas de cada componente en orden de entrada. Es
	// información operativa para slice; la salida pública de explain proyecta
	// solo el resumen estable de cohesión.
	Grupos [][]string
}

// Cohesion conecta rutas por co-cambio histórico y proximidad estructural.
// La puntuación es 0 si no hay rutas y 1/Clusters en otro caso: vale 1 para
// una única componente y decrece monótonamente al aumentar sus componentes.
//
// Regla inviolable de T3.5: esto es una SUGERENCIA, nunca se aplica
// automáticamente. Cohesion solo devuelve datos y no reescribe ni agrupa nada.
func Cohesion(rutas []string, git LectorGit) (ResultadoCohesion, error) {
	rutas = normalizarRutas(rutas)
	if len(rutas) == 0 {
		return ResultadoCohesion{}, nil
	}
	if git == nil {
		return ResultadoCohesion{}, fmt.Errorf("no se puede calcular la cohesión sin lector de git")
	}

	componentes := nuevoConjuntoDisjunto(len(rutas))
	conectarPorProximidad(rutas, componentes)

	porCommit, err := historialCoCambio(git, rutas)
	if err != nil {
		return ResultadoCohesion{}, err
	}
	conectarPorHistorial(porCommit, rutas, componentes)

	clusters := componentes.contar()
	return ResultadoCohesion{
		Clusters:        clusters,
		Puntuacion:      1 / float64(clusters),
		SugerenciaSplit: clusters > 1,
		Grupos:          gruposDeComponentes(rutas, componentes),
	}, nil
}

func gruposDeComponentes(rutas []string, componentes *conjuntoDisjunto) [][]string {
	indicePorRaiz := make(map[int]int)
	var grupos [][]string
	for i, ruta := range rutas {
		raiz := componentes.raiz(i)
		indice, existe := indicePorRaiz[raiz]
		if !existe {
			indice = len(grupos)
			indicePorRaiz[raiz] = indice
			grupos = append(grupos, nil)
		}
		grupos[indice] = append(grupos[indice], ruta)
	}
	return grupos
}

// loteMaximoRutasHistorial acota cuántas rutas van en un solo "git log --
// <rutas...>": pasar cientos de rutas sin trocear arriesga el límite de
// argumentos del proceso (ARG_MAX), justo en el caso que esta herramienta
// existe para detectar. No degrada con proximidad-solo si un lote falla:
// sigue el mismo criterio fail-fast que PerfilDeCambio ante un error de git,
// en vez de devolver una puntuación parcial sin avisar.
const loteMaximoRutasHistorial = 200

// historialCoCambio agrega, por SHA de commit, los archivos (del conjunto de
// rutas) que aparecieron en ese commit, FUSIONANDO entre lotes.
//
// Bug real corregido (revisión de T3.5, CRITICAL): con lotes independientes,
// "git log -- <lote>" solo reporta por commit los archivos de ESE lote. Dos
// rutas co-cambiadas en el mismo commit histórico pero repartidas en lotes
// distintos nunca coincidían bajo el mismo bloque tras concatenar texto: cada
// una aparecía en un bloque "commit:<mismo-sha>" separado, y
// conectarPorHistorial las trataba como grupos independientes, perdiendo la
// señal en silencio justo en el caso que loteMaximoRutasHistorial existe para
// cubrir (muchas rutas). Fusionar por SHA antes de conectar evita esto: un
// mismo commit acumula los archivos que le aporte cualquier lote.
func historialCoCambio(git LectorGit, rutas []string) (map[string][]string, error) {
	porCommit := map[string][]string{}
	for inicio := 0; inicio < len(rutas); inicio += loteMaximoRutasHistorial {
		fin := inicio + loteMaximoRutasHistorial
		if fin > len(rutas) {
			fin = len(rutas)
		}
		args := []string{"log", "--format=commit:%H", "--name-only", "--"}
		args = append(args, rutas[inicio:fin]...)
		salida, err := diffGit(git, "estas rutas", "leer el co-cambio histórico", args...)
		if err != nil {
			return nil, err
		}
		fusionarHistorial(salida, porCommit)
	}
	return porCommit, nil
}

// fusionarHistorial parsea un bloque "commit:<sha>" + rutas y ACUMULA sus
// archivos en porCommit[sha], en vez de sobrescribir: así una llamada
// posterior (otro lote) que reporte el mismo commit se suma a la anterior.
func fusionarHistorial(salida string, porCommit map[string][]string) {
	var commitActual string
	for _, linea := range strings.Split(salida, "\n") {
		linea = strings.TrimSpace(linea)
		if sha, esCommit := strings.CutPrefix(linea, "commit:"); esCommit {
			commitActual = sha
			continue
		}
		if linea == "" || commitActual == "" {
			continue
		}
		porCommit[commitActual] = append(porCommit[commitActual], linea)
	}
}

func normalizarRutas(rutas []string) []string {
	unicas := make(map[string]bool, len(rutas))
	resultado := make([]string, 0, len(rutas))
	for _, ruta := range rutas {
		if ruta == "" {
			continue
		}
		ruta = filepath.ToSlash(filepath.Clean(ruta))
		if !unicas[ruta] {
			unicas[ruta] = true
			resultado = append(resultado, ruta)
		}
	}
	return resultado
}

func conectarPorProximidad(rutas []string, componentes *conjuntoDisjunto) {
	directorios := make(map[string]int)
	modulos := make(map[string]int)
	for i, ruta := range rutas {
		directorio := filepath.ToSlash(filepath.Dir(ruta))
		conectarConPrimero(directorios, directorio, i, componentes)
		if modulo := moduloDeRuta(ruta); modulo != "" {
			conectarConPrimero(modulos, modulo, i, componentes)
		}
	}
}

func conectarConPrimero(grupo map[string]int, clave string, indice int, componentes *conjuntoDisjunto) {
	if primero, existe := grupo[clave]; existe {
		componentes.unir(primero, indice)
		return
	}
	grupo[clave] = indice
}

func conectarPorHistorial(porCommit map[string][]string, rutas []string, componentes *conjuntoDisjunto) {
	indices := make(map[string]int, len(rutas))
	for i, ruta := range rutas {
		indices[ruta] = i
	}

	for _, archivos := range porCommit {
		var indicesCommit []int
		for _, archivo := range archivos {
			if indice, existe := indices[filepath.ToSlash(archivo)]; existe {
				indicesCommit = append(indicesCommit, indice)
			}
		}
		for i := 1; i < len(indicesCommit); i++ {
			componentes.unir(indicesCommit[0], indicesCommit[i])
		}
	}
}

type conjuntoDisjunto struct {
	padre []int
}

func nuevoConjuntoDisjunto(tamano int) *conjuntoDisjunto {
	c := &conjuntoDisjunto{padre: make([]int, tamano)}
	for i := range c.padre {
		c.padre[i] = i
	}
	return c
}

func (c *conjuntoDisjunto) raiz(indice int) int {
	if c.padre[indice] != indice {
		c.padre[indice] = c.raiz(c.padre[indice])
	}
	return c.padre[indice]
}

func (c *conjuntoDisjunto) unir(a, b int) {
	c.padre[c.raiz(b)] = c.raiz(a)
}

func (c *conjuntoDisjunto) contar() int {
	raices := make(map[int]bool, len(c.padre))
	for i := range c.padre {
		raices[c.raiz(i)] = true
	}
	return len(raices)
}
