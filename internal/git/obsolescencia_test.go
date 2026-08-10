package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

func TestSigueVigenteTrueSinCambios(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	candidato, err := Congelar()
	if err != nil {
		t.Fatalf("Congelar devolvió error: %v", err)
	}

	vigente, err := SigueVigente(candidato)
	if err != nil {
		t.Fatalf("SigueVigente devolvió error: %v", err)
	}
	if !vigente {
		t.Errorf("SigueVigente = false sin ningún cambio; se esperaba true")
	}
}

func TestSigueVigenteFalseTrasModificarWorktree(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	candidato, err := Congelar()
	if err != nil {
		t.Fatalf("Congelar devolvió error: %v", err)
	}

	// Modificación sin commitear: el worktree queda distinto del que se
	// congeló, aunque HEAD no cambie.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar x = 1\n"), 0644); err != nil {
		t.Fatalf("no se pudo modificar a.go: %v", err)
	}

	vigente, err := SigueVigente(candidato)
	if err != nil {
		t.Fatalf("SigueVigente devolvió error: %v", err)
	}
	if vigente {
		t.Errorf("SigueVigente = true tras modificar el worktree; se esperaba false")
	}
}

func TestSigueVigenteFalseTrasNuevoCommit(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	candidato, err := Congelar()
	if err != nil {
		t.Fatalf("Congelar devolvió error: %v", err)
	}

	// Se commitea el mismo contenido: el árbol resultante coincide con el
	// que ya se había congelado, pero HEAD cambió a un commit distinto. El
	// candidato sigue siendo obsoleto porque referenciaba el HEAD anterior.
	ejecutarGit(t, dir, "commit", "--allow-empty", "-q", "-m", "commit vacío")

	vigente, err := SigueVigente(candidato)
	if err != nil {
		t.Fatalf("SigueVigente devolvió error: %v", err)
	}
	if vigente {
		t.Errorf("SigueVigente = true tras un nuevo commit; se esperaba false aunque el árbol coincida")
	}
}

func TestExigirWorktreeLimpioEnInplaceConWorktreeLimpio(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if err := ExigirWorktreeLimpioEnInplace(config.ModeInplace); err != nil {
		t.Errorf("ExigirWorktreeLimpioEnInplace devolvió error con worktree limpio: %v", err)
	}
}

func TestExigirWorktreeLimpioEnInplaceConWorktreeSucio(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar x = 1\n"), 0644); err != nil {
		t.Fatalf("no se pudo modificar a.go: %v", err)
	}

	if err := ExigirWorktreeLimpioEnInplace(config.ModeInplace); err == nil {
		t.Errorf("ExigirWorktreeLimpioEnInplace no devolvió error con worktree sucio; se esperaba un error explícito")
	}
}

func TestExigirWorktreeLimpioEnInplaceIgnoraOtrosModos(t *testing.T) {
	requiereGitReal(t)

	dir := prepararRepositorioPrueba(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nvar x = 1\n"), 0644); err != nil {
		t.Fatalf("no se pudo modificar a.go: %v", err)
	}

	if err := ExigirWorktreeLimpioEnInplace("worktree"); err != nil {
		t.Errorf("ExigirWorktreeLimpioEnInplace debería no aplicar fuera de modo inplace, devolvió: %v", err)
	}
}
