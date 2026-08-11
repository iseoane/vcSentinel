package consent

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConsentimientoDiffExternoEsLocalYRevocable(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Fatal(err)
	}
	if estado, err := EstadoDiffExterno(repo); err != nil || estado.Otorgado {
		t.Fatalf("estado inicial = %+v, err = %v", estado, err)
	}

	estado, err := OtorgarDiffExterno(repo)
	if err != nil || !estado.Otorgado || estado.Repositorio == "" || estado.Usuario == "" || estado.OtorgadoEn.IsZero() {
		t.Fatalf("grant no auditable = %+v, err = %v", estado, err)
	}
	commonDir, _ := filepath.Abs(filepath.Join(repo, ".git"))
	if filepath.Dir(filepath.Dir(estado.Ruta)) != filepath.Join(commonDir, "vas-sentinel") {
		t.Fatalf("grant fuera del common-dir: %s", estado.Ruta)
	}
	info, err := os.Stat(estado.Ruta)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("permisos del grant = %v", info.Mode())
	}

	if err := RevocarDiffExterno(repo); err != nil {
		t.Fatal(err)
	}
	if estado, err := EstadoDiffExterno(repo); err != nil || estado.Otorgado {
		t.Fatalf("estado revocado = %+v, err = %v", estado, err)
	}
}
