package change

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
			if caso.nombre == "generated domina infra" && (perfil.FileClasses[ClaseGenerated] != 1 || perfil.FileClasses[ClaseInfra] != 1) {
				t.Errorf("FileClasses = %#v, quiere ambas clases contadas", perfil.FileClasses)
			}
		})
	}
}

func TestPerfilDeCambioDerivaSimbolosExactos(t *testing.T) {
	casos := []struct {
		nombre, antes, despues string
		quiere                 ChangeSymbols
	}{
		{"solo cuerpo y nombre de parámetro", "func Publica(a int) int { return a }", "func Publica(b int) int { return b + 1 }", ChangeSymbols{Modified: 1, Complete: true}},
		{"firma", "func Publica(a int) int { return a }", "func Publica(a string) int { return len(a) }", ChangeSymbols{Modified: 1, ExportedTouched: 1, Complete: true}},
		{"nombre de parámetro en interfaz", "type Publica interface { Metodo(a int) }", "type Publica interface { Metodo(b int) }", ChangeSymbols{Modified: 1, Complete: true}},
		{"tipo y metodo", "", "type Publico struct { Campo int }; func (Publico) Metodo() {}", ChangeSymbols{Added: 2, ExportedTouched: 2, Complete: true}},
		{"interfaz eliminada", "type Publica interface { Metodo() error }", "", ChangeSymbols{Deleted: 1, ExportedTouched: 1, Complete: true}},
		{"parse invalido", "", "func Rota(", ChangeSymbols{}},
		{"tipo privado alcanzable transitivamente", "type oculto struct { interno }; type interno struct { Campo int }; func Publica() oculto { return oculto{} }", "type oculto struct { interno }; type interno struct { Campo string }; func Publica() oculto { return oculto{} }", ChangeSymbols{Modified: 1, ExportedTouched: 1, Complete: true}},
		{"tipo privado no resuelto", "type oculto struct { campo int }", "type oculto struct { campo string }", ChangeSymbols{Modified: 1}},
		{"var y const", "var Publica = 1; const Constante = 1", "var Publica = 2; const Constante = 2", ChangeSymbols{Modified: 2, ExportedTouched: 2, Complete: true}},
		{"metodos puntero y valor", "type Publico struct{}; func (Publico) Valor() {}", "type Publico struct{}; func (*Publico) Puntero() {}", ChangeSymbols{Added: 1, Deleted: 1, ExportedTouched: 2, Complete: true}},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			repo := t.TempDir()
			prepararRepositorio(t, repo)
			escribirArchivo(t, repo, "internal/demo/api.go", "package demo\n"+caso.antes+"\n")
			ejecutarGit(t, repo, "add", ".")
			ejecutarGit(t, repo, "commit", "-m", "chore: base API")
			escribirArchivo(t, repo, "internal/demo/api.go", "package demo\n"+caso.despues+"\n")
			ejecutarGit(t, repo, "add", ".")
			ejecutarGit(t, repo, "commit", "-m", "feat: API")
			t.Chdir(repo)
			perfil, err := PerfilDeCambio("HEAD~1", "HEAD")
			if err != nil || perfil.Symbols != caso.quiere {
				t.Fatalf("Symbols=%#v err=%v, quiere %#v", perfil.Symbols, err, caso.quiere)
			}
		})
	}
}

func TestSimbolosFallanCerradoFueraDelAnalizador(t *testing.T) {
	contenido := map[string]string{
		"base:api.ts": "export const API = 1", "head:api.ts": "export const API = 2",
		"base:api.pb.go": "no importa", "head:api.pb.go": "tampoco",
	}
	git := func(args ...string) (string, error) { return contenido[args[1]], nil }
	if got := simbolosCambiados(git, []cambioRuta{{"api.ts", "api.ts"}}, "base", "head"); got.Complete {
		t.Fatalf("fuente TypeScript declarada completa: %#v", got)
	}
	if got := simbolosCambiados(git, []cambioRuta{{"api.pb.go", "api.pb.go"}}, "base", "head"); !got.Complete {
		t.Fatalf("generado no debe exigir analizador: %#v", got)
	}
}

func TestSimbolosIncluyenPaqueteYPurezaDeFirma(t *testing.T) {
	git := func(args ...string) (string, error) {
		if args[1] == "base:api.go" {
			return "package antes\nfunc Publica(a int) int { return a }", nil
		}
		return "package despues\nfunc Publica(a int) int { return a }", nil
	}
	got := simbolosCambiados(git, []cambioRuta{{"api.go", "api.go"}}, "base", "head")
	if got.Modified != 1 || got.ExportedTouched != 1 || !got.Complete {
		t.Fatalf("cambio de paquete no detectado: %#v", got)
	}

	fset := token.NewFileSet()
	archivo, _ := parser.ParseFile(fset, "api.go", "package p\nfunc Publica(nombre int) int", 0)
	tipo := archivo.Decls[0].(*ast.FuncDecl).Type
	antes := imprimirAST(fset, tipo)
	primera := imprimirAPI(fset, tipo)
	if despues := imprimirAST(fset, tipo); despues != antes {
		t.Fatalf("imprimirAPI mutó AST: antes=%q después=%q", antes, despues)
	}
	if segunda := imprimirAPI(fset, tipo); segunda != primera {
		t.Fatalf("firma depende del orden: primera=%q segunda=%q", primera, segunda)
	}
}

func TestArbolDeRevisionProtegeOpciones(t *testing.T) {
	var got []string
	_, _ = arbolDeRevision(func(args ...string) (string, error) { got = args; return "tree\n", nil }, "-maliciosa")
	want := []string{"rev-parse", "--verify", "--end-of-options", "-maliciosa^{tree}"}
	if !slices.Equal(got, want) {
		t.Fatalf("git args = %q, quiere %q", got, want)
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
		case args[0] == "rev-parse":
			return args[1][:len(args[1])-7] + "-tree\n", nil
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
