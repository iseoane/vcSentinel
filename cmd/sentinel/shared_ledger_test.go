package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// TestSharedReviewLedgerAnchorsOnTheCommonDirectory pins where review evidence
// is written. review.NuevoLedger files fichas under whatever directory it is
// handed, and review, status and pr handed it the checkout's own gitDir. In a
// linked worktree that is <common-dir>/worktrees/<name>, so a review run there
// filed its record inside the administrative directory `git worktree remove`
// deletes. Five review records were lost exactly that way while the code they
// approved stayed on main.
//
// Only a linked worktree can prove this: in the main checkout the gitDir and
// the common directory are the same path, so any anchor looks correct there.
func TestSharedReviewLedgerAnchorsOnTheCommonDirectory(t *testing.T) {
	repo := repoGitTemporal(t)
	enlazado := filepath.Join(t.TempDir(), "linked")
	// repoGitTemporal already skips when git is absent, so a failure here is a
	// real setup failure — permissions, a branch collision, a broken repository
	// — and must fail. Skipping on any error would let this regression check
	// never run and still report success.
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "linked", enlazado).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v\n%s", err, out)
	}

	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	principal, err := sharedReviewLedger(repo)
	if err != nil {
		t.Fatalf("sharedReviewLedger(main checkout) error = %v", err)
	}
	deEnlazado, err := sharedReviewLedger(enlazado)
	if err != nil {
		t.Fatalf("sharedReviewLedger(linked worktree) error = %v", err)
	}
	if deEnlazado.RutaFicha(sha) != principal.RutaFicha(sha) {
		t.Errorf("the linked worktree files its ficha at %s and the main checkout at %s; a review run in a worktree is then invisible to the repository and dies with the worktree",
			deEnlazado.RutaFicha(sha), principal.RutaFicha(sha))
	}

	// Asserted separately rather than inferred from the equality above: two
	// ledgers could agree on a path that is still inside the worktree's own
	// administrative directory.
	gitDirEnlazado, err := git.ObtenerGitDirDe(enlazado)
	if err != nil {
		t.Fatalf("resolving the linked worktree's own gitDir: %v", err)
	}
	porCheckout := review.NuevoLedger(gitDirEnlazado).RutaFicha(sha)
	if deEnlazado.RutaFicha(sha) == porCheckout {
		t.Errorf("the shared ledger resolved the per-checkout path %s, which `git worktree remove` deletes", porCheckout)
	}

	// The expected path is named, not merely distinguished from the wrong one:
	// the two assertions above also pass for a fixed or global path that agrees
	// with itself and differs from this checkout.
	commonDir, err := git.ObtenerGitCommonDir(enlazado)
	if err != nil {
		t.Fatalf("resolving the common directory: %v", err)
	}
	esperada := review.NuevoLedger(commonDir).RutaFicha(sha)
	if deEnlazado.RutaFicha(sha) != esperada {
		t.Errorf("the shared ledger resolved %s, want %s under the Git common directory",
			deEnlazado.RutaFicha(sha), esperada)
	}
}
