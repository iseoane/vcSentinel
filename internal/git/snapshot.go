package git

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// snapshotMu serializes in-process snapshot creation and purging. Ticket 14
// diagnosis: the flaky "llamada N devolvió una ruta distinta" failures were a
// production race, not a test defect. Concurrent CrearSnapshot calls each ran
// their own "git worktree add" into a private temp path and the winner then
// ran "git worktree repair" while sibling calls were still mutating the same
// <git-common-dir>/worktrees administrative area; git does not lock that area
// across commands, so repair intermittently failed with exit status 128 and
// one caller returned an error (empty path) where every caller must receive
// the same deterministic destination. Serializing the whole create-or-reuse
// section removes the intra-process race deterministically without loosening
// any assertion; cross-process safety is unchanged and still relies on the
// atomic temp-checkout + rename publication below.
var snapshotMu sync.Mutex

// ArbolDe devuelve el tree OID de una revisión.
func ArbolDe(revision string) (string, error) {
	// Una revisión que empieza con "-" se interpretaría como una opción de
	// "git rev-parse" en vez de como el nombre de la revisión (B14): quien
	// llame con una entrada no confiable podría inyectar opciones de git.
	if strings.HasPrefix(revision, "-") {
		return "", fmt.Errorf("revisión inválida %q: no puede empezar con \"-\"", revision)
	}
	salida, err := ejecutarGitSalida("rev-parse", revision+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(salida), nil
}

// directorioSnapshots devuelve <git-common-dir>/vas-sentinel/snapshots del
// repositorio activo (el mismo para todos los worktrees enlazados, a
// diferencia del git-dir privado de cada uno), creándolo si no existe.
func directorioSnapshots() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	commonDir, err := ObtenerGitCommonDir(cwd)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(commonDir, "vas-sentinel", "snapshots")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// CrearSnapshot crea (o reutiliza) un worktree Git separado en modo detached
// para el árbol treeOID, dentro del common-dir del repositorio, de forma que
// cualquier worktree enlazado del mismo repositorio vea y reutilice el mismo
// snapshot. Si el snapshot ya existe, se devuelve sin repetir el checkout:
// es éxito, no error.
//
// Desviación de diseño verificada empíricamente: "git worktree add" exige un
// commit-ish y rechaza un tree OID desnudo ("object ... is a tree, not a
// commit"). Por eso primero se ancla el árbol en un commit colgante (sin
// rama, sin ref que lo referencie) con "git commit-tree": ese commit no
// aporta historia, solo sirve de punto de entrada válido para el checkout.
func CrearSnapshot(treeOID string) (string, error) {
	// See snapshotMu: concurrent creation inside one process raced the git
	// worktree administrative area and made repair fail intermittently.
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	snapshots, err := directorioSnapshots()
	if err != nil {
		return "", err
	}
	destino := filepath.Join(snapshots, treeOID)
	if info, err := os.Stat(destino); err == nil && info.IsDir() {
		return destino, nil
	}

	commitAncla, err := ejecutarGitSalida("commit-tree", treeOID, "-m", "vas-sentinel: snapshot")
	if err != nil {
		return "", fmt.Errorf("no se pudo anclar el árbol %s en un commit para el snapshot: %w", treeOID, err)
	}
	commitAncla = strings.TrimSpace(commitAncla)

	// Se hace el checkout en un directorio temporal de nombre único (PID +
	// timestamp) y luego se renombra al nombre final: si dos llamadas
	// concurrentes crean el mismo snapshot, cada una hace su propio checkout
	// aislado sin necesidad de locks explícitos, y solo una gana el
	// renombrado atómico; la otra descarta su worktree temporal sin error.
	temporal := filepath.Join(snapshots, fmt.Sprintf(".%s.tmp-%d-%d", treeOID, os.Getpid(), time.Now().UnixNano()))
	if _, err := ejecutarGitSalida("worktree", "add", "--detach", temporal, commitAncla); err != nil {
		return "", fmt.Errorf("no se pudo crear el worktree del snapshot %s: %w", treeOID, err)
	}

	if err := os.Rename(temporal, destino); err != nil {
		if info, statErr := os.Stat(destino); statErr == nil && info.IsDir() {
			// Otra llamada ganó la carrera: el destino ya existe. Se limpia
			// el worktree temporal propio (en su ruta original, todavía
			// consistente) y se reutiliza el resultado ajeno.
			_, _ = ejecutarGitSalida("worktree", "remove", "--force", temporal)
			return destino, nil
		}
		_, _ = ejecutarGitSalida("worktree", "remove", "--force", temporal)
		return "", fmt.Errorf("no se pudo publicar el snapshot %s: %w", treeOID, err)
	}

	// El renombrado deja obsoleta la referencia inversa que Git guarda en su
	// directorio administrativo (apunta a la ruta temporal, que ya no
	// existe): sin este "repair", "git worktree list"/"remove" no reconocen
	// la ruta final. Verificado empíricamente con git 2.47.3.
	if _, err := ejecutarGitSalida("worktree", "repair", destino); err != nil {
		return "", fmt.Errorf("no se pudo reparar los metadatos del worktree del snapshot %s: %w", treeOID, err)
	}

	return destino, nil
}

// PurgarSnapshots elimina los snapshots de <git-common-dir>/vas-sentinel/snapshots
// cuya fecha de modificación sea más antigua que antiguedad. Son desechables
// (se regeneran con CrearSnapshot), así que se fuerza la eliminación si falla
// la normal.
func PurgarSnapshots(antiguedad time.Duration) error {
	// The purge removes worktrees from the same administrative area the
	// create path mutates, so it shares snapshotMu: a purge running while
	// CrearSnapshot renames or repairs could otherwise make either git
	// command fail on transient administrative state.
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	snapshots, err := directorioSnapshots()
	if err != nil {
		return err
	}
	entradas, err := os.ReadDir(snapshots)
	if err != nil {
		return err
	}

	limite := time.Now().Add(-antiguedad)
	huboEliminacion := false
	var errores []error
	for _, entrada := range entradas {
		if !entrada.IsDir() {
			continue
		}
		info, err := entrada.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(limite) {
			continue
		}
		ruta := filepath.Join(snapshots, entrada.Name())
		if _, err := ejecutarGitSalida("worktree", "remove", ruta); err != nil {
			if _, errForzado := ejecutarGitSalida("worktree", "remove", "--force", ruta); errForzado != nil {
				// Ni la vía normal ni --force pudieron limpiar este
				// snapshot (B15): se acumula el error en vez de devolver
				// nil, que haría creer al llamador que el disco quedó
				// limpio cuando el directorio roto sigue ahí. El resto del
				// bucle continúa: es limpieza best-effort por snapshot, un
				// fallo aislado no debe impedir purgar los demás.
				errores = append(errores, fmt.Errorf("no se pudo eliminar el snapshot %s: %w", ruta, errForzado))
				continue
			}
		}
		huboEliminacion = true
	}

	if huboEliminacion {
		_, _ = ejecutarGitSalida("worktree", "prune")
	}
	return errors.Join(errores...)
}
