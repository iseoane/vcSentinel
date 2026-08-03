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

// RegistrarEvento anexa un evento al log del repositorio (O_APPEND, sin
// truncar). Crea el directorio y el archivo si no existen. Una escritura
// fallida devuelve error: el log nunca se descarta en silencio.
func RegistrarEvento(gitDir, cmd string, exit int, shas []string, detail, worktree string) error {
	ruta := filepath.Join(gitDir, eventosRel)
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		return err
	}
	archivo, err := os.OpenFile(ruta, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer archivo.Close()

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
	return nil
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
