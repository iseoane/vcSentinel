package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// prefijosEstables fija los prefijos de cabecera del diff. No es cosmético: el
// planner de revisión parsea estas salidas para extraer las líneas añadidas y
// reconoce la ruta por su prefijo "b/". diff.noprefix, diff.mnemonicPrefix y
// diff.srcPrefix/dstPrefix cambian ese formato, y una cabecera que el parser no
// reconoce pierde sus líneas sin error, dejando ciegos a los detectores que
// leen contenido. Forzarlos aquí impide que la configuración del usuario
// reintroduzca la divergencia que FU-10 registra.
var prefijosEstables = []string{"--src-prefix=a/", "--dst-prefix=b/"}

// DiffCommit devuelve el diff completo de un commit (sin el mensaje), con
// archivos nuevos, modificados y renombrados. Funciona también para el primer
// commit del repositorio (root commit).
func DiffCommit(sha string) (string, error) {
	args := append([]string{"show", "--format=", "--no-color"}, prefijosEstables...)
	salida, err := ejecutarGitSalida(append(args, sha, "--")...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(salida, "\n"), nil
}

// DiffRango devuelve el diff de base..head con los mismos prefijos estables que
// DiffCommit, para los llamadores que razonan sobre un rango en vez de sobre un
// commit.
func DiffRango(base, head string) (string, error) {
	args := append([]string{"diff", "--no-color"}, prefijosEstables...)
	salida, err := ejecutarGitSalida(append(args, base+".."+head, "--")...)
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

// ContenidoDeArchivoEnCommit devuelve el contenido exacto de un archivo tal
// como existía en un commit concreto ("git show <sha>:<archivo>"). Si el
// archivo no existe en ese commit (renombrado, borrado, ruta mal escrita por
// el agente que audita), devuelve un error explícito en vez de una cadena
// vacía silenciosa: el llamador necesita distinguir "archivo vacío" de
// "archivo no resuelto" para decidir si un hallazgo es válido.
func ContenidoDeArchivoEnCommit(sha, archivo string) (string, error) {
	// filepath.ToSlash: git siempre espera "/" en un pathspec <rev>:<ruta>,
	// aunque el archivo llegue con separadores de Windows (regla
	// multiplataforma del proyecto: nunca concatenar rutas a git sin
	// normalizar primero).
	salida, err := ejecutarGitSalida("show", sha+":"+filepath.ToSlash(archivo))
	if err != nil {
		return "", fmt.Errorf("no se pudo leer %q en el commit %q: %w", archivo, sha, err)
	}
	return salida, nil
}

// BlobDeArchivoEnCommit devuelve el hash de blob (objeto git) del contenido
// de un archivo tal como existía en un commit concreto, vía
// "git rev-parse <sha>:<archivo>" (esa forma ya resuelve directamente al
// blob, sin necesitar el sufijo "^{blob}"). Es la clave que sobrevive a un
// rebase: el SHA del commit cambia, pero el blob de un archivo cuyo
// contenido no cambió es idéntico bajo cualquier SHA que lo contenga. Si el
// archivo no existe en ese commit, error explícito (mismo criterio que
// ContenidoDeArchivoEnCommit).
func BlobDeArchivoEnCommit(sha, archivo string) (string, error) {
	// Mismo motivo que ContenidoDeArchivoEnCommit: el pathspec <rev>:<ruta>
	// de git siempre usa "/", sea cual sea el separador con el que llegó
	// archivo.
	salida, err := ejecutarGitSalida("rev-parse", sha+":"+filepath.ToSlash(archivo))
	if err != nil {
		return "", fmt.Errorf("no se pudo resolver el blob de %q en el commit %q: %w", archivo, sha, err)
	}
	return strings.TrimSpace(salida), nil
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
// Nota: un commit reescrito por rebase/amend/squash sigue existiendo como
// objeto dangling; para saber si sigue vivo en la historia usa
// ContenidoEnAlgunRef.
func ExisteCommit(sha string) bool {
	_, err := ejecutarGitSalida("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// ContenidoEnAlgunRef indica si el SHA es alcanzable desde algún ref local o
// remoto (git branch -a --contains). Un commit reescrito por rebase, amend o
// squash deja de estar contenido en ningún ref y aquí devuelve false: es la
// semántica correcta para detectar fichas huérfanas del ledger.
func ContenidoEnAlgunRef(sha string) bool {
	salida, err := ejecutarGitSalida("branch", "-a", "--contains", sha)
	if err != nil {
		return false
	}
	return strings.TrimSpace(salida) != ""
}

// ContenidoEnAlgunRefDe responde lo mismo pero contra el repositorio de
// worktree en vez de contra el directorio de trabajo del proceso.
//
// La diferencia importa donde la respuesta decide un borrado. Un llamador que
// purgue los ledgers de varios checkouts a la vez y clasifique con el CWD
// borraría fichas vivas en cuanto el proceso corriera desde otro repositorio,
// porque un SHA legítimo de este repo no aparece en los refs de aquel.
func ContenidoEnAlgunRefDe(worktree, sha string) bool {
	salida, err := gitEn(worktree, "branch", "-a", "--contains", sha)
	if err != nil {
		// Un objeto desconocido hace fallar a `branch --contains`, y ése es
		// precisamente el caso huérfano. Que un fallo signifique "no está" solo
		// es admisible porque el llamador validó antes el repositorio con
		// RepositorioUsable: sin esa comprobación, cualquier invocación rota
		// borraría fichas vivas.
		return false
	}
	return strings.TrimSpace(salida) != ""
}

// RepositorioUsable confirma que worktree resuelve a un repositorio Git. Un
// borrado guiado por ContenidoEnAlgunRefDe debe llamarla una vez antes de
// clasificar nada: es lo que separa "este commit ya no está" de "no he podido
// preguntar".
func RepositorioUsable(worktree string) error {
	if _, err := gitEn(worktree, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("%s is not a usable git repository: %w", worktree, err)
	}
	return nil
}

// gitEn ejecuta git contra worktree con el entorno despojado de las variables
// que seleccionan repositorio.
//
// No es precaución teórica: GIT_DIR TIENE PRIORIDAD SOBRE "-C". Con GIT_DIR
// apuntando a otro sitio, `git -C <ruta> rev-parse --git-dir` responde por el
// repositorio de GIT_DIR, no por el de la ruta. Sentinel corre dentro de su
// propio hook de pre-commit, que es exactamente un contexto donde Git exporta
// esas variables, así que una consulta que decide borrados no puede confiar en
// "-C" sin limpiarlas.
func gitEn(worktree string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
	cmd.Env = entornoSinSeleccionDeRepositorio()
	salida, err := cmd.Output()
	return string(salida), err
}

// entornoSinSeleccionDeRepositorio quita SOLO lo que elige repositorio o
// restringe qué refs se ven. Deliberadamente NO toca GIT_OBJECT_DIRECTORY ni
// GIT_ALTERNATE_OBJECT_DIRECTORIES: ésas dicen dónde están los objetos, no cuál
// es el repositorio, y quitarlas rompería un repositorio cuyos objetos viven
// donde el entorno indica. `branch --contains` fallaría para commits
// perfectamente alcanzables y el fallo se leería como "huérfano".
//
// La comparación ignora mayúsculas porque en Windows los nombres de variable no
// distinguen caso: un `git_dir` en minúsculas sobreviviría a un filtro
// sensible al caso y volvería a anular "-C".
func entornoSinSeleccionDeRepositorio() []string {
	seleccionan := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_NAMESPACE"}
	entorno := os.Environ()
	limpio := make([]string, 0, len(entorno))
	for _, variable := range entorno {
		nombre, _, _ := strings.Cut(variable, "=")
		descartar := false
		for _, seleccion := range seleccionan {
			if strings.EqualFold(nombre, seleccion) {
				descartar = true
				break
			}
		}
		if !descartar {
			limpio = append(limpio, variable)
		}
	}
	return limpio
}

// GitEnAislado ejecuta git contra worktree con el mismo entorno saneado que usa
// la sonda de alcance. Existe para que todo lo que participa en una decisión de
// borrado mire el MISMO repositorio: sanear solo una de las dos consultas
// mezcla identidades, y clasificar los ledgers de un repositorio contra los
// refs de otro borra fichas vivas.
func GitEnAislado(worktree string, args ...string) (string, error) {
	return gitEn(worktree, args...)
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

// Attributes devuelve el contenido de .gitattributes en revision, o cadena
// vacía si el árbol no lo tiene.
//
// Ausencia y fallo se distinguen con ls-tree: `git show <rev>:.gitattributes`
// y `git cat-file -e` salen con código no cero tanto si la ruta no está como
// si el repositorio o el objeto no se pueden leer, así que cualquiera de los
// dos convertiría un fallo real en "no hay atributos". Una ruta ausente es
// salida vacía con código cero.
func Attributes(revision string) (string, error) {
	listado, err := ejecutarGitSalida("ls-tree", "--name-only", revision, "--", ".gitattributes")
	if err != nil {
		return "", fmt.Errorf("looking for .gitattributes at %s: %w", revision, err)
	}
	if strings.TrimSpace(listado) == "" {
		return "", nil
	}
	contenido, err := ejecutarGitSalida("show", revision+":.gitattributes")
	if err != nil {
		return "", fmt.Errorf("reading .gitattributes at %s: %w", revision, err)
	}
	return contenido, nil
}
