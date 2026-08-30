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
	archivo, err := os.OpenFile(ruta, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer archivo.Close()
	info, err := archivo.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 0 {
		lastByte := []byte{0}
		if _, err := archivo.ReadAt(lastByte, info.Size()-1); err != nil {
			return err
		}
		if lastByte[0] != '\n' {
			if _, err := archivo.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	if _, err := archivo.Write(append(linea, '\n')); err != nil {
		return err
	}
	if err := archivo.Close(); err != nil {
		return err
	}

	info, err = os.Stat(ruta)
	if err != nil {
		return nil // sin stats no se rota, pero el evento ya quedó escrito
	}
	if info.Size() > maxEventosBytes {
		return RotarEventos(gitDir, maxEventosLineas)
	}
	return nil
}

// visitRawLines visits each JSONL record with its original line terminator (if
// any). Unlike bufio.Scanner, bufio.Reader does not impose a 64 KiB token
// limit, and retaining the bytes here lets selective rewrites preserve every
// kept record verbatim.
func visitRawLines(reader io.Reader, visit func([]byte) error) error {
	buffered := bufio.NewReader(reader)
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			if visitErr := visit(line); visitErr != nil {
				return visitErr
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

func copyRawLine(line []byte) []byte {
	return append([]byte(nil), line...)
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
	readErr := visitRawLines(archivo, func(line []byte) error {
		var ev Evento
		if json.Unmarshal(line, &ev) != nil || ev.Cmd != "pr-create" {
			conservadas = append(conservadas, copyRawLine(line))
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
		conservadas = append(conservadas, copyRawLine(line))
		return nil
	})
	errCierre := archivo.Close()
	if readErr != nil {
		return res, readErr
	}
	if errCierre != nil {
		return res, errCierre
	}

	if res.Purgadas == 0 {
		return res, nil
	}
	return res, writeRawTemporaryLog(ruta, conservadas)
}

// actaResuelta decide si la acta de una PR es una PR resuelta (purgar),
// devolviendo también un aviso si no se pudo verificar (se conserva). Un
// detail corrupto NO aborta la purga: se conserva con aviso (best-effort,
// nunca se destruye por incertidumbre).
func actaResuelta(detail any, consultarEstado func(int) (string, error)) (bool, string) {
	var acta DetallePrCreate
	if err := unmarshalDetail(detail, &acta); err != nil {
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

func unmarshalDetail(detail any, target any) error {
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

// Writes the lines (without terminators) to a temporary file and atomically
// and safely replaces the log. It NEVER removes the original before the
// replacement is written. On Windows, os.Rename over an existing destination
// fails, so replacement is backup → rename → cleanup: on any failure, the
// original log remains preserved (as .bak or intact).
func escribirLogTemporal(ruta string, lineas [][]byte) error {
	return writeTemporaryLogWithTerminators(ruta, lineas, os.Rename, true)
}

// writeRawTemporaryLog rewrites only after selecting lines, preserving the
// retained bytes exactly, including CRLF and a missing final terminator.
func writeRawTemporaryLog(path string, lines [][]byte) error {
	return writeTemporaryLogWithTerminators(path, lines, os.Rename, false)
}

// escribirLogTemporalRenombrando es la variante testeable de
// escribirLogTemporal: la operación de rename es inyectable para poder
// ejercitar las rutas de fallo y restauración sin depender del filesystem.
func escribirLogTemporalRenombrando(ruta string, lineas [][]byte, renombrar func(string, string) error) error {
	return writeTemporaryLogWithTerminators(ruta, lineas, renombrar, true)
}

func writeTemporaryLogWithTerminators(path string, lines [][]byte, rename func(string, string) error, terminateWithNewline bool) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "events-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	writer := bufio.NewWriter(temp)
	for _, line := range lines {
		data := line
		if terminateWithNewline {
			data = make([]byte, len(line)+1)
			copy(data, line)
			data[len(line)] = '\n'
		}
		if _, err := writer.Write(data); err != nil {
			temp.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}

	backupPath := path + ".bak"
	// Remove a stale backup from an interrupted execution. On Windows,
	// os.Rename fails if the destination exists and would block the next
	// rotation or purge. A stale backup never has newer data than the log, so
	// removing it loses no information.
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rename(path, backupPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// No prior log exists: the temporary file becomes the log.
		return rename(tempPath, path)
	}
	if err := rename(tempPath, path); err != nil {
		// Restore the original. If restoration also fails, the log remains safe
		// as .bak and the error reports where it can be recovered.
		if restoreErr := rename(backupPath, path); restoreErr != nil {
			return fmt.Errorf("%v (restore failed: %v; backup log at %s)", err, restoreErr, backupPath)
		}
		return err
	}
	// Best-effort backup cleanup: the log was written successfully, and a
	// failure here (antivirus lock or permissions) must not fail the operation.
	_ = os.Remove(backupPath)
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
	readErr := visitRawLines(archivo, func(line []byte) error {
		var ev Evento
		if json.Unmarshal(line, &ev) == nil && eventosTocanShas(ev.Shas, objetivo) {
			eliminadas++
			return nil
		}
		conservadas = append(conservadas, copyRawLine(line))
		return nil
	})
	errCierre := archivo.Close()
	if readErr != nil {
		return eliminadas, readErr
	}
	if errCierre != nil {
		return eliminadas, errCierre
	}

	if eliminadas == 0 {
		return 0, nil
	}
	return eliminadas, writeRawTemporaryLog(ruta, conservadas)
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
	readErr := visitRawLines(archivo, func(line []byte) error {
		var ev Evento
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil // línea corrupta: se descarta en la rotación
		}
		lineas = append(lineas, copyRawLine(line))
		return nil
	})
	errCierre := archivo.Close()
	if readErr != nil {
		return readErr
	}
	if errCierre != nil {
		return errCierre
	}

	if n > 0 && len(lineas) > n {
		lineas = lineas[len(lineas)-n:]
	} else if n <= 0 {
		lineas = nil
	}
	return writeRawTemporaryLog(ruta, lineas)
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
	readErr := visitRawLines(archivo, func(line []byte) error {
		var ev Evento
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil // línea corrupta: se ignora, el resto del log sigue válido
		}
		eventos = append(eventos, ev)
		return nil
	})
	if readErr != nil {
		return nil, readErr
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
