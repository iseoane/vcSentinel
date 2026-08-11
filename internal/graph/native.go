package graph

import (
	"bytes"
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
type faseVerificacion uint8

const (
	verificarIdentidad faseVerificacion = iota
	verificarConsumidos
)

type solicitudVerificacion struct {
	directorio, identidad string
	fase                  faseVerificacion
	consumidos            []string
}

type verificadorSnapshot func(solicitudVerificacion) error

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
		solicitud := solicitudVerificacion{directorio: p.directorio, identidad: p.identidad, fase: verificarIdentidad}
		if err := p.verificar(solicitud); err != nil {
			razones = append(razones, "snapshot no verificable: "+err.Error())
		}
	}
	config := &packages.Config{
		Mode:  packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedEmbedFiles | packages.NeedForTest | packages.NeedModule,
		Dir:   p.directorio,
		Env:   append(entornoGitSaneado(), "GOWORK=off"),
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
	if p.directorio != "" {
		solicitud := solicitudVerificacion{
			directorio: p.directorio, identidad: p.identidad,
			fase: verificarConsumidos, consumidos: archivosConsumidos(paquetes),
		}
		if err := p.verificar(solicitud); err != nil {
			razones = append(razones, "snapshot no verificable al completar: "+err.Error())
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
				*razones = append(*razones, "archivo de paquete fuera del snapshot")
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

func archivosConsumidos(paquetes []*packages.Package) []string {
	var archivos []string
	for _, paquete := range paquetes {
		if strings.HasSuffix(paquete.PkgPath, ".test") {
			continue
		}
		archivos = append(archivos, paquete.GoFiles...)
		archivos = append(archivos, paquete.CompiledGoFiles...)
		archivos = append(archivos, paquete.OtherFiles...)
		archivos = append(archivos, paquete.EmbedFiles...)
		if paquete.Module != nil {
			archivos = append(archivos, paquete.Module.GoMod)
			if paquete.Module.Replace != nil {
				archivos = append(archivos, paquete.Module.Replace.GoMod)
			}
		}
	}
	ordenarUnicos(&archivos)
	return archivos
}

func verificarSnapshotGit(solicitud solicitudVerificacion) error {
	directorio, esperado := solicitud.directorio, solicitud.identidad
	gitDir, err := gitSnapshot(directorio, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("repositorio Git no verificable")
	}
	gitDir = strings.TrimSpace(gitDir)
	arbol, err := gitEnRepositorio(directorio, gitDir, nil, "rev-parse", "HEAD^{tree}")
	if err != nil || strings.TrimSpace(arbol) != esperado {
		return fmt.Errorf("tree OID distinto del esperado")
	}
	noRastreados, err := gitEnRepositorio(directorio, gitDir, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil || noRastreados != "" {
		return fmt.Errorf("directorio sucio o con archivos no rastreados")
	}
	salida, err := gitEnRepositorio(directorio, gitDir, nil, "ls-tree", "-rz", "--full-tree", esperado)
	if err != nil {
		return fmt.Errorf("árbol esperado no legible")
	}
	blobs := map[string]string{}
	for _, entrada := range strings.Split(salida, "\x00") {
		cabecera, ruta, ok := strings.Cut(entrada, "\t")
		campos := strings.Fields(cabecera)
		if ok && len(campos) == 3 && campos[1] == "blob" {
			blobs[ruta] = campos[2]
		}
	}
	consumidos := solicitud.consumidos
	if solicitud.fase == verificarConsumidos {
		for _, ruta := range []string{"go.mod", "go.sum", filepath.Join("vendor", "modules.txt")} {
			if _, ok := blobs[filepath.ToSlash(ruta)]; ok {
				consumidos = append(consumidos, filepath.Join(directorio, ruta))
			} else if _, err := os.Stat(filepath.Join(directorio, ruta)); err == nil {
				consumidos = append(consumidos, filepath.Join(directorio, ruta))
			}
		}
	}
	ordenarUnicos(&consumidos)
	for _, archivo := range consumidos {
		if archivo == "" {
			continue
		}
		resuelto, err := filepath.EvalSymlinks(archivo)
		if err != nil || !dentroDe(directorio, resuelto) {
			return fmt.Errorf("blob consumido fuera del snapshot")
		}
		relativa, _ := filepath.Rel(directorio, resuelto)
		ruta := filepath.ToSlash(relativa)
		esperadoBlob, ok := blobs[ruta]
		if !ok {
			return fmt.Errorf("blob consumido no rastreado: %s", ruta)
		}
		contenido, err := os.ReadFile(resuelto)
		if err != nil {
			return fmt.Errorf("blob consumido no legible: %s", ruta)
		}
		actual, err := gitEnRepositorio(directorio, gitDir, contenido, "hash-object", "--stdin")
		if err != nil || strings.TrimSpace(actual) != esperadoBlob {
			return fmt.Errorf("blob consumido distinto del árbol: %s", ruta)
		}
	}
	return nil
}

func gitSnapshot(directorio string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "core.attributesFile=" + os.DevNull, "-c", "diff.external=", "-C", directorio}, args...)...)
	cmd.Env = entornoGitSaneado()
	salida, err := cmd.CombinedOutput()
	return string(salida), err
}

func gitEnRepositorio(directorio, gitDir string, entrada []byte, args ...string) (string, error) {
	base := []string{"-c", "core.attributesFile=" + os.DevNull, "-c", "diff.external=", "--git-dir", gitDir, "--work-tree", directorio}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Env = entornoGitSaneado()
	cmd.Stdin = bytes.NewReader(entrada)
	salida, err := cmd.CombinedOutput()
	return string(salida), err
}

func entornoGitSaneado() []string {
	entorno := []string{"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0"}
	for _, variable := range os.Environ() {
		nombre, _, _ := strings.Cut(variable, "=")
		if !strings.HasPrefix(nombre, "GIT_") && nombre != "LANG" && nombre != "LC_ALL" && !strings.HasPrefix(nombre, "LC_") && nombre != "GOWORK" {
			entorno = append(entorno, variable)
		}
	}
	return entorno
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
