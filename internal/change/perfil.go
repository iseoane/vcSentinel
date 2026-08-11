package change

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ChangeProfile mantiene el vocabulario del contrato estable para que los
// consumidores puedan persistirlo sin traducir sus campos.
type ChangeProfile struct {
	Base        string         `json:"base"`
	Head        string         `json:"head"`
	Kind        string         `json:"kind"`
	Size        ChangeSize     `json:"size"`
	Symbols     ChangeSymbols  `json:"symbols"`
	Modules     []string       `json:"modules"`
	FileClasses map[string]int `json:"file_classes"`
}

// ChangeSize conserva las métricas del diff separadas porque ninguna sustituye
// a las demás al decidir el riesgo de un cambio.
type ChangeSize struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	Hunks   int `json:"hunks"`
}

// ChangeSymbols resume la comparación AST; Complete distingue cero cambios de
// una comparación que no pudo ser exacta.
type ChangeSymbols struct {
	Added           int  `json:"added"`
	Modified        int  `json:"modified"`
	Deleted         int  `json:"deleted"`
	ExportedTouched int  `json:"exported_touched"`
	Complete        bool `json:"complete"`
}

type cambioRuta struct{ antes, despues string }

// LectorGit abstrae las lecturas de git que necesita este paquete: una
// función en vez de una interfaz con métodos porque todas comparten la misma
// forma (argumentos -> salida cruda, error) y la única variación real son los
// argumentos. Desacopla claseDeCambio/esRefactor de invocar git real y
// permite testearlos con dobles de prueba (revisión de T3.2).
type LectorGit func(args ...string) (string, error)

// PerfilDeCambio calcula señales deterministas sobre los árboles inmutables del rango.
func PerfilDeCambio(base, head string) (ChangeProfile, error) {
	return perfilDeCambioCon(base, head, salidaGit)
}

// perfilDeCambioCon es PerfilDeCambio con el lector de git inyectado: variante
// testeable sin invocar git real.
func perfilDeCambioCon(base, head string, git LectorGit) (ChangeProfile, error) {
	baseTree, err := arbolDeRevision(git, base)
	if err != nil {
		return ChangeProfile{}, err
	}
	headTree, err := arbolDeRevision(git, head)
	if err != nil {
		return ChangeProfile{}, err
	}
	rango := baseTree + ".." + headTree
	rutas, err := rutasDelDiff(git, rango)
	if err != nil {
		return ChangeProfile{}, err
	}
	cambios, err := cambiosDeRuta(git, rango)
	if err != nil {
		return ChangeProfile{}, err
	}
	reglas := ReglasPorDefecto()
	clasificar := func(ruta string) string { return ClasificarPorRuta(ruta, reglas) }

	perfil := ChangeProfile{
		Base:        base,
		Head:        head,
		Size:        ChangeSize{Files: len(rutas)},
		Symbols:     simbolosCambiados(git, cambios, baseTree, headTree, clasificar),
		Modules:     modulosDeRutas(rutas),
		FileClasses: conteoDeClases(rutas),
	}
	if perfil.Size.Added, perfil.Size.Deleted, err = lineasDelDiff(git, rango); err != nil {
		return ChangeProfile{}, err
	}
	if perfil.Size.Hunks, err = hunksDelDiff(git, rango); err != nil {
		return ChangeProfile{}, err
	}
	mensajes, err := diffGit(git, base+".."+head, "leer los mensajes", "log", "--format=%s", base+".."+head)
	if err != nil {
		return ChangeProfile{}, err
	}
	perfil.Kind = claseDeCambio(git, entradaClasificacion{rutas: rutas, cambios: cambios, base: baseTree, head: headTree, mensajes: mensajes})
	return perfil, nil
}

func arbolDeRevision(git LectorGit, revision string) (string, error) {
	salida, err := diffGit(git, revision, "resolver el árbol", "rev-parse", "--verify", "--end-of-options", revision+"^{tree}")
	return strings.TrimSpace(salida), err
}

// salidaGit es la implementación real de LectorGit, sobre la CLI de git.
func salidaGit(args ...string) (string, error) {
	salida, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return string(salida), nil
}

// diffGit ejecuta una lectura de git y envuelve el error con la descripción
// de la acción en un solo lugar, en vez de repetir el mismo fmt.Errorf en
// cada función de lectura (revisión de T3.2).
func diffGit(git LectorGit, rango, accion string, args ...string) (string, error) {
	salida, err := git(args...)
	if err != nil {
		return "", fmt.Errorf("no se pudieron %s de %s: %w", accion, rango, err)
	}
	return salida, nil
}

