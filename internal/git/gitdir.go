package git

import (
	"bytes"
	"os/exec"
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

// ObtenerGitCommonDir devuelve la ruta absoluta del common-dir de Git del
// repositorio en path (`git rev-parse --git-common-dir`): el mismo directorio
// para todos los worktrees enlazados del repositorio, a diferencia de
// ObtenerGitDir. Se usa para instalar artefactos compartidos por todo el
// repositorio (como los hooks), que Git solo lee del common-dir y no del
// directorio privado de cada worktree enlazado.
func ObtenerGitCommonDir(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--git-common-dir")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out.String())
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(path, dir)
	}
	return filepath.Clean(dir), nil
}
