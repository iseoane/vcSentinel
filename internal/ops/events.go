package ops

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

	temp, err := os.CreateTemp(filepath.Dir(ruta), "events-*.tmp")
	if err != nil {
		return eliminadas, err
	}
	rutaTemp := temp.Name()
	defer os.Remove(rutaTemp)
	escribir := bufio.NewWriter(temp)
	for _, linea := range conservadas {
		escribir.Write(append(linea, '\n'))
	}
	if err := escribir.Flush(); err != nil {
		temp.Close()
		return eliminadas, err
	}
	if err := temp.Close(); err != nil {
		return eliminadas, err
	}
	if err := os.Remove(ruta); err != nil && !errors.Is(err, os.ErrNotExist) {
		return eliminadas, err
	}
	if err := os.Rename(rutaTemp, ruta); err != nil {
		return eliminadas, err
	}
	return eliminadas, nil
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

	temp, err := os.CreateTemp(filepath.Dir(ruta), "events-*.tmp")
	if err != nil {
		return err
	}
	rutaTemp := temp.Name()
	defer os.Remove(rutaTemp)
	escribir := bufio.NewWriter(temp)
	for _, linea := range lineas {
		escribir.Write(append(linea, '\n'))
	}
	if err := escribir.Flush(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Remove(ruta); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(rutaTemp, ruta)
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
