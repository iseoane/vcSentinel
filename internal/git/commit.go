package git

import (
	"strings"
)

// MensajeCommit devuelve la primera línea del mensaje de un commit.
func MensajeCommit(sha string) (string, error) {
	salida, err := ejecutarGitSalida("log", "-1", "--format=%s", sha)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(salida), nil
}

// DiffCommit devuelve el diff completo de un commit (sin el mensaje), con
// archivos nuevos, modificados y renombrados. Funciona también para el primer
// commit del repositorio (root commit).
func DiffCommit(sha string) (string, error) {
	salida, err := ejecutarGitSalida("show", "--format=", "--no-color", sha, "--")
	if err != nil {
		return "", err
	}
	return strings.TrimRight(salida, "\n"), nil
}

// SHAHead devuelve el SHA completo del commit HEAD.
func SHAHead() (string, error) {
	salida, err := ejecutarGitSalida("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(salida), nil
}

// SHAsRango devuelve los SHAs desde "desde" (exclusivo) hasta "hasta"
// (inclusivo), en orden cronológico. Para el primer commit de una rama se usa
// un SHA vacío como "desde".
func SHAsRango(desde, hasta string) ([]string, error) {
	revision := desde + ".." + hasta
	if desde == "" {
		revision = hasta
	}
	salida, err := ejecutarGitSalida("rev-list", "--reverse", revision)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, linea := range strings.Split(salida, "\n") {
		if sha := strings.TrimSpace(linea); sha != "" {
			shas = append(shas, sha)
		}
	}
	return shas, nil
}

// ArchivosDeCommit devuelve las rutas de los archivos que toca un commit,
// útiles para deducir el saco (capa) de la auditoría.
func ArchivosDeCommit(sha string) ([]string, error) {
	salida, err := ejecutarGitSalida("show", "--name-only", "--format=", sha, "--")
	if err != nil {
		return nil, err
	}
	var archivos []string
	for _, linea := range strings.Split(salida, "\n") {
		if ruta := strings.TrimSpace(linea); ruta != "" {
			archivos = append(archivos, ruta)
		}
	}
	return archivos, nil
}
