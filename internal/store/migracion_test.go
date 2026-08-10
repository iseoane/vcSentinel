package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// gitDirPrivado devuelve el git-dir privado y absoluto de dir
// (`--absolute-git-dir`), distinto del common-dir para un worktree enlazado.
// Se usa exec directo (no internal/git.ObtenerGitDir, que no acepta ruta) tal
// como ya hace TestStoreAncladoEnGitCommonDirCompartidoEntreWorktrees.
func gitDirPrivado(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--absolute-git-dir")
	salida, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --absolute-git-dir en %s: %v", dir, err)
	}
	return filepath.Clean(strings.TrimSpace(string(salida)))
}

// TestMigrarDesdeV1PrincipalYWorktreeEnlazado cubre el caso real: fichas v1
// en el checkout principal (ya en el sitio compartido, solo falta el
// formato) y fichas v1 privadas de un worktree enlazado (ubicación Y
// formato). Tras migrar, ambas deben quedar accesibles desde el store
// compartido, y ningún archivo v1 original se toca.
func TestMigrarDesdeV1PrincipalYWorktreeEnlazado(t *testing.T) {
	principal := t.TempDir()
	ejecutarGit(t, principal, "init", "-q")
	ejecutarGit(t, principal, "config", "user.email", "test@vas.sentinel")
	ejecutarGit(t, principal, "config", "user.name", "vas-sentinel-test")
	if err := os.WriteFile(filepath.Join(principal, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGit(t, principal, "add", "a.txt")
	ejecutarGit(t, principal, "commit", "-q", "-m", "inicial")

	worktree := filepath.Join(t.TempDir(), "worktree")
	ejecutarGit(t, principal, "worktree", "add", "-q", worktree, "-b", "rama-migracion")

	gitCommonDir, err := git.ObtenerGitCommonDir(principal)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir(principal): %v", err)
	}
	gitDirWorktree := gitDirPrivado(t, worktree)

	// Ledger v1 del checkout principal: ya en el sitio compartido.
	ledgerPrincipal := review.NuevoLedger(gitCommonDir)
	revPrincipal := review.Revision{At: time.Now().UTC(), Result: review.VerdictOK}
	if err := ledgerPrincipal.GuardarRevision("shaprincipal1", "feat(x): principal", "backend", "modelo-a", revPrincipal); err != nil {
		t.Fatalf("GuardarRevision principal: %v", err)
	}

	// Ledger v1 privado del worktree enlazado.
	ledgerWorktree := review.NuevoLedger(gitDirWorktree)
	revWorktree := review.Revision{At: time.Now().UTC(), Result: review.VerdictBlock}
	if err := ledgerWorktree.GuardarRevision("shaworktree1", "fix(y): worktree", "frontend", "modelo-b", revWorktree); err != nil {
		t.Fatalf("GuardarRevision worktree: %v", err)
	}

	resumen, err := MigrarDesdeV1(gitCommonDir)
	if err != nil {
		t.Fatalf("MigrarDesdeV1: %v", err)
	}
	if resumen.Migrados != 2 {
		t.Errorf("Migrados = %d, esperado 2", resumen.Migrados)
	}
	if resumen.YaMigrados != 0 {
		t.Errorf("YaMigrados = %d, esperado 0", resumen.YaMigrados)
	}
	if len(resumen.Corruptos) != 0 {
		t.Errorf("Corruptos = %v, esperado ninguno", resumen.Corruptos)
	}

	s := NuevoStore(gitCommonDir)

	idxPrincipal, err := s.LeerIndiceCommit("shaprincipal1")
	if err != nil {
		t.Fatalf("LeerIndiceCommit(shaprincipal1): %v", err)
	}
	if idxPrincipal == nil || idxPrincipal.Message != "feat(x): principal" {
		t.Fatalf("idxPrincipal = %+v, no coincide con la ficha v1", idxPrincipal)
	}
	if idxPrincipal.V1 == nil || idxPrincipal.V1.Bucket != "backend" || idxPrincipal.V1.Model != "modelo-a" || len(idxPrincipal.V1.Revisions) != 1 {
		t.Errorf("idxPrincipal.V1 = %+v, no conserva la ficha v1 completa", idxPrincipal.V1)
	}
	if len(idxPrincipal.Fingerprints) != 0 {
		t.Errorf("un commit migrado no debe traer fingerprints inventados: %v", idxPrincipal.Fingerprints)
	}

	idxWorktree, err := s.LeerIndiceCommit("shaworktree1")
	if err != nil {
		t.Fatalf("LeerIndiceCommit(shaworktree1): %v", err)
	}
	if idxWorktree == nil || idxWorktree.Message != "fix(y): worktree" {
		t.Fatalf("idxWorktree = %+v, no coincide con la ficha v1 del worktree", idxWorktree)
	}
	if idxWorktree.V1 == nil || idxWorktree.V1.Bucket != "frontend" || idxWorktree.V1.Model != "modelo-b" {
		t.Errorf("idxWorktree.V1 = %+v, no conserva la ficha v1 del worktree", idxWorktree.V1)
	}

	// Nada se borra de los archivos v1 originales.
	if _, err := os.Stat(ledgerPrincipal.RutaFicha("shaprincipal1")); err != nil {
		t.Errorf("la ficha v1 principal ya no existe en disco: %v", err)
	}
	if _, err := os.Stat(ledgerWorktree.RutaFicha("shaworktree1")); err != nil {
		t.Errorf("la ficha v1 del worktree ya no existe en disco: %v", err)
	}
}

// TestMigrarDesdeV1EsIdempotente confirma que ejecutar la migración dos veces
// no duplica nada: la segunda pasada reporta "ya migrado" para todo lo que la
// primera ya procesó.
func TestMigrarDesdeV1EsIdempotente(t *testing.T) {
	dir := t.TempDir()
	ledger := review.NuevoLedger(dir)
	rev := review.Revision{At: time.Now().UTC(), Result: review.VerdictOK}
	if err := ledger.GuardarRevision("shaidem1", "msg", "backend", "m", rev); err != nil {
		t.Fatalf("GuardarRevision: %v", err)
	}

	primera, err := MigrarDesdeV1(dir)
	if err != nil {
		t.Fatalf("primera migración: %v", err)
	}
	if primera.Migrados != 1 || primera.YaMigrados != 0 {
		t.Fatalf("primera = %+v, esperado 1 migrado y 0 ya migrados", primera)
	}

	segunda, err := MigrarDesdeV1(dir)
	if err != nil {
		t.Fatalf("segunda migración: %v", err)
	}
	if segunda.Migrados != 0 || segunda.YaMigrados != 1 {
		t.Errorf("segunda = %+v, esperado 0 migrados y 1 ya migrado (idempotencia)", segunda)
	}
}

// TestMigrarDesdeV1ArchivoCorruptoNoAborta confirma que un <sha>.json v1
// inválido se reporta en el resumen sin abortar la migración de los demás
// archivos válidos, y que el archivo corrupto sigue intacto en disco.
func TestMigrarDesdeV1ArchivoCorruptoNoAborta(t *testing.T) {
	dir := t.TempDir()
	ledger := review.NuevoLedger(dir)
	rev := review.Revision{At: time.Now().UTC(), Result: review.VerdictOK}
	if err := ledger.GuardarRevision("shavalido1", "msg valido", "backend", "m", rev); err != nil {
		t.Fatalf("GuardarRevision: %v", err)
	}

	rutaCorrupta := ledger.RutaFicha("shacorrupto1")
	contenidoCorrupto := []byte("esto no es json")
	if err := os.WriteFile(rutaCorrupta, contenidoCorrupto, 0644); err != nil {
		t.Fatal(err)
	}

	resumen, err := MigrarDesdeV1(dir)
	if err != nil {
		t.Fatalf("MigrarDesdeV1 no debería abortar por un archivo corrupto: %v", err)
	}
	if resumen.Migrados != 1 {
		t.Errorf("Migrados = %d, esperado 1 (el válido, a pesar del corrupto)", resumen.Migrados)
	}
	if len(resumen.Corruptos) != 1 || resumen.Corruptos[0].Ruta != rutaCorrupta {
		t.Fatalf("Corruptos = %+v, esperado 1 con ruta %q", resumen.Corruptos, rutaCorrupta)
	}

	s := NuevoStore(dir)
	if idx, _ := s.LeerIndiceCommit("shavalido1"); idx == nil {
		t.Error("el commit válido debería haberse migrado a pesar del corrupto")
	}

	datos, err := os.ReadFile(rutaCorrupta)
	if err != nil {
		t.Fatalf("el archivo corrupto ya no existe: %v", err)
	}
	if string(datos) != string(contenidoCorrupto) {
		t.Error("el archivo corrupto no debería modificarse durante la migración")
	}
}
