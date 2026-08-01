package git

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
)

const (
	limiteLineasLote    = 400
	limiteConfigGigante = 400
	limiteCodigoGigante = 500

	mensajeAisladoDeps   = "chore(deps): track lock and auto-generated files"
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

	var resultado []ArchivoModificado
	for _, linea := range strings.Split(salida, "\n") {
		linea = strings.TrimSpace(linea)
		if linea == "" {
			continue
		}
		campos := strings.Fields(linea)
		if len(campos) < 3 {
			continue
		}
		addCount, err := strconv.Atoi(campos[0])
		if err != nil {
			continue // Ignora binarios marcados con "-"
		}
		ruta := campos[2]
		resultado = append(resultado, ArchivoModificado{Ruta: ruta, Lineas: addCount, Capa: clasificarCapa(ruta)})
	}
	return resultado, nil
}

// archivosNoRastreados detecta los archivos nuevos (??) y cuenta sus líneas físicas.
func archivosNoRastreados() ([]ArchivoModificado, error) {
	salida, err := ejecutarGitSalida("status", "--short", "-uall")
	if err != nil {
		return nil, err
	}

	var resultado []ArchivoModificado
	for _, linea := range strings.Split(salida, "\n") {
		linea = strings.TrimSpace(linea)
		if !strings.HasPrefix(linea, "??") {
			continue
		}
		campos := strings.Fields(linea)
		if len(campos) < 2 {
			continue
		}
		ruta := campos[1]
		lineas, err := contarLineasFisicas(ruta)
		if err != nil {
			return nil, fmt.Errorf("no se pudo contar las líneas de %s: %w", ruta, err)
		}
		resultado = append(resultado, ArchivoModificado{Ruta: ruta, Lineas: lineas, Capa: clasificarCapa(ruta)})
	}
	return resultado, nil
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

// clasificarCapa determina la capa de un archivo según su ruta y extensión.
func clasificarCapa(ruta string) string {
	ext := filepath.Ext(ruta)
	base := filepath.Base(ruta)
	rutaLower := strings.ToLower(ruta)

	if strings.Contains(rutaLower, "test") || strings.Contains(base, "spec") {
		return "test"
	} else if strings.Contains(rutaLower, "frontend") || ext == ".tsx" || ext == ".jsx" || ext == ".css" || ext == ".scss" {
		return "frontend"
	} else if ext == ".json" || ext == ".yaml" || ext == ".toml" || ext == ".lock" || ext == ".sum" || base == "requirements.txt" {
		return "config"
	}
	return "backend"
}

// esConfigGigante indica si un archivo de configuración supera el límite de aislamiento.
func esConfigGigante(f ArchivoModificado) bool {
	return f.Capa == "config" && f.Lineas > limiteConfigGigante
}

// esCodigoGigante indica si un archivo de código fuente supera el límite de bypass interactivo.
func esCodigoGigante(f ArchivoModificado) bool {
	return f.Capa != "config" && f.Lineas > limiteCodigoGigante
}

// ConfirmarBypass pregunta al usuario si quiere fragmentar un archivo de código
// que supera el límite de volumen. Devuelve true solo si responde afirmativamente.
func ConfirmarBypass(f ArchivoModificado) (bool, error) {
	fmt.Printf("⚠️ ¡Alerta! El archivo %s tiene %d líneas y supera el límite de %d. ¿Quieres fragmentarlo igual? (s/N): ", f.Ruta, f.Lineas, limiteCodigoGigante)
	var respuesta string
	if _, err := fmt.Scanln(&respuesta); err != nil {
		return false, err
	}
	respuesta = strings.ToLower(strings.TrimSpace(respuesta))
	switch respuesta {
	case "s", "si", "sí", "y", "yes":
		return true, nil
	default:
		return false, nil
	}
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

// diffPendienteRutas devuelve el diff de los archivos dados frente a HEAD,
// combinando los cambios staged y unstaged sin necesidad de prepararlos.
func diffPendienteRutas(rutas []string) (string, error) {
	args := append([]string{"diff", "HEAD", "--"}, rutas...)
	return ejecutarGitSalida(args...)
}
