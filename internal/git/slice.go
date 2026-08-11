package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
)

const (
	// Ambos derivan de LimiteLineasRevisables (umbrales.go): un lote y un
	// archivo de configuración aislado se miden con la misma vara que el
	// guardián, y recalibrar el guardián debe arrastrarlos.
	limiteLineasLote    = LimiteLineasRevisables
	limiteConfigGigante = LimiteLineasRevisables
	// El micro-diff se envía a un proceso externo: limita tanto cada archivo
	// nuevo como el resultado total antes de materializar su contenido completo.
	limiteBytesMicroDiff = 1 << 20

	mensajeAisladoDeps   = "chore(deps): track lock and auto-generated files"
	mensajeAisladoDocs   = "docs(slice): isolate extensive document %s"
	mensajeBypassGigante = "chore(slice): bypass IA for massive file %s"
)

var ordenCapas = []string{"config", "backend", "frontend", "test"}

// ArchivoModificado representa un archivo con cambios pendientes de fragmentar.
type ArchivoModificado struct {
	Ruta   string
	Lineas int
	Capa   string
}

// ObtenerArchivosModificados devuelve los archivos con cambios respecto a HEAD,
// incluyendo los no rastreados (untracked), con sus líneas añadidas y su capa.
func ObtenerArchivosModificados() ([]ArchivoModificado, error) {
	rastreados, err := archivosRastreados()
	if err != nil {
		return nil, err
	}
	noRastreados, err := archivosNoRastreados()
	if err != nil {
		return nil, err
	}
	return append(rastreados, noRastreados...), nil
}

// archivosRastreados lee los cambios rastreados (modificados y staged) con numstat.
func archivosRastreados() ([]ArchivoModificado, error) {
	salida, err := ejecutarGitSalida("diff", "HEAD", "--numstat")
	if err != nil {
		return nil, err
	}

	return parsearNumstat(salida), nil
}

// parsearNumstat interpreta la salida de "git diff --numstat", que separa
// añadidas/borradas/ruta con tabuladores. Se corta por los dos primeros
// tabuladores (SplitN) en vez de por espacios, así una ruta con espacios se
// conserva íntegra en el tercer campo. Para los renombrados, ese campo no es
// una ruta utilizable tal cual: rutaDestino lo reduce a la ruta de destino,
// la única que existe en el worktree y que "git add" acepta.
func parsearNumstat(salida string) []ArchivoModificado {
	var resultado []ArchivoModificado
	for _, linea := range strings.Split(salida, "\n") {
		linea = strings.TrimRight(linea, "\r")
		if linea == "" {
			continue
		}
		campos := strings.SplitN(linea, "\t", 3)
		if len(campos) < 3 {
			continue
		}
		addCount, err := strconv.Atoi(campos[0])
		if err != nil {
			continue // Ignora binarios marcados con "-"
		}
		ruta := rutaDestino(campos[2])
		resultado = append(resultado, ArchivoModificado{Ruta: ruta, Lineas: addCount, Capa: ClasificarCapa(ruta)})
	}
	return resultado
}

// rutaDestino reduce el tercer campo del numstat a la ruta de destino cuando
// describe un renombrado. Git emite los renombrados en dos formas:
//
//   - plana: "viejo archivo.go => nuevo archivo.go" — la cadena completa no
//     es una ruta, solo la mitad derecha lo es.
//   - abreviada con llaves: "dir/{viejo => sub1/nuevo}.go" — solo cambia el
//     tramo entre llaves; hay que sustituirlo por su mitad derecha y
//     conservar el prefijo y el sufijo comunes.
//
// Si el campo no describe un renombrado, se devuelve tal cual.
func rutaDestino(campo string) string {
	if apertura := strings.Index(campo, "{"); apertura != -1 {
		if cierreRelativo := strings.Index(campo[apertura:], "}"); cierreRelativo != -1 {
			cierre := apertura + cierreRelativo
			contenido := campo[apertura+1 : cierre]
			if partes := strings.SplitN(contenido, " => ", 2); len(partes) == 2 {
				return campo[:apertura] + partes[1] + campo[cierre+1:]
			}
		}
	}
	if partes := strings.SplitN(campo, " => ", 2); len(partes) == 2 {
		return partes[1]
	}
	return campo
}

// archivosNoRastreados detecta los archivos nuevos (??) y cuenta sus líneas
// físicas. Usa "--porcelain -z" en vez de "--short": con "--short", git
// entrecomilla cualquier ruta con espacios o caracteres especiales y escapa
// los no ASCII (p. ej. una tilde) con secuencias octales, lo que rompía tanto
// el recuento (archivo inexistente) como el "git add" posterior de slice.
// "-z" separa los registros con NUL y emite las rutas en crudo, sin comillas
// ni escapes: elimina la clase entera de errores en vez de parchear un caso.
func archivosNoRastreados() ([]ArchivoModificado, error) {
	salida, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall")
	if err != nil {
		return nil, err
	}

	var resultado []ArchivoModificado
	for _, ruta := range rutasNoRastreadas(salida) {
		lineas, err := contarLineasFisicas(ruta)
		if err != nil {
			return nil, fmt.Errorf("no se pudo contar las líneas de %s: %w", ruta, err)
		}
		resultado = append(resultado, ArchivoModificado{Ruta: ruta, Lineas: lineas, Capa: ClasificarCapa(ruta)})
	}
	return resultado, nil
}

