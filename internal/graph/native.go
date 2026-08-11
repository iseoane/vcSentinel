package graph

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

type cargadorPaquetes func(*packages.Config, ...string) ([]*packages.Package, error)

type proveedorNativo struct {
	directorio string
	identidad  string
	cargar     cargadorPaquetes
	imports    map[string][]string
	archivos   map[string]string
	tests      map[string]bool
}

// NuevoProveedorNativo ancla cada carga a un directorio de snapshot y su tree OID.
func NuevoProveedorNativo(directorioSnapshot, identidadArbol string) *proveedorNativo {
	directorio := ""
	if directorioSnapshot != "" {
		directorio, _ = filepath.Abs(directorioSnapshot)
	}
	return &proveedorNativo{directorio: directorio, identidad: identidadArbol, cargar: packages.Load}
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
	for _, ruta := range rutas {
		paquete, cubierto := p.archivos[ruta]
		motivosRuta := []string{}
		if ruta == ".." || strings.HasPrefix(ruta, "../") || filepath.IsAbs(filepath.FromSlash(ruta)) {
			motivosRuta = append(motivosRuta, fmt.Sprintf("ruta fuera del snapshot: %s", ruta))
			cubierto = false
		} else {
			motivosRuta = detectarRiesgos(p.directorio, ruta)
		}
		if esConfiguracionGlobal(ruta) {
			motivosRuta = append(motivosRuta, fmt.Sprintf("configuración global cambiada: %s", ruta))
		}
		if !cubierto {
			motivosRuta = append(motivosRuta, fmt.Sprintf("archivo no pertenece a ningún paquete cargado: %s", ruta))
		} else {
			alcance.paquetes = append(alcance.paquetes, paquete)
			alcance.explicacion = append(alcance.explicacion, fmt.Sprintf("%s pertenece a %s", ruta, paquete))
			if p.tests[paquete] {
				alcance.tests = append(alcance.tests, "./"+filepath.ToSlash(filepath.Dir(ruta)))
			}
		}
		if len(motivosRuta) > 0 {
			razones = append(razones, motivosRuta...)
			noCubiertos = append(noCubiertos, ruta)
		}
	}
	if len(razones) > 0 && len(noCubiertos) == 0 {
		noCubiertos = append(noCubiertos, rutas...)
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
	p.imports, p.archivos, p.tests = map[string][]string{}, map[string]string{}, map[string]bool{}
	for _, paquete := range paquetes {
		for _, fallo := range paquete.Errors {
			*razones = append(*razones, fmt.Sprintf("error de carga de %s: %s", paquete.PkgPath, fallo.Msg))
		}
		if paquete.ForTest == "" {
			for importado := range paquete.Imports {
				p.imports[paquete.PkgPath] = append(p.imports[paquete.PkgPath], importado)
			}
		}
		for _, archivo := range append(append(paquete.GoFiles, paquete.OtherFiles...), paquete.EmbedFiles...) {
			relativa, err := filepath.Rel(p.directorio, archivo)
			if err == nil && relativa != ".." && !strings.HasPrefix(relativa, ".."+string(filepath.Separator)) {
				ruta := filepath.ToSlash(relativa)
				p.archivos[ruta] = paquete.PkgPath
				p.tests[paquete.PkgPath] = p.tests[paquete.PkgPath] || strings.HasSuffix(ruta, "_test.go")
			}
		}
	}
	for paquete := range p.imports {
		importados := p.imports[paquete]
		ordenarUnicos(&importados)
		p.imports[paquete] = importados
	}
}

func detectarRiesgos(directorio, ruta string) []string {
	if filepath.Ext(ruta) != ".go" {
		return nil
	}
	contenido, err := os.ReadFile(filepath.Join(directorio, filepath.FromSlash(ruta)))
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
		strings.HasPrefix(base, "build.")
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
