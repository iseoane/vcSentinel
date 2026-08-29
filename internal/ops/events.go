package ops

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EventDetail is the structured payload used by newly written operation
// events. Legacy callers may still provide a string.
type EventDetail map[string]any

// Evento es una línea de events.jsonl: el registro append-only de operaciones
// de VAS Sentinel en el repositorio.
type Evento struct {
	At       time.Time `json:"at"`
	Cmd      string    `json:"cmd"`
	Exit     int       `json:"exit"`
	Shas     []string  `json:"shas,omitempty"`
	Detail   any       `json:"detail,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
}

// UnmarshalJSON accepts both the historical string representation and the
// structured object representation. A legacy string containing a JSON object
// is normalized for readers; other legacy text remains unchanged.
func (e *Evento) UnmarshalJSON(data []byte) error {
	type eventWire struct {
		At       time.Time       `json:"at"`
		Cmd      string          `json:"cmd"`
		Exit     int             `json:"exit"`
		Shas     []string        `json:"shas,omitempty"`
		Detail   json.RawMessage `json:"detail"`
		Worktree string          `json:"worktree,omitempty"`
	}
	var wire eventWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*e = Evento{At: wire.At, Cmd: wire.Cmd, Exit: wire.Exit, Shas: wire.Shas, Worktree: wire.Worktree}
	rawDetail := bytes.TrimSpace(wire.Detail)
	if len(rawDetail) == 0 || bytes.Equal(rawDetail, []byte("null")) {
		return nil
	}

	var detail any
	if err := json.Unmarshal(rawDetail, &detail); err != nil {
		return err
	}
	if legacy, ok := detail.(string); ok {
		var object map[string]any
		if err := json.Unmarshal([]byte(legacy), &object); err == nil && object != nil {
			detail = object
		}
	}
	e.Detail = detail
	return nil
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
func RegistrarEvento(gitDir, cmd string, exit int, shas []string, detail any, worktree string) error {
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

// recorrerLineasCrudas visits each JSONL record with its original line
// terminator (if any). Unlike bufio.Scanner, bufio.Reader does not impose a
// 64 KiB token limit, and retaining the bytes here lets selective rewrites
// preserve every kept record verbatim.
func recorrerLineasCrudas(archivo io.Reader, visitar func([]byte) error) error {
	lector := bufio.NewReader(archivo)
	for {
		linea, err := lector.ReadBytes('\n')
		if len(linea) > 0 {
			if errVisita := visitar(linea); errVisita != nil {
				return errVisita
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func copiarLineaCruda(linea []byte) []byte {
	return append([]byte(nil), linea...)
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
	errLectura := recorrerLineasCrudas(archivo, func(linea []byte) error {
		var ev Evento
		if json.Unmarshal(linea, &ev) != nil || ev.Cmd != "pr-create" {
			conservadas = append(conservadas, copiarLineaCruda(linea))
			return nil
		}
		purgar, aviso := actaResuelta(ev.Detail, consultarEstado)
		if purgar {
			res.Purgadas++
			return nil
		}
		res.Conservadas++
		if aviso != "" {
			res.Avisos = append(res.Avisos, aviso)
		}
		conservadas = append(conservadas, copiarLineaCruda(linea))
		return nil
	})
	errCierre := archivo.Close()
	if errLectura != nil {
		return res, errLectura
	}
	if errCierre != nil {
		return res, errCierre
	}

	if res.Purgadas == 0 {
		return res, nil
	}
	return res, escribirLogTemporalRaw(ruta, conservadas)
}

// actaResuelta decide si la acta de una PR es una PR resuelta (purgar),
// devolviendo también un aviso si no se pudo verificar (se conserva). Un
// detail corrupto NO aborta la purga: se conserva con aviso (best-effort,
// nunca se destruye por incertidumbre).
func actaResuelta(detail any, consultarEstado func(int) (string, error)) (bool, string) {
	var acta DetallePrCreate
	if err := unmarshalaDetalle(detail, &acta); err != nil {
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

func unmarshalaDetalle(detail any, target any) error {
	if legacy, ok := detail.(string); ok {
		return json.Unmarshal([]byte(legacy), target)
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
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

// escribirLogTemporal escribe las líneas (sin terminadores) en un archivo
// temporal y lo reemplaza de forma atómica y segura sobre el log: NUNCA borra
// el original antes de tener el nuevo escrito. En Windows un os.Rename sobre
// un destino existente falla, por lo que el reemplazo es backup → rename →
// limpieza: ante cualquier fallo el log original se conserva (como .bak o
// intacto).
func escribirLogTemporal(ruta string, lineas [][]byte) error {
	return escribirLogTemporalConTerminadores(ruta, lineas, os.Rename, true)
}

// escribirLogTemporalRaw reescribe solo después de una selección de líneas
// conservando exactamente los bytes que se retuvieron, incluidos CRLF y la
// ausencia de un terminador final.
func escribirLogTemporalRaw(ruta string, lineas [][]byte) error {
	return escribirLogTemporalConTerminadores(ruta, lineas, os.Rename, false)
}

// escribirLogTemporalRenombrando es la variante testeable de
// escribirLogTemporal: la operación de rename es inyectable para poder
// ejercitar las rutas de fallo y restauración sin depender del filesystem.
func escribirLogTemporalRenombrando(ruta string, lineas [][]byte, renombrar func(string, string) error) error {
	return escribirLogTemporalConTerminadores(ruta, lineas, renombrar, true)
}

func escribirLogTemporalConTerminadores(ruta string, lineas [][]byte, renombrar func(string, string) error, terminarConNuevaLinea bool) error {
	temp, err := os.CreateTemp(filepath.Dir(ruta), "events-*.tmp")
	if err != nil {
		return err
	}
	rutaTemp := temp.Name()
	defer os.Remove(rutaTemp)
	escribir := bufio.NewWriter(temp)
	for _, linea := range lineas {
		datos := linea
		if terminarConNuevaLinea {
			datos = make([]byte, len(linea)+1)
			copy(datos, linea)
			datos[len(linea)] = '\n'
		}
		if _, err := escribir.Write(datos); err != nil {
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
	errLectura := recorrerLineasCrudas(archivo, func(linea []byte) error {
		var ev Evento
		if json.Unmarshal(linea, &ev) == nil && eventosTocanShas(ev.Shas, objetivo) {
			eliminadas++
			return nil
		}
		conservadas = append(conservadas, copiarLineaCruda(linea))
		return nil
	})
	errCierre := archivo.Close()
	if errLectura != nil {
		return eliminadas, errLectura
	}
	if errCierre != nil {
		return eliminadas, errCierre
	}

	if eliminadas == 0 {
		return 0, nil
	}
	return eliminadas, escribirLogTemporalRaw(ruta, conservadas)
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

// RotarEventos poda el log dejando las últimas n líneas válidas (las más
// recientes). Descarta las líneas corruptas (JSON inválido): solo los eventos
// que parsean cuentan para la poda y se conservan. Lo hace con escritura
// atómica temp + rename: si algo falla, el log original se conserva intacto.
// Un n <= 0 vacía el archivo.
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
	errLectura := recorrerLineasCrudas(archivo, func(linea []byte) error {
		var ev Evento
		if err := json.Unmarshal(linea, &ev); err != nil {
			return nil // línea corrupta: se descarta en la rotación
		}
		lineas = append(lineas, copiarLineaCruda(linea))
		return nil
	})
	errCierre := archivo.Close()
	if errLectura != nil {
		return errLectura
	}
	if errCierre != nil {
		return errCierre
	}

	if n > 0 && len(lineas) > n {
		lineas = lineas[len(lineas)-n:]
	} else if n <= 0 {
		lineas = nil
	}
	return escribirLogTemporalRaw(ruta, lineas)
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
	errLectura := recorrerLineasCrudas(archivo, func(linea []byte) error {
		var ev Evento
		if err := json.Unmarshal(linea, &ev); err != nil {
			return nil // línea corrupta: se ignora, el resto del log sigue válido
		}
		eventos = append(eventos, ev)
		return nil
	})
	if errLectura != nil {
		return nil, errLectura
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
