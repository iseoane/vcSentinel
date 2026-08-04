package git

import (
	"errors"
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

// SHAsHasta devuelve los SHAs de todos los commits alcanzables desde la
// expresión, del más antiguo al más reciente. Útil para --all: auditar todo
// el historial pendiente.
func SHAsHasta(expresion string) ([]string, error) {
	salida, err := ejecutarGitSalida("rev-list", "--reverse", expresion)
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

// RamaActual devuelve el nombre de la rama actual (corta).
func RamaActual() (string, error) {
	salida, err := ejecutarGitSalida("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(salida), nil
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

// ResolverSHA devuelve el SHA completo de una expresión (HEAD, HEAD~2, un sha
// abreviado...).
func ResolverSHA(expresion string) (string, error) {
	salida, err := ejecutarGitSalida("rev-parse", "--verify", expresion+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(salida), nil
}

// ExisteCommit indica si el SHA existe como commit en el almacén de objetos.
func ExisteCommit(sha string) bool {
	_, err := ejecutarGitSalida("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// UpstreamOMain devuelve el ref base para auditar cadenas de commits: el
// upstream si existe; si no, la rama main local; si no, master.
func UpstreamOMain() (string, error) {
	for _, ref := range []string{"@{u}", "main", "master"} {
		salida, err := ejecutarGitSalida("rev-parse", "--verify", "--quiet", ref)
		if err == nil && strings.TrimSpace(salida) != "" {
			return ref, nil
		}
	}
	return "", errors.New("no se encontró upstream ni rama main/master para la cadena")
}

// RemotoDeRama devuelve el remoto configurado para una rama (branch.<rama>.remote)
// o vacío si la rama no tiene remoto.
func RemotoDeRama(rama string) string {
	salida, err := ejecutarGitSalida("config", "--get", "branch."+rama+".remote")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(salida)
}
