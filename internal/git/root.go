package git

import (
	"path/filepath"
	"runtime"
	"strings"
)

// ObtenerRaizWorktree devuelve la ruta absoluta normalizada de la raíz del
// worktree Git activo (el directorio desde el que se ejecuta el proceso).
// Devuelve error si el directorio actual no pertenece a un repositorio Git
// (o es un repositorio bare), porque no existe una raíz de trabajo que
// reportar.
func ObtenerRaizWorktree() (string, error) {
	salida, err := ejecutarGitSalida("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(salida)), nil
}

// EsMismaRuta compara dos rutas tolerando diferencias de separador y de
// mayúsculas. En Windows la comparación ignora mayúsculas; en el resto de
// sistemas es sensible a mayúsculas.
func EsMismaRuta(a string, b string) bool {
	limpiaA := filepath.Clean(a)
	limpiaB := filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(limpiaA, limpiaB)
	}
	return limpiaA == limpiaB
}