func rutasDelDiff(git LectorGit, rango string) ([]string, error) {
	salida, err := diffGit(git, rango, "listar los archivos", "diff", "--name-only", "-z", "-M", rango)
	if err != nil {
		return nil, err
	}
	return rutasNulas(salida), nil
}
func cambiosDeRuta(git LectorGit, rango string) ([]cambioRuta, error) {
	salida, err := diffGit(git, rango, "listar los cambios", "diff", "--name-status", "-z", "-M", rango)
	if err != nil {
		return nil, err
	}
	partes := rutasNulas(salida)
	var cambios []cambioRuta
	for i := 0; i < len(partes); {
		estado := partes[i]
		i++
		if estado == "" || i >= len(partes) {
			return nil, fmt.Errorf("estado de archivo inválido en %s", rango)
		}
		switch estado[0] {
		case 'R', 'C':
			if i+1 >= len(partes) {
				return nil, fmt.Errorf("estado de rename inválido en %s", rango)
			}
			cambios = append(cambios, cambioRuta{antes: partes[i], despues: partes[i+1]})
			i += 2
		case 'M', 'T':
			cambios = append(cambios, cambioRuta{antes: partes[i], despues: partes[i]})
			i++
		case 'D':
			cambios = append(cambios, cambioRuta{antes: partes[i]})
			i++
		default:
			cambios = append(cambios, cambioRuta{despues: partes[i]})
			i++
		}
	}
	return cambios, nil
}
func rutasNulas(salida string) []string {
	partes := strings.Split(strings.TrimSuffix(salida, "\x00"), "\x00")
	if len(partes) == 1 && partes[0] == "" {
		return nil
	}
	return partes
}
func lineasDelDiff(git LectorGit, rango string) (int, int, error) {
	salida, err := diffGit(git, rango, "medir las líneas", "diff", "--numstat", rango)
	if err != nil {
		return 0, 0, err
	}
	var added, deleted int
	for _, linea := range strings.Split(salida, "\n") {
		campos := strings.SplitN(linea, "\t", 3)
		if len(campos) < 2 || campos[0] == "-" || campos[1] == "-" {
			continue
		}
		a, errA := strconv.Atoi(campos[0])
		b, errB := strconv.Atoi(campos[1])
		if errA == nil && errB == nil {
			added += a
			deleted += b
		}
	}
	return added, deleted, nil
}
func hunksDelDiff(git LectorGit, rango string) (int, error) {
	salida, err := diffGit(git, rango, "medir los hunks", "diff", "--no-color", "--unified=0", rango)
	if err != nil {
		return 0, err
	}
	hunks := 0
	for _, linea := range strings.Split(salida, "\n") {
		if strings.HasPrefix(linea, "@@ ") {
			hunks++
		}
	}
	return hunks, nil
}
func conteoDeClases(rutas []string) map[string]int {
	conteo := map[string]int{ClaseSource: 0, ClaseTest: 0, ClaseConfig: 0, ClaseGenerated: 0, ClaseDocs: 0, ClaseInfra: 0, ClaseCI: 0}
	reglas := ReglasPorDefecto()
	for _, ruta := range rutas {
		conteo[ClasificarPorRuta(ruta, reglas)]++
	}
	return conteo
}
func modulosDeRutas(rutas []string) []string {
	conjunto := make(map[string]bool)
	for _, ruta := range rutas {
		if modulo := moduloDeRuta(ruta); modulo != "" {
			conjunto[modulo] = true
		}
	}
	modulos := make([]string, 0, len(conjunto))
	for modulo := range conjunto {
		modulos = append(modulos, modulo)
	}
	sort.Strings(modulos)
	return modulos
}

// profundidadModulo son los segmentos mínimos de ruta para que exista un
// "módulo" (dos primeros directorios, p. ej. "internal/review"): menos que
// eso es la raíz del repo o un solo directorio, no un módulo identificable.
const profundidadModulo = 3

