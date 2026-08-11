package graph

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

type cargadorPaquetes func(*packages.Config, ...string) ([]*packages.Package, error)
type verificadorSnapshot func(string, string) error

type proveedorNativo struct {
	directorio string
	identidad  string
	cargar     cargadorPaquetes
	verificar  verificadorSnapshot
	imports    map[string][]string
	archivos   map[string]string
	tests      map[string]string
}

// NuevoProveedorNativo ancla cada carga a un directorio de snapshot y su tree OID.
func NuevoProveedorNativo(directorioSnapshot, identidadArbol string) *proveedorNativo {
	directorio := ""
	if directorioSnapshot != "" {
		directorio, _ = filepath.Abs(directorioSnapshot)
	}
	return &proveedorNativo{directorio: directorio, identidad: identidadArbol, cargar: packages.Load, verificar: verificarSnapshotGit}
}

func (*proveedorNativo) Nombre() string { return "native" }

func (p *proveedorNativo) Analizar(rutas []string) (ResultadoAnalisis, error) {
	rutas = normalizar(rutas)
	razones := []string{}
	if p.identidad == "" {
		razones = append(razones, "identidad de snapshot vacía")
	}
	if p.directorio == "" {
		razones = append(razones, "directorio de snapshot vacío")
	} else if directorio, err := filepath.EvalSymlinks(p.directorio); err != nil {
		razones = append(razones, "snapshot no resoluble: "+err.Error())
	} else {
		p.directorio = directorio
		if err := p.verificar(p.directorio, p.identidad); err != nil {
			razones = append(razones, "snapshot no verificable: "+err.Error())
		}
	}
	config := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedEmbedFiles | packages.NeedForTest,
		Dir:   p.directorio,
		Env:   append(os.Environ(), "GOWORK=off"),
		Tests: true,
	}
	var paquetes []*packages.Package
	var err error
	if p.directorio != "" {
		paquetes, err = p.cargar(config, "./...")
	}
	if err != nil {
		razones = append(razones, "error de carga: "+err.Error())
	}
	p.construirGrafo(paquetes, &razones)
	if len(paquetes) == 0 {
		razones = append(razones, "error de carga: no se cargaron paquetes")
	}

	alcance := alcanceAfectado{}
	noCubiertos := []string{}
	cambiados := map[string][]string{}
	for _, ruta := range rutas {
		paquete, cubierto := p.archivos[ruta]
		motivosRuta := []string{}
		resuelta, err := resolverEnSnapshot(p.directorio, ruta)
		if err != nil {
			motivosRuta = append(motivosRuta, err.Error())
			cubierto = false
		} else {
			motivosRuta = detectarRiesgos(resuelta, ruta)
		}
		if esConfiguracionGlobal(ruta) {
			motivosRuta = append(motivosRuta, fmt.Sprintf("configuración global cambiada: %s", ruta))
		}
		if !cubierto {
			motivosRuta = append(motivosRuta, fmt.Sprintf("archivo no pertenece a ningún paquete cargado: %s", ruta))
		} else {
			cambiados[paquete] = append(cambiados[paquete], ruta)
		}
		if len(motivosRuta) > 0 {
			razones = append(razones, motivosRuta...)
			noCubiertos = append(noCubiertos, ruta)
		}
	}
	p.expandirAlcance(cambiados, &alcance)
	if len(razones) > 0 && len(noCubiertos) == 0 {
		noCubiertos = append(noCubiertos, rutas...)
	}
	if p.directorio != "" {
		if err := p.verificar(p.directorio, p.identidad); err != nil {
			razones = append(razones, "snapshot no verificable al completar: "+err.Error())
		}
	}
	ordenarUnicos(&razones)
	ordenarUnicos(&noCubiertos)
	ordenarUnicos(&alcance.paquetes)
	ordenarUnicos(&alcance.tests)
	ordenarUnicos(&alcance.explicacion)
	evidencia := evidenciaCompletitud{completo: len(razones) == 0, noCubiertos: noCubiertos}
	if len(razones) > 0 {
		evidencia.motivo = strings.Join(razones, "; ")
	}
	return nuevoResultadoAnalisis(p.identidad, rutas, alcance, evidencia), nil
}

func (p *proveedorNativo) construirGrafo(paquetes []*packages.Package, razones *[]string) {
	p.imports, p.archivos, p.tests = map[string][]string{}, map[string]string{}, map[string]string{}
	for _, paquete := range paquetes {
		if strings.HasSuffix(paquete.PkgPath, ".test") {
			continue
		}
		for _, fallo := range paquete.Errors {
			*razones = append(*razones, fmt.Sprintf("error de carga de %s: %s", paquete.PkgPath, fallo.Msg))
		}
		if paquete.ForTest == "" {
			for importado := range paquete.Imports {
				p.imports[paquete.PkgPath] = append(p.imports[paquete.PkgPath], importado)
			}
		}
		for _, archivo := range append(append(paquete.GoFiles, paquete.OtherFiles...), paquete.EmbedFiles...) {
			resuelto, err := filepath.EvalSymlinks(archivo)
			if err != nil || !dentroDe(p.directorio, resuelto) {
				*razones = append(*razones, fmt.Sprintf("archivo de paquete fuera del snapshot: %s", archivo))
				continue
			}
			relativa, _ := filepath.Rel(p.directorio, resuelto)
			ruta := filepath.ToSlash(relativa)
			p.archivos[ruta] = paquete.PkgPath
			if strings.HasSuffix(ruta, "_test.go") {
				p.tests[paquete.PkgPath] = "./" + filepath.ToSlash(filepath.Dir(ruta))
			}
		}
	}
	for paquete := range p.imports {
		importados := p.imports[paquete]
		ordenarUnicos(&importados)
		p.imports[paquete] = importados
	}
}

