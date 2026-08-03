package git

import (
	"path/filepath"
	"strings"
)

// ObtenerGitDir devuelve la ruta absoluta del common-dir de Git del
// repositorio activo (`git rev-parse --absolute-git-dir`). En worktrees
// enlazados devuelve el directorio privado del worktree
// (`.git/worktrees/<nombre>`), lo que aísla el estado de VAS Sentinel por
// worktree de forma gratuita. Nunca se asume la ruta literal `.git`: en
// Windows y en worktrees enlazados el directorio puede estar representado por
// un archivo gitfile.
func ObtenerGitDir() (string, error) {
	salida, err := ejecutarGitSalida("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(salida)), nil
}