// moduloDeRuta es el único punto de verdad del criterio de "módulo" del
// paquete: modulosDeRutas (cross_module) y Cohesion (proximidad estructural,
// T3.5) lo comparten en vez de reimplementar cada uno su propio corte de
// segmentos (revisión de T3.5).
func moduloDeRuta(ruta string) string {
	partes := strings.Split(filepath.ToSlash(ruta), "/")
	if len(partes) < profundidadModulo {
		return ""
	}
	return filepath.ToSlash(filepath.Join(partes[0], partes[1]))
}

// entradaClasificacion agrupa las señales ya derivadas (rutas, cambios) y los
// identificadores/texto crudos (base, head, mensajes) que claseDeCambio
// necesita: un struct en vez de 5 parámetros posicionales, para que sumar una
// señal nueva en F4 no siga ampliando la firma (revisión de T3.2).
type entradaClasificacion struct {
	rutas    []string
	cambios  []cambioRuta
	base     string
	head     string
	mensajes string
}

// claseDeCambio aplica el orden del contrato de mayor a menor prioridad: una
// señal inequívoca debe ocultar etiquetas más generales del mismo diff.
func claseDeCambio(git LectorGit, entrada entradaClasificacion) string {
	clases := conteoDeClases(entrada.rutas)
	switch {
	case clases[ClaseGenerated] > 0:
		return "generated"
	case tocaDependencias(entrada.rutas):
		return "dependency"
	case clases[ClaseInfra] > 0:
		return "infra"
	case clases[ClaseCI] > 0:
		return "ci_cd"
	case clases[ClaseConfig] > 0:
		return "configuration"
	case clases[ClaseDocs] > 0:
		return "documentation"
	case clases[ClaseTest] == len(entrada.rutas) && len(entrada.rutas) > 0:
		return "test_only"
	case esRefactor(git, entrada.cambios, entrada.base, entrada.head):
		return "refactor"
	case tocaCodigoExistente(entrada.cambios) && contieneBugfix(entrada.mensajes):
		return "bugfix"
	default:
		return "feature"
	}
}
func tocaDependencias(rutas []string) bool {
	for _, ruta := range rutas {
		switch filepath.ToSlash(ruta) {
		case "go.mod", "go.work", "go.work.sum", "vendor/modules.txt", "package.json", "Cargo.toml", "requirements.txt", "pyproject.toml":
			return true
		}
	}
	return false
}
func tocaCodigoExistente(cambios []cambioRuta) bool {
	reglas := ReglasPorDefecto()
	for _, cambio := range cambios {
		if cambio.antes != "" && ClasificarPorRuta(cambio.antes, reglas) == ClaseSource {
			return true
		}
	}
	return false
}
func contieneBugfix(mensajes string) bool {
	for _, mensaje := range strings.Split(mensajes, "\n") {
		mensaje = strings.ToLower(strings.TrimSpace(mensaje))
		if strings.HasPrefix(mensaje, "fix:") || strings.HasPrefix(mensaje, "fix(") {
			return true
		}
	}
	return false
}

type funcionCambiada struct{ nombre, cuerpo, ruta string }

func esRefactor(git LectorGit, cambios []cambioRuta, base, head string) bool {
	var antes, despues []funcionCambiada
	for _, cambio := range cambios {
		antes = append(antes, funcionesEnRevision(git, base, cambio.antes)...)
		despues = append(despues, funcionesEnRevision(git, head, cambio.despues)...)
	}
	for _, antigua := range antes {
		for _, nueva := range despues {
			if antigua.cuerpo == nueva.cuerpo && (antigua.nombre != nueva.nombre || antigua.ruta != nueva.ruta) {
				return true
			}
		}
	}
	return false
}
func funcionesEnRevision(git LectorGit, revision, ruta string) []funcionCambiada {
	if ruta == "" || !strings.HasSuffix(ruta, ".go") {
		return nil
	}
	contenido, err := git("show", revision+":"+filepath.ToSlash(ruta))
	if err != nil {
		return nil
	}
	archivo, err := parser.ParseFile(token.NewFileSet(), ruta, contenido, 0)
	if err != nil {
		return nil
	}
	var funciones []funcionCambiada
	for _, declaracion := range archivo.Decls {
		funcion, ok := declaracion.(*ast.FuncDecl)
		if !ok || funcion.Body == nil {
			continue
		}
		var cuerpo bytes.Buffer
		if format.Node(&cuerpo, token.NewFileSet(), funcion.Body) == nil {
			funciones = append(funciones, funcionCambiada{funcion.Name.Name, cuerpo.String(), filepath.ToSlash(ruta)})
		}
	}
	return funciones
}