func (p *proveedorNativo) expandirAlcance(cambiados map[string][]string, alcance *alcanceAfectado) {
	inversos := map[string][]string{}
	for importador, imports := range p.imports {
		for _, importado := range imports {
			inversos[importado] = append(inversos[importado], importador)
		}
	}
	cola := make([]string, 0, len(cambiados))
	cadenas := map[string]string{}
	for paquete, rutas := range cambiados {
		ordenarUnicos(&rutas)
		cola = append(cola, paquete)
		cadenas[paquete] = paquete
		alcance.explicacion = append(alcance.explicacion, fmt.Sprintf("%s: cambio directo en %s", paquete, strings.Join(rutas, ", ")))
	}
	sort.Strings(cola)
	for i := 0; i < len(cola); i++ {
		paquete := cola[i]
		alcance.paquetes = append(alcance.paquetes, paquete)
		if test := p.tests[paquete]; test != "" {
			alcance.tests = append(alcance.tests, test)
		}
		importadores := inversos[paquete]
		ordenarUnicos(&importadores)
		for _, importador := range importadores {
			if _, visto := cadenas[importador]; visto {
				continue
			}
			cadenas[importador] = importador + " -> " + cadenas[paquete]
			alcance.explicacion = append(alcance.explicacion, fmt.Sprintf("%s: importa %s (ruta %s)", importador, paquete, cadenas[importador]))
			cola = append(cola, importador)
		}
	}
}

func detectarRiesgos(archivoResuelto, ruta string) []string {
	if filepath.Ext(ruta) != ".go" {
		return nil
	}
	contenido, err := os.ReadFile(archivoResuelto)
	if err != nil {
		return []string{fmt.Sprintf("archivo no legible: %s", ruta)}
	}
	razones := []string{}
	if strings.Contains(string(contenido), "//go:linkname") {
		razones = append(razones, "directiva go:linkname en "+ruta)
	}
	archivo, _ := parser.ParseFile(token.NewFileSet(), ruta, contenido, parser.ImportsOnly)
	if archivo != nil {
		for _, importacion := range archivo.Imports {
			nombre, _ := strconv.Unquote(importacion.Path.Value)
			if nombre == "reflect" || nombre == "plugin" {
				razones = append(razones, "uso de "+nombre+" en "+ruta)
			}
		}
	}
	return razones
}

func esConfiguracionGlobal(ruta string) bool {
	ruta = strings.ToLower(filepath.ToSlash(ruta))
	base := filepath.Base(filepath.FromSlash(ruta))
	return ruta == "go.mod" || ruta == "go.sum" || base == "makefile" || base == "dockerfile" ||
		strings.HasPrefix(ruta, ".github/workflows/") || base == ".gitlab-ci.yml" || base == "azure-pipelines.yml" ||
		base == "build.sh" || base == "build.bat" || base == "build.cmd" || base == "build.ps1" ||
		base == "build.yml" || base == "build.yaml" || base == "build.xml"
}

func verificarSnapshotGit(directorio, esperado string) error {
	arbol, err := gitSnapshot(directorio, "rev-parse", "HEAD^{tree}")
	if err != nil || strings.TrimSpace(arbol) != esperado {
		return fmt.Errorf("tree OID distinto del esperado")
	}
	estado, err := gitSnapshot(directorio, "status", "--porcelain", "--untracked-files=all")
	if err != nil || strings.TrimSpace(estado) != "" {
		return fmt.Errorf("directorio sucio o con archivos no rastreados")
	}
	return nil
}

func gitSnapshot(directorio string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", directorio}, args...)...)
	salida, err := cmd.CombinedOutput()
	return string(salida), err
}

func resolverEnSnapshot(directorio, ruta string) (string, error) {
	nativa := filepath.FromSlash(ruta)
	if ruta == ".." || strings.HasPrefix(ruta, "../") || filepath.IsAbs(nativa) {
		return "", fmt.Errorf("ruta fuera del snapshot: %s", ruta)
	}
	resuelta, err := filepath.EvalSymlinks(filepath.Join(directorio, nativa))
	if err != nil {
		return "", fmt.Errorf("ruta no resoluble en snapshot: %s", ruta)
	}
	if !dentroDe(directorio, resuelta) {
		return "", fmt.Errorf("ruta fuera del snapshot: %s", ruta)
	}
	return resuelta, nil
}

func dentroDe(directorio, ruta string) bool {
	relativa, err := filepath.Rel(directorio, ruta)
	return err == nil && relativa != ".." && !strings.HasPrefix(relativa, ".."+string(filepath.Separator))
}

func normalizar(rutas []string) []string {
	resultado := make([]string, 0, len(rutas))
	for _, ruta := range rutas {
		resultado = append(resultado, filepath.ToSlash(filepath.Clean(filepath.FromSlash(ruta))))
	}
	ordenarUnicos(&resultado)
	return resultado
}

func ordenarUnicos(valores *[]string) {
	sort.Strings(*valores)
	destino := (*valores)[:0]
	for _, valor := range *valores {
		if len(destino) == 0 || destino[len(destino)-1] != valor {
			destino = append(destino, valor)
		}
	}
	*valores = destino
}
