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

// ChangeSymbols es explícito aunque F3 no calcule símbolos: el cero significa
// «todavía sin grafo», no la ausencia accidental de la dimensión del contrato.
type ChangeSymbols struct {
	Added           int `json:"added"`
	Modified        int `json:"modified"`
	Deleted         int `json:"deleted"`
	ExportedTouched int `json:"exported_touched"`
}

type cambioRuta struct{ antes, despues string }

// PerfilDeCambio calcula las señales deterministas disponibles en F3 para el
// rango base..head; las señales que necesitan grafo se incorporan en F4.
func PerfilDeCambio(base, head string) (ChangeProfile, error) {
	rango := base + ".." + head
	rutas, err := rutasDelDiff(rango)
	if err != nil {
		return ChangeProfile{}, err
	}
	cambios, err := cambiosDeRuta(rango)
	if err != nil {
		return ChangeProfile{}, err
	}

	perfil := ChangeProfile{
		Base:        base,
		Head:        head,
		Size:        ChangeSize{Files: len(rutas)},
		Symbols:     ChangeSymbols{},
		Modules:     modulosDeRutas(rutas),
		FileClasses: conteoDeClases(rutas),
	}
	if perfil.Size.Added, perfil.Size.Deleted, err = lineasDelDiff(rango); err != nil {
		return ChangeProfile{}, err
	}
	if perfil.Size.Hunks, err = hunksDelDiff(rango); err != nil {
		return ChangeProfile{}, err
	}
	mensajes, err := salidaGit("log", "--format=%s", rango)
	if err != nil {
		return ChangeProfile{}, fmt.Errorf("no se pudieron leer los mensajes de %s: %w", rango, err)
	}
	perfil.Kind = claseDeCambio(rutas, cambios, base, head, mensajes)
	return perfil, nil
}
func salidaGit(args ...string) (string, error) {
	salida, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return string(salida), nil
}
func rutasDelDiff(rango string) ([]string, error) {
	salida, err := salidaGit("diff", "--name-only", "-z", "-M", rango)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron listar los archivos de %s: %w", rango, err)
	}
	return rutasNulas(salida), nil
}
func cambiosDeRuta(rango string) ([]cambioRuta, error) {
	salida, err := salidaGit("diff", "--name-status", "-z", "-M", rango)
	if err != nil {
		return nil, fmt.Errorf("no se pudieron listar los cambios de %s: %w", rango, err)
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
func lineasDelDiff(rango string) (int, int, error) {
	salida, err := salidaGit("diff", "--numstat", rango)
	if err != nil {
		return 0, 0, fmt.Errorf("no se pudieron medir las líneas de %s: %w", rango, err)
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
func hunksDelDiff(rango string) (int, error) {
	salida, err := salidaGit("diff", "--no-color", "--unified=0", rango)
	if err != nil {
		return 0, fmt.Errorf("no se pudieron medir los hunks de %s: %w", rango, err)
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
		partes := strings.Split(filepath.ToSlash(ruta), "/")
		if len(partes) >= 3 {
			conjunto[filepath.ToSlash(filepath.Join(partes[0], partes[1]))] = true
		}
	}
	modulos := make([]string, 0, len(conjunto))
	for modulo := range conjunto {
		modulos = append(modulos, modulo)
	}
	sort.Strings(modulos)
	return modulos
}

// claseDeCambio aplica el orden del contrato de mayor a menor prioridad: una
// señal inequívoca debe ocultar etiquetas más generales del mismo diff.
func claseDeCambio(rutas []string, cambios []cambioRuta, base, head, mensajes string) string {
	clases := conteoDeClases(rutas)
	switch {
	case clases[ClaseGenerated] > 0:
		return "generated"
	case tocaDependencias(rutas):
		return "dependency"
	case clases[ClaseInfra] > 0:
		return "infra"
	case clases[ClaseCI] > 0:
		return "ci_cd"
	case clases[ClaseConfig] > 0:
		return "configuration"
	case clases[ClaseDocs] > 0:
		return "documentation"
	case clases[ClaseTest] == len(rutas) && len(rutas) > 0:
		return "test_only"
	case esRefactor(cambios, base, head):
		return "refactor"
	case tocaCodigoExistente(cambios) && contieneBugfix(mensajes):
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

func esRefactor(cambios []cambioRuta, base, head string) bool {
	var antes, despues []funcionCambiada
	for _, cambio := range cambios {
		antes = append(antes, funcionesEnRevision(base, cambio.antes)...)
		despues = append(despues, funcionesEnRevision(head, cambio.despues)...)
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
func funcionesEnRevision(revision, ruta string) []funcionCambiada {
	if ruta == "" || !strings.HasSuffix(ruta, ".go") {
		return nil
	}
	contenido, err := salidaGit("show", revision+":"+filepath.ToSlash(ruta))
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
