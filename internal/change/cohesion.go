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

	historial, err := historialCoCambio(git, rutas)
	if err != nil {
		return ResultadoCohesion{}, err
	}
	conectarPorHistorial(historial, rutas, componentes)

	clusters := componentes.contar()
	return ResultadoCohesion{
		Clusters:        clusters,
		Puntuacion:      1 / float64(clusters),
		SugerenciaSplit: clusters > 1,
	}, nil
}

// loteMaximoRutasHistorial acota cuántas rutas van en un solo "git log --
// <rutas...>": pasar cientos de rutas sin trocear arriesga el límite de
// argumentos del proceso (ARG_MAX), justo en el caso que esta herramienta
// existe para detectar (revisión de T3.5). No degrada con proximidad-solo si
// un lote falla: sigue el mismo criterio fail-fast que PerfilDeCambio ante un
// error de git, en vez de devolver una puntuación parcial sin avisar.
const loteMaximoRutasHistorial = 200

// historialCoCambio agrega el historial de "git log --name-only" en lotes
// para no superar loteMaximoRutasHistorial rutas por invocación.
func historialCoCambio(git LectorGit, rutas []string) (string, error) {
	var historial strings.Builder
	for inicio := 0; inicio < len(rutas); inicio += loteMaximoRutasHistorial {
		fin := inicio + loteMaximoRutasHistorial
		if fin > len(rutas) {
			fin = len(rutas)
		}
		args := []string{"log", "--format=commit:%H", "--name-only", "--"}
		args = append(args, rutas[inicio:fin]...)
		salida, err := diffGit(git, "estas rutas", "leer el co-cambio histórico", args...)
		if err != nil {
			return "", err
		}
		historial.WriteString(salida)
		historial.WriteString("\n")
	}
	return historial.String(), nil
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

func conectarPorHistorial(historial string, rutas []string, componentes *conjuntoDisjunto) {
	indices := make(map[string]int, len(rutas))
	for i, ruta := range rutas {
		indices[ruta] = i
	}

	var commit []int
	unirCommit := func() {
		for i := 1; i < len(commit); i++ {
			componentes.unir(commit[0], commit[i])
		}
		commit = nil
	}
	for _, linea := range strings.Split(historial, "\n") {
		linea = strings.TrimSpace(linea)
		if strings.HasPrefix(linea, "commit:") {
			unirCommit()
			continue
		}
		if indice, existe := indices[filepath.ToSlash(linea)]; existe {
			commit = append(commit, indice)
		}
	}
	unirCommit()
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
