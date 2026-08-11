package consent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func repoTemporal(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Fatal(err)
	}
	return repo
}

func usarHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func crearSymlink(t *testing.T, destino, enlace string) {
	t.Helper()
	if err := os.Symlink(destino, enlace); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("la plataforma no permite crear symlinks: %v", err)
		}
		t.Fatal(err)
	}
}

func TestConsentimientoDiffExternoFS(t *testing.T) {
	t.Run("grant idempotente y revocable", func(t *testing.T) {
		repo := repoTemporal(t)
		primero, err := OtorgarDiffExterno(repo)
		if err != nil {
			t.Fatal(err)
		}
		contenido, err := os.ReadFile(primero.Ruta)
		if err != nil {
			t.Fatal(err)
		}
		segundo, err := OtorgarDiffExterno(repo)
		if err != nil || segundo.OtorgadoEn != primero.OtorgadoEn {
			t.Fatalf("segundo grant = %+v, err = %v", segundo, err)
		}
		despues, _ := os.ReadFile(primero.Ruta)
		if string(despues) != string(contenido) {
			t.Fatal("el grant idempotente reemplazo el archivo existente")
		}
		for ruta, modo := range map[string]os.FileMode{filepath.Dir(primero.Ruta): 0700, primero.Ruta: 0600} {
			info, err := os.Stat(ruta)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != modo {
				t.Fatalf("modo de %s = %v", ruta, info.Mode())
			}
		}
		if err := RevocarDiffExterno(repo); err != nil {
			t.Fatal(err)
		}
		if estado, err := EstadoDiffExterno(repo); err != nil || estado.Otorgado {
			t.Fatalf("estado revocado = %+v, err = %v", estado, err)
		}
	})

	t.Run("JSON corrupto falla cerrado y no se reemplaza", func(t *testing.T) {
		repo := repoTemporal(t)
		estado, _ := alcance(repo)
		if err := os.MkdirAll(filepath.Dir(estado.Ruta), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(estado.Ruta, []byte("{corrupto"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := EstadoDiffExterno(repo); err == nil {
			t.Fatal("JSON corrupto conto como consentimiento")
		}
		if _, err := OtorgarDiffExterno(repo); err == nil {
			t.Fatal("el grant reemplazo JSON manipulado")
		}
		contenido, _ := os.ReadFile(estado.Ruta)
		if string(contenido) != "{corrupto" {
			t.Fatal("el JSON manipulado fue sobrescrito")
		}
	})

	t.Run("JSON valido manipulado no cuenta como consentimiento", func(t *testing.T) {
		repo := repoTemporal(t)
		estado, err := OtorgarDiffExterno(repo)
		if err != nil {
			t.Fatal(err)
		}
		contenido, _ := os.ReadFile(estado.Ruta)
		contenido = append(contenido, '\n')
		if err := os.WriteFile(estado.Ruta, contenido, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := EstadoDiffExterno(repo); err == nil {
			t.Fatal("JSON no canonico conto como consentimiento")
		}
		if _, err := OtorgarDiffExterno(repo); err == nil {
			t.Fatal("el grant reemplazo JSON no canonico")
		}
	})

	t.Run("aisla usuario y common-dir", func(t *testing.T) {
		repoA, repoB := repoTemporal(t), repoTemporal(t)
		usarHome(t, filepath.Join(t.TempDir(), "usuario-a"))
		a, err := OtorgarDiffExterno(repoA)
		if err != nil {
			t.Fatal(err)
		}
		b, err := OtorgarDiffExterno(repoB)
		if err != nil {
			t.Fatal(err)
		}
		usarHome(t, filepath.Join(t.TempDir(), "usuario-b"))
		otro, err := OtorgarDiffExterno(repoA)
		if err != nil {
			t.Fatal(err)
		}
		if a.Ruta == b.Ruta || a.Repositorio == b.Repositorio || a.Ruta == otro.Ruta || a.Usuario == otro.Usuario {
			t.Fatalf("alcances no aislados: a=%+v b=%+v otro=%+v", a, b, otro)
		}
	})
}

func TestConsentimientoDiffExternoRechazaSymlinks(t *testing.T) {
	t.Run("destino precreado", func(t *testing.T) {
		repo := repoTemporal(t)
		estado, _ := alcance(repo)
		if err := os.MkdirAll(filepath.Dir(estado.Ruta), 0700); err != nil {
			t.Fatal(err)
		}
		objetivo := filepath.Join(t.TempDir(), "objetivo")
		if err := os.WriteFile(objetivo, []byte("intacto"), 0600); err != nil {
			t.Fatal(err)
		}
		crearSymlink(t, objetivo, estado.Ruta)
		if _, err := OtorgarDiffExterno(repo); err == nil {
			t.Fatal("el grant siguio el symlink de destino")
		}
		contenido, _ := os.ReadFile(objetivo)
		if string(contenido) != "intacto" {
			t.Fatal("el objetivo del symlink fue sobrescrito")
		}
	})

	t.Run("componente app-owned", func(t *testing.T) {
		repo := repoTemporal(t)
		estado, _ := alcance(repo)
		externo := t.TempDir()
		crearSymlink(t, externo, filepath.Dir(filepath.Dir(estado.Ruta)))
		if _, err := OtorgarDiffExterno(repo); err == nil {
			t.Fatal("el grant siguio un componente app-owned enlazado")
		}
		entradas, _ := os.ReadDir(externo)
		if len(entradas) != 0 {
			t.Fatal("se escribio fuera del subtree de consentimiento")
		}
	})
}
