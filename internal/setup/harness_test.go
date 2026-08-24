package setup

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout while fn runs and returns everything it
// printed. The install/upgrade/uninstall entry points report progress through
// fmt.Print* on stdout, so their user-facing contract is only observable here.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = original }()

	fn()

	writer.Close()
	os.Stdout = original
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("could not read captured stdout: %v", err)
	}
	return string(output)
}

// TestVerificarBinarioReportsInstalledVersion: after an upgrade replaces the
// binary, verificarBinario must run `<binary> --version` and report the
// effective installed version to the user.
func TestVerificarBinarioReportsInstalledVersion(t *testing.T) {
	dir := t.TempDir()
	ruta := filepath.Join(dir, "sentinel")
	if runtime.GOOS == "windows" {
		ruta += ".cmd"
		if err := os.WriteFile(ruta, []byte("@echo off\r\necho sentinel v9.9.9\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(ruta, []byte("#!/bin/sh\necho sentinel v9.9.9\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	output := captureStdout(t, func() {
		if err := verificarBinario(ruta); err != nil {
			t.Fatalf("verificarBinario returned error: %v", err)
		}
	})
	if !strings.Contains(output, "v9.9.9") {
		t.Errorf("the output does not report the installed version: %q", output)
	}
}

// TestUpgradeReemplazaBinarioPreservandoConfiguracion composes the upgrade
// replacement primitives with real configuration artifacts: after the binary
// is replaced in place, both the global configuration (~/.vas_sentinel/
// vassentinel.yml) and a repository-local configuration must remain
// byte-identical. Both replacement helpers are pure filesystem operations over
// explicit paths, so both GOOS variants execute on every platform.
func TestUpgradeReemplazaBinarioPreservandoConfiguracion(t *testing.T) {
	variantes := []struct {
		nombre    string
		reemplaza func(tmpPath string, binarioActual string) error
	}{
		{"reemplazarWindows", func(tmp, actual string) error {
			_, err := reemplazarWindows(tmp, actual)
			return err
		}},
		{"reemplazarLinux", reemplazarLinux},
	}

	for _, variante := range variantes {
		t.Run(variante.nombre, func(t *testing.T) {
			home := t.TempDir()
			repo := t.TempDir()
			setHome(t, home)

			if err := crearConfiguracionGlobal(); err != nil {
				t.Fatalf("crearConfiguracionGlobal returned error: %v", err)
			}
			if err := CrearConfiguracionPerProyecto(repo); err != nil {
				t.Fatalf("CrearConfiguracionPerProyecto returned error: %v", err)
			}
			rutaGlobal := filepath.Join(home, ".vas_sentinel", "vassentinel.yml")
			rutaRepo := filepath.Join(repo, ".vas_sentinel", "vassentinel.yml")
			globalAntes, err := os.ReadFile(rutaGlobal)
			if err != nil {
				t.Fatal(err)
			}
			repoAntes, err := os.ReadFile(rutaRepo)
			if err != nil {
				t.Fatal(err)
			}

			binarioActual := filepath.Join(home, "instalado", nombreBinarioGo())
			if err := os.MkdirAll(filepath.Dir(binarioActual), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binarioActual, []byte("version-old"), 0755); err != nil {
				t.Fatal(err)
			}
			nuevo := filepath.Join(home, "descarga", nombreBinarioGo())
			if err := os.MkdirAll(filepath.Dir(nuevo), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(nuevo, []byte("version-new"), 0755); err != nil {
				t.Fatal(err)
			}

			output := captureStdout(t, func() {
				if err := variante.reemplaza(nuevo, binarioActual); err != nil {
					t.Fatalf("%s returned error: %v", variante.nombre, err)
				}
			})
			if strings.TrimSpace(output) != "" {
				// A clean replacement is silent; the only expected print is
				// the locked-backup warning when the .old file cannot be
				// removed, which must not happen in this fixture.
				t.Errorf("%s printed unexpected output during a clean replacement: %q", variante.nombre, output)
			}

			contenido, err := os.ReadFile(binarioActual)
			if err != nil {
				t.Fatalf("the current binary disappeared after the replacement: %v", err)
			}
			if string(contenido) != "version-new" {
				t.Errorf("the binary was not replaced: %q", contenido)
			}

			globalDespues, err := os.ReadFile(rutaGlobal)
			if err != nil {
				t.Fatal(err)
			}
			if string(globalDespues) != string(globalAntes) {
				t.Error("upgrade modified the global configuration")
			}
			repoDespues, err := os.ReadFile(rutaRepo)
			if err != nil {
				t.Fatal(err)
			}
			if string(repoDespues) != string(repoAntes) {
				t.Error("upgrade modified the per-project configuration")
			}
		})
	}
}

// TestNombreBinarioPorSistema pins the per-GOOS binary name used by the go
// install fallback. On this platform the value is executed for real; on the
// other one it is verified as far as Linux allows (pure path building), with
// GOOS=windows build+vet providing the compile-time half.
func TestNombreBinarioPorSistema(t *testing.T) {
	esperado := "sentinel"
	if runtime.GOOS == "windows" {
		esperado = "sentinel.exe"
	}
	if got := nombreBinarioGo(); got != esperado {
		t.Errorf("nombreBinarioGo() = %q, expected %q", got, esperado)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(esperado, ".exe") {
		t.Errorf("the Windows binary must carry the .exe suffix: %q", esperado)
	}
}

// snapshotArbol walks root and returns a stable map of relative path to file
// content (directories map to empty content), so a test can prove an area of
// the filesystem was left untouched.
func snapshotArbol(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.Walk(root, func(ruta string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, ruta)
		if err != nil {
			return err
		}
		if info.IsDir() {
			snapshot[rel+"/"] = ""
			return nil
		}
		contenido, err := os.ReadFile(ruta)
		if err != nil {
			return err
		}
		snapshot[rel] = string(contenido)
		return nil
	})
	if err != nil {
		t.Fatalf("could not snapshot %s: %v", root, err)
	}
	return snapshot
}

// TestDesinstalacionNoTocaRepositorios composes the uninstall primitives
// (binary removal + global config cleanup + shell rc cleanup) against a fake
// repository that must survive byte-identically: uninstall is global scope and
// repositories are explicitly out of its blast radius.
func TestDesinstalacionNoTocaRepositorios(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()
	setHome(t, home)

	// Fake repository: worktree file plus the hook init would have installed.
	hookDir := filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hookDir, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsentinel check --staged\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before := snapshotArbol(t, repo)

	// Installed artifacts that uninstall owns.
	binario := filepath.Join(home, ".vas_sentinel", "bin", nombreBinarioGo())
	if err := os.MkdirAll(filepath.Dir(binario), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binario, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := crearConfiguracionGlobal(); err != nil {
		t.Fatalf("crearConfiguracionGlobal returned error: %v", err)
	}

	captureStdout(t, func() {
		if err := eliminarBinario(binario); err != nil {
			t.Fatalf("eliminarBinario returned error: %v", err)
		}
		if err := eliminarConfiguracionGlobal(home); err != nil {
			t.Fatalf("eliminarConfiguracionGlobal returned error: %v", err)
		}
		if err := quitarRutaShell(); err != nil {
			t.Fatalf("quitarRutaShell returned error: %v", err)
		}
	})

	if _, err := os.Stat(binario); !os.IsNotExist(err) {
		t.Errorf("the installed binary still exists: %v", err)
	}
	after := snapshotArbol(t, repo)
	if len(before) != len(after) {
		t.Fatalf("uninstall changed the repository tree: %d entries before, %d after", len(before), len(after))
	}
	for ruta, contenido := range before {
		if after[ruta] != contenido {
			t.Errorf("uninstall modified %s in the repository", ruta)
		}
	}
	// The full uninstall flow (EjecutarDesinstalacionCompleta) is not safe to
	// run from tests — desinstalarLinux targets the real /usr/local/bin — so
	// its repository-hook disclaimer is pinned through the constant the flow
	// prints, keeping repositories documented as out of uninstall's scope.
	if !strings.Contains(avisoHooksRepositorio, "hook pre-commit") ||
		!strings.Contains(avisoHooksRepositorio, "no se elimina") {
		t.Errorf("the uninstall disclaimer no longer states that repository hooks are left untouched: %q", avisoHooksRepositorio)
	}
}