// rutasNoRastreadas extrae las rutas de los archivos no rastreados ("??") de
// la salida de "git status --porcelain -z -uall". Cada registro va separado
// por NUL; los no rastreados tienen un único registro con el prefijo "?? ".
// Los renombrados de archivos rastreados generan un registro adicional sin
// ese prefijo (la ruta antigua), que se ignora igual que cualquier otro
// estado que no sea "??".
func rutasNoRastreadas(salida string) []string {
	var rutas []string
	for _, registro := range strings.Split(salida, "\x00") {
		ruta, esNoRastreado := strings.CutPrefix(registro, "?? ")
		if !esNoRastreado {
			continue
		}
		rutas = append(rutas, ruta)
	}
	return rutas
}

// ejecutarGitSalida ejecuta git y devuelve la salida estándar completa.
func ejecutarGitSalida(args ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command("git", args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// contarLineasFisicas cuenta las líneas de un archivo sin depender de git,
// tolerante a líneas muy largas y a archivos sin salto de línea final.
func contarLineasFisicas(ruta string) (int, error) {
	archivo, err := os.Open(ruta)
	if err != nil {
		return 0, err
	}
	defer archivo.Close()

	lineas := 0
	hayContenido := false
	terminaEnNuevaLinea := false
	buffer := make([]byte, 32*1024)
	for {
		n, err := archivo.Read(buffer)
		if n > 0 {
			hayContenido = true
			terminaEnNuevaLinea = buffer[n-1] == '\n'
			for i := 0; i < n; i++ {
				if buffer[i] == '\n' {
					lineas++
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if hayContenido && !terminaEnNuevaLinea {
		lineas++
	}
	return lineas, nil
}

// ClasificarCapa determina la capa de un archivo según su ruta y extensión.
//
// Clasifica por subcadena (p. ej. "test" en cualquier parte de la ruta), lo
// que produce falsos positivos documentados en F3 (latest/x.go, contest.go).
// internal/change.ClasificarPorRuta (T3.1) corrige esto con globs explícitos,
// pero coexiste con esta función hasta T3.7: sus tres consumidores (agrupar
// lotes de slice, elegir dimensiones de review, calcular el bucket de la
// ficha) migran en tareas separadas (T3.7, F5-T5.2, F2-T2.6 ya cerrada para
// el tercero), no de un salto. No eliminar esta función ni redirigirla aquí
// sin cerrar esa migración: ver docs/reingenieria/f3-cambio-riesgo.md, T3.1.
func ClasificarCapa(ruta string) string {
	ext := filepath.Ext(ruta)
	base := filepath.Base(ruta)
	rutaLower := strings.ToLower(ruta)

	if strings.Contains(rutaLower, "test") || strings.Contains(base, "spec") {
		return "test"
	} else if strings.Contains(rutaLower, "frontend") || ext == ".tsx" || ext == ".jsx" || ext == ".css" || ext == ".scss" {
		return "frontend"
	} else if ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".toml" || ext == ".lock" || ext == ".sum" || base == "requirements.txt" {
		return "config"
	}
	return "backend"
}

// esConfigGigante indica si un archivo de configuración supera el límite de aislamiento.
func esConfigGigante(f ArchivoModificado) bool {
	return f.Capa == "config" && f.Lineas > limiteConfigGigante
}

// esDocumentacionExtensa indica si un documento supera el límite de
// aislamiento. Se aísla en su propio lote como la configuración, pero NUNCA
// entra por la rama de código masivo: proponer un plan de división SRP sobre
// prosa no tiene ningún sentido.
func esDocumentacionExtensa(f ArchivoModificado) bool {
	return ClaseArchivo(f.Ruta) == ClaseDocs && f.Lineas > limiteConfigGigante
}

// esCodigoGigante indica si un archivo de código fuente supera el límite de
// bypass interactivo. Solo aplica a código: la documentación se aísla con
// esDocumentacionExtensa y lo generado nunca se mezcla con código porque
// ConstruirPlanFragmentacion agrupa primero por clase, así que ninguno de los
// dos necesita ofrecer refactorización.
func esCodigoGigante(f ArchivoModificado) bool {
	switch ClaseArchivo(f.Ruta) {
	case ClaseDocs, ClaseGenerada, ClaseConfig:
		return false
	}
	return f.Capa != "config" && f.Lineas > LimiteCodigoGigante
}

type loteConCapa struct {
	Capa  string
	Rutas []string
}

func construirSecuenciaLotes(porCapas map[string][]ArchivoModificado) []loteConCapa {
	var secuencia []loteConCapa

	for _, capa := range ordenCapas {
		for _, lote := range construirLotes(porCapas[capa]) {
			rutas := make([]string, 0, len(lote))
			for _, f := range lote {
				rutas = append(rutas, f.Ruta)
			}
			secuencia = append(secuencia, loteConCapa{Capa: capa, Rutas: rutas})
		}
	}
	return secuencia
}

func construirLotes(archivos []ArchivoModificado) [][]ArchivoModificado {
	var lotes [][]ArchivoModificado
	var loteActual []ArchivoModificado
	lineasAcumuladas := 0

	for _, f := range archivos {
		if lineasAcumuladas+f.Lineas > limiteLineasLote && len(loteActual) > 0 {
			lotes = append(lotes, loteActual)
			loteActual = nil
			lineasAcumuladas = 0
		}
		loteActual = append(loteActual, f)
		lineasAcumuladas += f.Lineas
	}

	if len(loteActual) > 0 {
		lotes = append(lotes, loteActual)
	}
	return lotes
}

// obtenerMensajeConDiff prefiere el adaptador con capacidad de diff (AdapterConDiff);
// si la extracción del diff pendiente falla o el adaptador no la implementa, usa la
// interfaz base AgentAdapter.
func obtenerMensajeConDiff(rutas []string, capa string, numero int, adapter agentadapter.AgentAdapter) (string, error) {
	if adapterConDiff, ok := adapter.(agentadapter.AdapterConDiff); ok {
		diff, err := diffPendienteRutas(rutas)
		if err == nil {
			return adapterConDiff.ObtenerMensajeCommitConDiff(rutas, capa, numero, diff)
		}
	}
	return adapter.ObtenerMensajeCommit(rutas, capa, numero)
}

// diffPendienteRutas devuelve el diff de los archivos dados frente a HEAD e
// incorpora archivos no rastreados sin preparar ni modificar el índice.
func diffPendienteRutas(rutas []string) (string, error) {
	args := append([]string{"diff", "--no-color", "--no-ext-diff", "--no-textconv", "HEAD", "--"}, rutas...)
	rastreado, err := ejecutarGitSalida(args...)
	if err != nil {
		return "", err
	}
	if len(rastreado) > limiteBytesMicroDiff {
		return "", fmt.Errorf("el diff rastreado supera el límite de %d bytes para el micro-diff", limiteBytesMicroDiff)
	}

	args = append([]string{"ls-files", "--others", "--exclude-standard", "-z", "--"}, rutas...)
	salida, err := ejecutarGitSalida(args...)
	if err != nil {
		return "", err
	}
	noRastreados := strings.Split(strings.TrimSuffix(salida, "\x00"), "\x00")
	if len(noRastreados) == 1 && noRastreados[0] == "" {
		return rastreado, nil
	}
	sort.Strings(noRastreados)

	var diff strings.Builder
	diff.WriteString(rastreado)
	for _, ruta := range noRastreados {
		info, err := os.Lstat(filepath.FromSlash(ruta))
		if err != nil {
			return "", fmt.Errorf("no se pudo leer el archivo no rastreado %s: %w", ruta, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("el archivo no rastreado %s no es un archivo regular", ruta)
		}
		if info.Size() > limiteBytesMicroDiff {
			return "", fmt.Errorf("el archivo no rastreado %s supera el límite de %d bytes para el micro-diff", ruta, limiteBytesMicroDiff)
		}

		numstat, err := ejecutarGitDiffNoIndex("--numstat", os.DevNull, ruta)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(numstat, "-\t-\t") {
			return "", fmt.Errorf("el archivo no rastreado %s es binario; no se enviará su contenido", ruta)
		}
		parche, err := ejecutarGitDiffNoIndex("--patch", os.DevNull, ruta)
		if err != nil {
			return "", err
		}
		if diff.Len()+len(parche) > limiteBytesMicroDiff {
			return "", fmt.Errorf("el micro-diff supera el límite de %d bytes", limiteBytesMicroDiff)
		}
		diff.WriteString(parche)
	}
	return diff.String(), nil
}

func ejecutarGitDiffNoIndex(formato, origen, destino string) (string, error) {
	cmd := exec.Command("git", "diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv", formato, "--", origen, destino)
	salida, err := cmd.Output()
	if err == nil {
		return string(salida), nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return string(salida), nil
	}
	detalle := ""
	if exitErr != nil {
		detalle = strings.TrimSpace(string(exitErr.Stderr))
	}
	return "", fmt.Errorf("git diff --no-index falló para %s: %w: %s", destino, err, detalle)
}
