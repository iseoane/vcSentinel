package ops

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Evento es una línea de events.jsonl: el registro append-only de operaciones
// de VAS Sentinel en el repositorio.
type Evento struct {
	At       time.Time `json:"at"`
	Cmd      string    `json:"cmd"`
	Exit     int       `json:"exit"`
	Shas     []string  `json:"shas,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
}

var eventosRel = filepath.Join("vas-sentinel", "events.jsonl")

// Umbrales de rotación: el log nunca crece sin límite. Cuando el archivo
// supera maxEventosBytes se poda a las últimas maxEventosLineas líneas.
const (
	maxEventosBytes  = 256 * 1024
	maxEventosLineas = 1000
)

// RegistrarEvento anexa un evento al log del repositorio (O_APPEND, sin
// truncar). Crea el directorio y el archivo si no existen. Una escritura
// fallida devuelve error: el log nunca se descarta en silencio. Tras anexar,
// si el log supera el umbral de tamaño se rota dejando las últimas
// maxEventosLineas líneas (nunca vacía el historial entero).
func RegistrarEvento(gitDir, cmd string, exit int, shas []string, detail, worktree string) error {
	ruta := filepath.Join(gitDir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		return err
	}
	archivo, err := os.OpenFile(ruta, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	linea, err := json.Marshal(Evento{
		At:       time.Now().UTC(),
		Cmd:      cmd,
		Exit:     exit,
		Shas:     shas,
		Detail:   detail,
		Worktree: worktree,
	})
	if err != nil {
		return err
	}
	if _, err := archivo.Write(append(linea, '\n')); err != nil {
		return err
	}
	if err := archivo.Close(); err != nil {
		return err
	}

	info, err := os.Stat(ruta)
	if err != nil {
		return nil // sin stats no se rota, pero el evento ya quedó escrito
	}
	if info.Size() > maxEventosBytes {
		return RotarEventos(gitDir, maxEventosLineas)
	}
	return nil
}

// DetallePrCreate es el esquema del detail del evento pr-create (§13): la
// acta de publicación. Sin pr_url la acta es por fallback y no es verificable.
type DetallePrCreate struct {
	PrURL    string `json:"pr_url"`
	Fallback bool   `json:"fallback"`
	ChainPR  bool   `json:"chain_pr"`
}

// PurgaResultado resume la purga de actas de PRs resueltas.
type PurgaResultado struct {
	Purgadas    int
	Conservadas int
	// Avisos documenta lo que no se pudo verificar (sin gh/red, actas sin URL):
	// la purga es best-effort y nunca destruye por incertidumbre.
	Avisos []string
}

// PurgeEventosDePRsResueltas consulta el estado de las PRs de los eventos
// pr-create (gh pr view <nº> --json state) y purga las actas de PRs
// MERGED/CLOSED. Conserva OPEN/DRAFT, las actas por fallback (sin URL) y todo
// lo que no se pueda verificar. consultarEstado es inyectable para tests; nil
// usa gh real.
func PurgeEventosDePRsResueltas(gitDir string, consultarEstado func(numero int) (string, error)) (PurgaResultado, error) {
	res := PurgaResultado{}
	ruta := filepath.Join(gitDir, eventosRel)
	archivo, err := os.Open(ruta)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return res, nil
		}
		return res, err
	}

	if consultarEstado == nil {
		consultarEstado = estadoPRConGH
	}

	var conservadas [][]byte
	scanner := bufio.NewScanner(archivo)
	for scanner.Scan() {
		bytes := scanner.Bytes()
		var ev Evento
		if json.Unmarshal(bytes, &ev) != nil || ev.Cmd != "pr-create" {
			conservadas = append(conservadas, append([]byte(nil), bytes...))
			continue
		}
		purgar, aviso := actaResuelta(ev.Detail, consultarEstado)
		if purgar {
			res.Purgadas++
			continue
		}
		res.Conservadas++
		if aviso != "" {
			res.Avisos = append(res.Avisos, aviso)
		}
		conservadas = append(conservadas, append([]byte(nil), bytes...))
	}
	errCierre := archivo.Close()
	if err := scanner.Err(); err != nil {
		return res, err
	}
	if errCierre != nil {
		return res, errCierre
	}

	if res.Purgadas == 0 {
		return res, nil
	}
	return res, escribirLogTemporal(ruta, conservadas)
}

// actaResuelta decide si la acta de una PR es una PR resuelta (purgar),
// devolviendo también un aviso si no se pudo verificar (se conserva). Un
// detail corrupto NO aborta la purga: se conserva con aviso (best-effort,
// nunca se destruye por incertidumbre).
func actaResuelta(detail string, consultarEstado func(int) (string, error)) (bool, string) {
	var acta DetallePrCreate
	if err := json.Unmarshal([]byte(detail), &acta); err != nil {
		return false, "acta con detail inválido: se conserva"
	}
	numero, ok := numeroDePR(acta.PrURL)
	if !ok {
		return false, "acta sin pr_url (fallback): no verificable, se conserva"
	}
	estado, err := consultarEstado(numero)
	if err != nil {
		return false, "PR #" + strconv.Itoa(numero) + " no verificable (gh/red): se conserva"
	}
	switch estado {
	case "MERGED", "CLOSED":
		return true, ""
	default:
		return false, ""
	}
}

// numeroDePR extrae el número de la URL de una PR (/pull/<nº>, tolerando
// sufijos como /pull/42/files).
func numeroDePR(url string) (int, bool) {
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return 0, false
	}
	resto := url[idx+len("/pull/"):]
	fin := 0
	for fin < len(resto) && resto[fin] >= '0' && resto[fin] <= '9' {
		fin++
	}
	if fin == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(resto[:fin])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// estadoPRConGH consulta gh pr view <nº> --json state y devuelve el estado.
// Cualquier fallo (gh ausente, sin red) es un error: la purga lo trata como
// best-effort y conserva la acta.
func estadoPRConGH(numero int) (string, error) {
	salida, err := exec.Command("gh", "pr", "view", strconv.Itoa(numero), "--json", "state").Output()
	if err != nil {
		return "", err
	}
	var crudo struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(salida, &crudo); err != nil {
		return "", err
	}
	return crudo.State, nil
}

// escribirLogTemporal escribe las líneas en un archivo temporal y lo
// reemplaza de forma atómica y segura sobre el log: NUNCA borra el original
// antes de tener el nuevo escrito. En Windows un os.Rename sobre un destino
// existente falla, por lo que el reemplazo es backup → rename → limpieza:
// ante cualquier fallo el log original se conserva (como .bak o intacto).
func escribirLogTemporal(ruta string, lineas [][]byte) error {
	return escribirLogTemporalRenombrando(ruta, lineas, os.Rename)
}

// escribirLogTemporalRenombrando es la variante testeable de
// escribirLogTemporal: la operación de rename es inyectable para poder
// ejercitar las rutas de fallo y restauración sin depender del filesystem.
func escribirLogTemporalRenombrando(ruta string, lineas [][]byte, renombrar func(string, string) error) error {
	temp, err := os.CreateTemp(filepath.Dir(ruta), "events-*.tmp")
	if err != nil {
		return err
	}
	rutaTemp := temp.Name()
	defer os.Remove(rutaTemp)
	escribir := bufio.NewWriter(temp)
	for _, linea := range lineas {
		if _, err := escribir.Write(append(linea, '\n')); err != nil {
			temp.Close()
			return err
		}
	}
	if err := escribir.Flush(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	rutaBak := ruta + ".bak"
	// Limpiar un .bak residual de una ejecución interrumpida: en Windows
	// os.Rename falla si el destino ya existe y bloquearía la siguiente
	// rotación/purga. El .bak residual nunca tiene datos más nuevos que el
	// propio log, así que eliminarlo no pierde información.
	if err := os.Remove(rutaBak); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := renombrar(ruta, rutaBak); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// No había log previo: el temporal pasa a ser el log.
		return renombrar(rutaTemp, ruta)
	}
	if err := renombrar(rutaTemp, ruta); err != nil {
		// Restaurar el original; si la restauración también falla, el log
		// queda a salvo como .bak y se informa dónde recuperarlo.
		if errRest := renombrar(rutaBak, ruta); errRest != nil {
			return fmt.Errorf("%v (restauración fallida: %v; log de respaldo en %s)", err, errRest, rutaBak)
		}
		return err
	}
	// Limpieza del .bak best-effort: el log ya está correctamente escrito;
	// un fallo aquí (lock de antivirus, permisos) no debe reportarse como
	// fallo de la operación.
	_ = os.Remove(rutaBak)
	return nil
}

// PurgeEventosDe reescribe el log eliminando las líneas que referencian
// alguno de los SHAs dados (eventos de auditorías de commits que ya no
// existen). Conserva intactas las líneas de commits que siguen vivos: la
// limpieza es selectiva por SHA, nunca por antigüedad. Devuelve cuántas
// líneas eliminó. Escritura atómica temp + rename: ante fallo, el log
// original se conserva.
func PurgeEventosDe(gitDir string, shas []string) (int, error) {
	if len(shas) == 0 {
		return 0, nil
	}
	ruta := filepath.Join(gitDir, eventosRel)
	archivo, err := os.Open(ruta)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}

	objetivo := map[string]bool{}
	for _, sha := range shas {
		objetivo[sha] = true
	}

	var conservadas [][]byte
	eliminadas := 0
	scanner := bufio.NewScanner(archivo)
	for scanner.Scan() {
		var ev Evento
		bytes := scanner.Bytes()
		if json.Unmarshal(bytes, &ev) == nil && eventosTocanShas(ev.Shas, objetivo) {
			eliminadas++
			continue
		}
		linea := make([]byte, len(bytes))
		copy(linea, bytes)
		conservadas = append(conservadas, linea)
	}
	errCierre := archivo.Close()
	if err := scanner.Err(); err != nil {
		return eliminadas, err
	}
	if errCierre != nil {
		return eliminadas, errCierre
	}

	if eliminadas == 0 {
		return 0, nil
	}
	return eliminadas, escribirLogTemporal(ruta, conservadas)
}

// eventosTocanShas indica si la lista de SHAs del evento contiene alguno de
// los SHAs objetivo.
func eventosTocanShas(evento []string, objetivo map[string]bool) bool {
	for _, sha := range evento {
		if objetivo[sha] {
			return true
		}
	}
	return false
}

// RotarEventos poda el log dejando las últimas n líneas (las más recientes).
// Lo hace con escritura atómica temp + rename: si algo falla, el log original
// se conserva intacto. Un n <= 0 vacía el archivo.
func RotarEventos(gitDir string, n int) error {
	ruta := filepath.Join(gitDir, eventosRel)
	archivo, err := os.Open(ruta)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var lineas [][]byte
	scanner := bufio.NewScanner(archivo)
	for scanner.Scan() {
		linea := make([]byte, len(scanner.Bytes()))
		copy(linea, scanner.Bytes())
		lineas = append(lineas, linea)
	}
	errCierre := archivo.Close()
	if err := scanner.Err(); err != nil {
		return err
	}
	if errCierre != nil {
		return errCierre
	}

	if n > 0 && len(lineas) > n {
		lineas = lineas[len(lineas)-n:]
	} else if n <= 0 {
		lineas = nil
	}
	return escribirLogTemporal(ruta, lineas)
}

// UltimosEventos devuelve los n eventos más recientes del log (el más nuevo
// primero). Si el log no existe devuelve una lista vacía sin error.
func UltimosEventos(gitDir string, n int) ([]Evento, error) {
	ruta := filepath.Join(gitDir, eventosRel)
	archivo, err := os.Open(ruta)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Evento{}, nil
		}
		return nil, err
	}
	defer archivo.Close()

	var eventos []Evento
	scanner := bufio.NewScanner(archivo)
	for scanner.Scan() {
		var ev Evento
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // línea corrupta: se ignora, el resto del log sigue válido
		}
		eventos = append(eventos, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Últimos n, más reciente primero.
	if n <= 0 || len(eventos) <= n {
		for i, j := 0, len(eventos)-1; i < j; i, j = i+1, j-1 {
			eventos[i], eventos[j] = eventos[j], eventos[i]
		}
		return eventos, nil
	}
	ultimos := eventos[len(eventos)-n:]
	for i, j := 0, n-1; i < j; i, j = i+1, j-1 {
		ultimos[i], ultimos[j] = ultimos[j], ultimos[i]
	}
	return ultimos, nil
}
