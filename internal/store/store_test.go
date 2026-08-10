package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// TestStoreEscrituraAtomica reproduce TestLedgerEscrituraAtomica (T2.x) sobre
// el layout nuevo: varias escrituras consecutivas al mismo id nunca dejan el
// archivo destino a medias.
func TestStoreEscrituraAtomica(t *testing.T) {
	s := NuevoStore(t.TempDir())
	for i := 0; i < 20; i++ {
		idx := &IndiceCommit{SHA: "sha1", Message: "m", Fingerprints: []string{"fp"}}
		if err := s.GuardarIndiceCommit(idx); err != nil {
			t.Fatalf("escritura %d: %v", i, err)
		}
	}
	idx, err := s.LeerIndiceCommit("sha1")
	if err != nil {
		t.Fatalf("tras 20 escrituras el archivo quedó corrupto: %v", err)
	}
	if idx == nil || idx.Message != "m" {
		t.Errorf("indice = %+v, no coincide con la última escritura", idx)
	}
}

func ejecutarGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestStoreAncladoEnGitCommonDirCompartidoEntreWorktrees confirma que el
// store, anclado con ObtenerGitCommonDir, resuelve al mismo directorio para
// el checkout principal y para un worktree enlazado — a diferencia de
// ObtenerGitDir, que es privado por worktree. Una escritura desde el store
// del checkout principal debe ser visible desde el store del worktree.
func TestStoreAncladoEnGitCommonDirCompartidoEntreWorktrees(t *testing.T) {
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
	ejecutarGit(t, principal, "worktree", "add", "-q", worktree, "-b", "rama-test")

	commonPrincipal, err := git.ObtenerGitCommonDir(principal)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir(principal): %v", err)
	}
	commonWorktree, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatalf("ObtenerGitCommonDir(worktree): %v", err)
	}
	if commonPrincipal != commonWorktree {
		t.Fatalf("common-dir distinto entre checkout principal y worktree: %s != %s", commonPrincipal, commonWorktree)
	}
	// El git-dir privado del worktree (--absolute-git-dir) es
	// .git/worktrees/<nombre>, distinto del common-dir: confirma que anclar
	// en ObtenerGitCommonDir (y no en ObtenerGitDir) es lo que hace posible
	// compartir el store entre worktrees enlazados.
	cmd := exec.Command("git", "-C", worktree, "rev-parse", "--absolute-git-dir")
	salida, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --absolute-git-dir: %v", err)
	}
	privadoWorktree := filepath.Clean(strings.TrimSpace(string(salida)))
	if privadoWorktree == commonWorktree {
		t.Fatalf("el git-dir privado del worktree no debería coincidir con el common-dir")
	}

	sPrincipal := NuevoStore(commonPrincipal)
	if err := sPrincipal.GuardarIndiceCommit(&IndiceCommit{SHA: "sha1", Fingerprints: []string{"fp1"}}); err != nil {
		t.Fatalf("GuardarIndiceCommit desde principal: %v", err)
	}

	sWorktree := NuevoStore(commonWorktree)
	idx, err := sWorktree.LeerIndiceCommit("sha1")
	if err != nil {
		t.Fatalf("LeerIndiceCommit desde worktree: %v", err)
	}
	if idx == nil || len(idx.Fingerprints) != 1 || idx.Fingerprints[0] != "fp1" {
		t.Errorf("el worktree no ve lo escrito por el checkout principal: %+v", idx)
	}
}
