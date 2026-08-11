package change

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPerfilDeCambioKind(t *testing.T) {
	casos := []struct {
		nombre, ruta, contenido, mensaje, quiere string
		extra                                    []archivoSintetico
	}{
		{"generated domina infra", "api/salida.pb.go", "package api\n", "feat: genera API", "generated", []archivoSintetico{{"infra/main.tf", "resource \"x\" \"y\" {}\n"}}},
		{"dependency", "go.mod", "module example.test/cambio\n", "chore: actualiza dependencia", "dependency", nil},
		{"infra", "infra/main.tf", "resource \"x\" \"y\" {}\n", "chore: infraestructura", "infra", nil},
		{"ci cd", ".github/workflows/ci.yml", "name: CI\n", "chore: pipeline", "ci_cd", nil},
		{"configuration", "config/app.toml", "name = \"sentinel\"\n", "chore: configuracion", "configuration", nil},
		{"documentation", "README.md", "# Cambio\n", "docs: explica cambio", "documentation", nil},
		{"test only", "internal/demo/demo_test.go", "package demo\n", "test: cubre demo", "test_only", nil},
		{"refactor", "internal/demo/demo.go", "package demo\nfunc Nuevo() { println(\"igual\") }\n", "chore: reorganiza", "refactor", nil},
		{"bugfix", "internal/demo/demo.go", "package demo\nfunc Estado() int { return 1 }\n", "fix: corrige estado", "bugfix", nil},
		{"feature", "internal/nuevo/nuevo.go", "package nuevo\nfunc Crear() {}\n", "feat: crea capacidad", "feature", nil},
	}

	directorioInicial, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(directorioInicial) })

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			repo := t.TempDir()
			prepararRepositorio(t, repo)
			escribirArchivo(t, repo, caso.ruta, caso.contenido)
			for _, extra := range caso.extra {
				escribirArchivo(t, repo, extra.ruta, extra.contenido)
			}
			ejecutarGit(t, repo, "add", ".")
			ejecutarGit(t, repo, "commit", "-m", caso.mensaje)
			if err := os.Chdir(repo); err != nil {
				t.Fatal(err)
			}

			perfil, err := PerfilDeCambio("HEAD~1", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if perfil.Kind != caso.quiere {
				t.Errorf("Kind = %q, quiere %q", perfil.Kind, caso.quiere)
			}
			if perfil.Size.Files != 1+len(caso.extra) || perfil.Size.Added == 0 || perfil.Size.Hunks == 0 {
				t.Errorf("Size = %#v, quiere archivos, líneas y hunks del diff", perfil.Size)
			}
			if perfil.Symbols != (ChangeSymbols{}) {
				t.Errorf("Symbols = %#v, quiere ceros explícitos", perfil.Symbols)
			}
			if caso.nombre == "generated domina infra" && (perfil.FileClasses[ClaseGenerated] != 1 || perfil.FileClasses[ClaseInfra] != 1) {
				t.Errorf("FileClasses = %#v, quiere ambas clases contadas", perfil.FileClasses)
			}
		})
	}
}

// TestPerfilDeCambioConLectorGitFalso prueba que la lógica de clasificación
// es inyectable: un doble de prueba basta, sin invocar git real (revisión de
// T3.2: antes exec.Command("git", ...) estaba acoplado directamente en el
// dominio, sin ningún puerto intermedio que permitiera esto).
func TestPerfilDeCambioConLectorGitFalso(t *testing.T) {
	llamadas := 0
	falso := func(args ...string) (string, error) {
		llamadas++
		switch {
		case args[0] == "diff" && contains(args, "--name-only"):
			return "internal/nuevo/nuevo.go\x00", nil
		case args[0] == "diff" && contains(args, "--name-status"):
			return "M\x00internal/nuevo/nuevo.go\x00", nil
		case args[0] == "diff" && contains(args, "--numstat"):
			return "3\t0\tinternal/nuevo/nuevo.go\n", nil
		case args[0] == "diff":
			return "@@ -0,0 +1,3 @@\n", nil
		case args[0] == "log":
			return "feat: crea capacidad\n", nil
		}
		return "", nil
	}

	perfil, err := perfilDeCambioCon("HEAD~1", "HEAD", falso)
	if err != nil {
		t.Fatalf("perfilDeCambioCon devolvió error: %v", err)
	}
	if perfil.Kind != "feature" {
		t.Errorf("Kind = %q, quiere %q", perfil.Kind, "feature")
	}
	if llamadas == 0 {
		t.Fatal("el doble de prueba nunca se invocó: el test no prueba nada")
	}
}

func contains(args []string, buscado string) bool {
	for _, a := range args {
		if a == buscado {
			return true
		}
	}
	return false
}

type archivoSintetico struct{ ruta, contenido string }

func prepararRepositorio(t *testing.T, repo string) {
	t.Helper()
	ejecutarGit(t, repo, "init")
	escribirArchivo(t, repo, "internal/demo/demo.go", "package demo\nfunc Viejo() { println(\"igual\") }\nfunc Estado() int { return 0 }\n")
	ejecutarGit(t, repo, "add", ".")
	ejecutarGit(t, repo, "commit", "-m", "chore: base")
}

func escribirArchivo(t *testing.T, repo, ruta, contenido string) {
	t.Helper()
	abs := filepath.Join(repo, filepath.FromSlash(ruta))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ejecutarGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	args = append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)
	if salida, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, salida)
	}
}
