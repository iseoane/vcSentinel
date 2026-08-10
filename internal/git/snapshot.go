package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ArbolDe devuelve el tree OID de una revisión.
func ArbolDe(revision string) (string, error) {
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
			_, _ = ejecutarGitSalida("worktree", "remove", "--force", ruta)
		}
		huboEliminacion = true
	}

	if huboEliminacion {
		_, _ = ejecutarGitSalida("worktree", "prune")
	}
	return nil
}
